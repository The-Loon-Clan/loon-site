package handlers

import (
	"math"
	"testing"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The seeding economy's rules, tested directly. awardFor is pure, which is the
// only reason any of this is testable on a host whose tracker is switched off
// and whose tracker schema therefore does not exist.

const oneTB = int64(1) << 40

func poolSettings(cap float64) seedSettings {
	return seedSettings{Mode: seedModePool, PoolPerTBHour: 100, CapPerHour: cap}
}

func awardsByUser(t *testing.T, got []seedAward) map[int64]seedAward {
	t.Helper()
	m := map[int64]seedAward{}
	for _, a := range got {
		m[a.UserID] = a
	}
	return m
}

// The pool's whole point: the same torrent pays each seeder more when there
// are fewer of them.
func TestPoolPaysMoreWhenFewerSeed(t *testing.T) {
	alone := awardFor([]pluginapi.SeedRow{
		{UserID: 1, InfoHash: "a", SizeBytes: oneTB, Seeders: 1},
	}, poolSettings(0), time.Hour, map[int64]float64{})
	crowded := awardFor([]pluginapi.SeedRow{
		{UserID: 1, InfoHash: "a", SizeBytes: oneTB, Seeders: 4},
	}, poolSettings(0), time.Hour, map[int64]float64{})

	if alone[0].Points != 100 {
		t.Errorf("sole seeder got %d, want the whole 100-point pool", alone[0].Points)
	}
	if crowded[0].Points != 25 {
		t.Errorf("one of four got %d, want a quarter of the pool", crowded[0].Points)
	}
}

// And the shares must SUM to the pool. This is why Seeders is counted from the
// same rows being paid rather than read off the denormalised counter: a divisor
// that disagreed with the payees would mint or destroy points every hour, in a
// direction nobody could see from the outside.
func TestPoolSharesSumToThePool(t *testing.T) {
	rows := []pluginapi.SeedRow{
		{UserID: 1, InfoHash: "a", SizeBytes: oneTB, Seeders: 4},
		{UserID: 2, InfoHash: "a", SizeBytes: oneTB, Seeders: 4},
		{UserID: 3, InfoHash: "a", SizeBytes: oneTB, Seeders: 4},
		{UserID: 4, InfoHash: "a", SizeBytes: oneTB, Seeders: 4},
	}
	total := 0
	for _, a := range awardFor(rows, poolSettings(0), time.Hour, map[int64]float64{}) {
		total += a.Points
	}
	if total != 100 {
		t.Errorf("the four seeders were paid %d between them; the pool was 100", total)
	}
}

// The cap is what stands between the pool and its own incentive. Without it,
// somebody seeding many torrents alone collects a full pool from each.
func TestCapBoundsTheLoneSeederFarm(t *testing.T) {
	var rows []pluginapi.SeedRow
	for i := 0; i < 50; i++ {
		rows = append(rows, pluginapi.SeedRow{
			UserID: 1, InfoHash: string(rune('a' + i)), SizeBytes: oneTB, Seeders: 1,
		})
	}
	uncapped := awardFor(rows, poolSettings(0), time.Hour, map[int64]float64{})
	if uncapped[0].Points != 5000 {
		t.Fatalf("uncapped farm earned %d, want 50 full pools", uncapped[0].Points)
	}
	capped := awardFor(rows, poolSettings(250), time.Hour, map[int64]float64{})
	if capped[0].Points != 250 {
		t.Errorf("capped farm earned %d, want the 250 cap", capped[0].Points)
	}
	if !capped[0].Capped {
		t.Error("the award does not report that the cap bit; the job cannot count what it cannot see")
	}
}

// The cap is a RATE, so a longer accounting period pays proportionally more.
// A catch-up run must not be silently truncated to one hour's worth.
func TestCapScalesWithTheAccountingPeriod(t *testing.T) {
	rows := []pluginapi.SeedRow{{UserID: 1, InfoHash: "a", SizeBytes: 10 * oneTB, Seeders: 1}}
	got := awardFor(rows, poolSettings(250), 2*time.Hour, map[int64]float64{})
	if got[0].Points != 500 {
		t.Errorf("two hours at a 250/hour cap paid %d, want 500", got[0].Points)
	}
}

// Fractions are carried, not dropped. A member earning 0.4 an hour is paid
// nothing in hours one and two and two points across five -- rather than
// nothing, forever, which is what dropping the remainder does to every small
// seeder while paying the large ones in full.
func TestFractionsAreCarriedNotDropped(t *testing.T) {
	// A 1 TB torrent with 250 seeders at 100/TB/hour is 0.4 each per hour.
	rows := []pluginapi.SeedRow{{UserID: 1, InfoHash: "a", SizeBytes: oneTB, Seeders: 250}}
	carry := map[int64]float64{}
	paid := 0
	for hour := 1; hour <= 5; hour++ {
		got := awardFor(rows, poolSettings(0), time.Hour, carry)
		paid += got[0].Points
		carry[1] = got[0].Carry
	}
	if paid != 2 {
		t.Errorf("five hours at 0.4/hour paid %d points, want 2", paid)
	}
	if carry[1] < 0 || carry[1] >= 1 {
		t.Errorf("carry is %v; it must always be a fraction of one point", carry[1])
	}
}

// A capped member's carry is dropped rather than banked. Banking it would let
// the fraction accumulate through every capped hour and pay out the moment
// they dipped below the cap -- the cap failing slowly instead of working.
func TestCappedMembersDoNotBankCarry(t *testing.T) {
	rows := []pluginapi.SeedRow{{UserID: 1, InfoHash: "a", SizeBytes: 10 * oneTB, Seeders: 1}}
	carry := map[int64]float64{1: 0.9}
	got := awardFor(rows, poolSettings(250), time.Hour, carry)
	if got[0].Points != 250 {
		t.Errorf("capped award was %d, want exactly the cap", got[0].Points)
	}
	if got[0].Carry != 0 {
		t.Errorf("a capped member carried %v forward; it should be dropped", got[0].Carry)
	}
}

// Classic pays for size and for time. The loyalty term is the "time" half.
func TestClassicPaysForSizeAndTime(t *testing.T) {
	s := seedSettings{Mode: seedModeClassic, ClassicPerTBHour: 100,
		LoyaltyPctPerMonth: 5, LoyaltyMaxPct: 100}

	fresh := awardFor([]pluginapi.SeedRow{
		{UserID: 1, InfoHash: "a", SizeBytes: oneTB, Seedtime: 0},
	}, s, time.Hour, map[int64]float64{})
	if fresh[0].Points != 100 {
		t.Errorf("a fresh 1 TB seed paid %d, want the base 100", fresh[0].Points)
	}

	// Two months held: +10%.
	aged := awardFor([]pluginapi.SeedRow{
		{UserID: 1, InfoHash: "a", SizeBytes: oneTB, Seedtime: 60 * 24 * 3600},
	}, s, time.Hour, map[int64]float64{})
	if aged[0].Points != 110 {
		t.Errorf("a 1 TB seed held two months paid %d, want 110", aged[0].Points)
	}

	// Twice the size, twice the pay.
	big := awardFor([]pluginapi.SeedRow{
		{UserID: 1, InfoHash: "a", SizeBytes: 2 * oneTB, Seedtime: 0},
	}, s, time.Hour, map[int64]float64{})
	if big[0].Points != 200 {
		t.Errorf("a 2 TB seed paid %d, want double the 1 TB rate", big[0].Points)
	}
}

// Unlike the pool, classic does NOT care how many others are seeding. The two
// economies answer different questions and this is the difference.
func TestClassicIgnoresTheSeederCount(t *testing.T) {
	s := seedSettings{Mode: seedModeClassic, ClassicPerTBHour: 100}
	alone := awardFor([]pluginapi.SeedRow{{UserID: 1, SizeBytes: oneTB, Seeders: 1}}, s, time.Hour, map[int64]float64{})
	crowded := awardFor([]pluginapi.SeedRow{{UserID: 1, SizeBytes: oneTB, Seeders: 99}}, s, time.Hour, map[int64]float64{})
	if alone[0].Points != crowded[0].Points {
		t.Errorf("classic paid %d alone and %d in a crowd; it must not depend on the swarm",
			alone[0].Points, crowded[0].Points)
	}
}

// The loyalty bonus is bounded. Unbounded, it eventually pays more for age
// than for seeding and nothing dislodges the people at the top.
func TestLoyaltyIsCapped(t *testing.T) {
	s := seedSettings{Mode: seedModeClassic, ClassicPerTBHour: 100,
		LoyaltyPctPerMonth: 5, LoyaltyMaxPct: 100}
	// Ten years held would be +600% uncapped.
	got := awardFor([]pluginapi.SeedRow{
		{UserID: 1, SizeBytes: oneTB, Seedtime: 10 * 365 * 24 * 3600},
	}, s, time.Hour, map[int64]float64{})
	if got[0].Points != 200 {
		t.Errorf("a decade-old seed paid %d, want 200 (the +100%% ceiling)", got[0].Points)
	}
}

// Off pays nothing at all, which is the shipped default: an economy that
// starts paying because somebody deployed is not a decision anybody made.
func TestOffPaysNothing(t *testing.T) {
	rows := []pluginapi.SeedRow{{UserID: 1, SizeBytes: oneTB, Seeders: 1}}
	for _, mode := range []string{seedModeOff, "", "POOL "} {
		s := seedSettings{Mode: mode, PoolPerTBHour: 100, ClassicPerTBHour: 100}
		if got := awardFor(rows, s, time.Hour, map[int64]float64{}); len(got) != 0 {
			t.Errorf("mode %q paid %d members; only the two known modes pay", mode, len(got))
		}
	}
}

// Hostile and degenerate rows must not produce a NaN, a negative, or a panic:
// this arithmetic lands in somebody's points balance.
func TestAwardsSurviveDegenerateRows(t *testing.T) {
	rows := []pluginapi.SeedRow{
		{UserID: 1, SizeBytes: 0, Seeders: 1},     // zero-size torrent
		{UserID: 1, SizeBytes: oneTB, Seeders: 0}, // impossible: paid but nobody seeding
		{UserID: 1, SizeBytes: -oneTB, Seeders: 2},
	}
	for _, s := range []seedSettings{poolSettings(0), {Mode: seedModeClassic, ClassicPerTBHour: 100}} {
		for _, a := range awardFor(rows, s, time.Hour, map[int64]float64{}) {
			if a.Points < 0 {
				t.Errorf("mode %s produced a negative award %d", s.Mode, a.Points)
			}
			if math.IsNaN(a.Carry) || math.IsInf(a.Carry, 0) {
				t.Errorf("mode %s produced carry %v", s.Mode, a.Carry)
			}
		}
	}
}

// A zero or negative period pays nothing rather than paying backwards.
func TestNoTimeNoPay(t *testing.T) {
	rows := []pluginapi.SeedRow{{UserID: 1, SizeBytes: oneTB, Seeders: 1}}
	for _, d := range []time.Duration{0, -time.Hour} {
		if got := awardFor(rows, poolSettings(0), d, map[int64]float64{}); len(got) != 0 {
			t.Errorf("elapsed %v paid %d members", d, len(got))
		}
	}
}

// One member seeding several torrents is paid for all of them, summed before
// the cap is applied -- the cap is per member per hour, not per torrent.
func TestCapAppliesToTheMemberNotTheTorrent(t *testing.T) {
	rows := []pluginapi.SeedRow{
		{UserID: 1, InfoHash: "a", SizeBytes: oneTB, Seeders: 1},
		{UserID: 1, InfoHash: "b", SizeBytes: oneTB, Seeders: 1},
		{UserID: 2, InfoHash: "a", SizeBytes: oneTB, Seeders: 1},
	}
	by := awardsByUser(t, awardFor(rows, poolSettings(150), time.Hour, map[int64]float64{}))
	if by[1].Points != 150 {
		t.Errorf("member on two torrents got %d, want the 150 cap (200 uncapped)", by[1].Points)
	}
	if by[2].Points != 100 {
		t.Errorf("member on one torrent got %d, want an uncapped 100", by[2].Points)
	}
	if by[2].Capped {
		t.Error("a member under the cap is reported as capped")
	}
}

// The capped-carry rule, on a period that is not a whole hour -- which is the
// case that exposed it. cap × elapsed then has a fraction of its own, and an
// implementation that merely declines to ADD the member's existing carry still
// banks that one.
func TestCappedMembersCarryNothingOnAPartialHour(t *testing.T) {
	rows := []pluginapi.SeedRow{{UserID: 1, InfoHash: "a", SizeBytes: 100 * oneTB, Seeders: 1}}
	// 250/hour × 1.33h = 332.5, so the capped amount is fractional.
	got := awardFor(rows, poolSettings(250), 80*time.Minute, map[int64]float64{1: 0.7})
	if got[0].Points != 333 {
		t.Errorf("capped award was %d, want 333 (250/hour over 80 minutes)", got[0].Points)
	}
	if got[0].Carry != 0 {
		t.Errorf("a capped member carried %v forward on a partial hour; it must be dropped", got[0].Carry)
	}
}
