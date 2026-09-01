package handlers

import (
	"context"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"

	"github.com/the-loon-clan/loon-site/internal/config"

	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon/schedule"
)

// Paying members for seeding.
//
// Two economies, and an operator picks ONE. They answer different questions and
// running both at once would pay twice for the same hour:
//
//	classic  size and time, the calculation most trackers run. Predictable,
//	         pays for seeding anything, and rewards holding a big torrent for
//	         a long while.
//	pool     each torrent mints a pool every hour and the seeders SPLIT it, so
//	         the fewer of you there are the more each of you earns. Pays for
//	         seeding what nobody else will, which is the thing a tracker
//	         actually struggles to buy.
//
// Neither is on by default. An economy that starts paying because somebody
// deployed is not a decision anybody made, so the mode ships as "off" and the
// operator turns it on at Jobs → Config.
//
// THE ARITHMETIC IN THIS FILE IS PURE. awardFor takes rows and settings and
// returns what each member is owed; nothing in it reads a clock or a database.
// That is what lets the whole economy be tested without a tracker, which
// matters here because this host runs with the tracker switched off.

// Seeding-points modes.
const (
	seedModeOff     = "off"
	seedModeClassic = "classic"
	seedModePool    = "pool"
)

const (
	// seedJobName is also the key its settings are stored under.
	seedJobName = "Seeding points"

	// seedIntervalMin is the accounting period. The rates below are all "per
	// hour" and are scaled by the period actually elapsed, so changing this
	// changes how often members are paid and not how much.
	seedIntervalMin = 60

	// seedFreshFor is how recently a peer must have announced to count as
	// seeding. Generous against the usual 30-minute announce interval: a
	// client that missed one beat is still seeding, and being paid an hour
	// late for an hour you did seed is worse than the reverse.
	seedFreshFor = 75 * time.Minute

	// seedMaxCatchUp bounds the elapsed time a single run will pay for.
	// Without it, a site that was down for a week would mint a week of points
	// in one run -- and the per-member cap is applied per RUN, so the cap
	// would not save it.
	seedMaxCatchUp = 2 * seedIntervalMin * time.Minute

	bytesPerTB = 1 << 40

	// seedLastRunKey stores when this job last paid, beside the operator's
	// economy in the same per-job settings.
	seedLastRunKey = "last_run_unix"
)

// seedSettings is the operator's economy, read from the job's config.
type seedSettings struct {
	Mode string

	// ClassicPerTBHour is points per terabyte seeded per hour, before loyalty.
	ClassicPerTBHour float64
	// LoyaltyPctPerMonth adds to the classic rate the longer a member has held
	// one torrent -- the "time" half of size-and-time. LoyaltyMaxPct bounds it,
	// because an unbounded loyalty term eventually pays more for age than for
	// seeding and nothing dislodges the people at the top.
	LoyaltyPctPerMonth float64
	LoyaltyMaxPct      float64

	// PoolPerTBHour is the pool a torrent mints each hour, per terabyte of its
	// size. Divided among its seeders.
	PoolPerTBHour float64

	// CapPerHour bounds what one member can earn in an hour, 0 for no cap.
	//
	// This is what stands between the pool and its own incentive: a share is
	// pool/seeders, so one member alone on a torrent takes the whole thing,
	// and somebody seeding five hundred dead torrents would take five hundred
	// whole pools an hour. The cap is deliberately a hard edge rather than a
	// curve -- an operator can explain "you can earn at most N an hour" to a
	// member, and cannot explain a curve.
	CapPerHour float64
}

// seedConfigVars declares the economy on /admin/jobs → Config.
//
// Every rate is an integer because the job-config page has no float input, so
// they are all expressed per TERABYTE per hour rather than per gigabyte: a
// gigabyte rate small enough to be sane is smaller than 1 and could not be
// typed. A member seeding 200 GB at the default classic rate earns 2 points an
// hour, which is the scale these defaults are chosen around.
func seedConfigVars() []schedule.JobConfigVar {
	return []schedule.JobConfigVar{
		{Key: "mode", Label: "Mode", Type: schedule.JobConfigString, Default: seedModeOff,
			Description: "off, classic (size × time), or pool (each torrent mints a pool its seeders split). One at a time: both would pay twice for the same hour."},
		{Key: "classic_per_tb_hour", Label: "Classic: points per TB per hour", Type: schedule.JobConfigInt, Default: "10",
			Description: "Classic mode only. A member seeding 200 GB earns this × 0.2 each hour, before loyalty."},
		{Key: "loyalty_pct_per_month", Label: "Classic: loyalty % per month", Type: schedule.JobConfigInt, Default: "5",
			Description: "Classic mode only. Adds this percent to a torrent's rate for every month it has been seeded."},
		{Key: "loyalty_max_pct", Label: "Classic: loyalty cap %", Type: schedule.JobConfigInt, Default: "100",
			Description: "Ceiling on the loyalty bonus. Unbounded, it eventually pays more for age than for seeding."},
		{Key: "pool_per_tb_hour", Label: "Pool: points per TB per hour", Type: schedule.JobConfigInt, Default: "50",
			Description: "Pool mode only. The pool a torrent mints each hour, per TB of its size, split among its seeders."},
		{Key: "cap_per_hour", Label: "Cap: most one member may earn per hour", Type: schedule.JobConfigInt, Default: "250",
			Description: "0 removes the cap. Chiefly guards pool mode, where seeding many rare torrents alone would otherwise earn a full pool from each."},
	}
}

// seedAward is what one member is owed this run, and what is left over.
type seedAward struct {
	UserID int64
	// Points is whole points to credit now.
	Points int
	// Carry is the unpaid fraction to store for next time. Always < 1.
	Carry float64
	// Capped records that the cap bit, so the job can say how often it does.
	Capped bool
}

// awardFor computes every member's payout for one accounting period.
//
// PURE: rows, settings, elapsed and the carried fractions in; awards out. No
// clock, no database, no logging. Every rule in this economy is therefore
// testable directly, which is the only way it gets tested at all on a host
// whose tracker is switched off.
func awardFor(rows []pluginapi.SeedRow, s seedSettings, elapsed time.Duration, carry map[int64]float64) []seedAward {
	if s.Mode != seedModeClassic && s.Mode != seedModePool {
		return nil
	}
	hours := elapsed.Hours()
	if hours <= 0 {
		return nil
	}

	earned := map[int64]float64{}
	for _, r := range rows {
		tb := float64(r.SizeBytes) / bytesPerTB
		if tb <= 0 {
			continue // a zero-size torrent earns nothing in either economy
		}
		switch s.Mode {
		case seedModeClassic:
			earned[r.UserID] += s.ClassicPerTBHour * tb * loyaltyFactor(r.Seedtime, s) * hours
		case seedModePool:
			// Seeders comes from the same rows being paid, so the shares sum
			// to the pool by construction. Guarded anyway: a zero would be a
			// division by zero on a row that exists, which cannot happen and
			// would be a silent NaN in somebody's balance if it did.
			if r.Seeders <= 0 {
				continue
			}
			earned[r.UserID] += (s.PoolPerTBHour * tb / float64(r.Seeders)) * hours
		}
	}

	// The cap applies to the hourly RATE, not to the run, so a longer period
	// pays proportionally more and a catch-up run is not silently truncated to
	// one hour's worth.
	capThisRun := math.Inf(1)
	if s.CapPerHour > 0 {
		capThisRun = s.CapPerHour * hours
	}

	out := make([]seedAward, 0, len(earned))
	for userID, amount := range earned {
		a := seedAward{UserID: userID}
		if amount > capThisRun {
			// Capped: paid the maximum, and nothing is carried forward.
			//
			// Zeroing the carry is the rule, not a rounding detail. Banked, a
			// fraction would accumulate through every capped hour and pay out
			// the moment the member dipped below the cap -- the cap failing
			// slowly instead of working. And it is not enough to simply not ADD
			// their existing carry: cap × elapsed is not a whole number for any
			// period that is not a whole hour, so the capped amount has a
			// fraction of its own that would be banked if it were not dropped
			// here.
			a.Points, a.Carry, a.Capped = int(math.Floor(capThisRun)), 0, true
			out = append(out, a)
			continue
		}
		amount += carry[userID]
		whole := math.Floor(amount)
		a.Points, a.Carry = int(whole), amount-whole
		out = append(out, a)
	}
	return out
}

// loyaltyFactor is the "time" half of size-and-time: 1 plus a percentage for
// every month this torrent has been held, bounded.
func loyaltyFactor(seedtimeSecs int64, s seedSettings) float64 {
	if seedtimeSecs <= 0 || s.LoyaltyPctPerMonth <= 0 {
		return 1
	}
	months := float64(seedtimeSecs) / (30 * 24 * 3600)
	pct := s.LoyaltyPctPerMonth * months
	if s.LoyaltyMaxPct > 0 && pct > s.LoyaltyMaxPct {
		pct = s.LoyaltyMaxPct
	}
	return 1 + pct/100
}

// wireSeedPoints registers the job, its economy, and the loop that runs it.
//
// Registered on every process so /admin/jobs can list it and "Run now" has
// something to enqueue against; the loop only turns where jobs run, which is
// the same split every other job here uses.
func (w *web) wireSeedPoints(c *core.Core, jobSettings schedule.JobConfigStore, logger *slog.Logger) {
	job := schedule.RegisterJob(seedJobName,
		"Pays bonus points for seeding. Two economies -- classic (size × time) and pool (each torrent mints a pool its seeders split) -- and the operator picks one at Config. Off until they do.")
	job.IntervalMin = seedIntervalMin
	job.DeclareConfig(jobSettings, seedConfigVars()...)
	job.SetTrigger(triggerProtected(job, func() { w.runSeedPoints(context.Background(), c, job, jobSettings, logger) }))
	if config.RunsJobs() {
		go schedule.ServiceLoop(context.Background(), job,
			seedIntervalMin*time.Minute, seedIntervalMin*time.Minute,
			func(ctx context.Context) { w.runSeedPoints(ctx, c, job, jobSettings, logger) })
	}
}

// runSeedPoints pays one accounting period.
func (w *web) runSeedPoints(ctx context.Context, c *core.Core, job *schedule.JobInfo, jobSettings schedule.JobConfigStore, logger *slog.Logger) {
	job.SetRunning()
	next := time.Now().Add(seedIntervalMin * time.Minute)

	s := readSeedSettings(job)
	if s.Mode == seedModeOff {
		job.SetIdle(next)
		return
	}
	// The swarm comes from the TRACKER, through pluginapi's snapshot seam.
	// Absent is the normal state of a host with no tracker -- on an
	// indexer-flavoured site the plugin never booted and never registered it --
	// and saying so is the point: an operator who switched this economy on
	// while the tracker is off has made a mistake that no amount of "0 paid"
	// would ever reveal.
	//
	// This used to be a direct read of tracker.user_stats and tracker.torrents
	// from this host's own store. That worked and was flagged rather than
	// hidden; the seam replaces it, so the host no longer depends on the shape
	// of another plugin's tables.
	snap, ok := pluginapi.SeedingSnapshots(c)
	if !ok {
		job.SetError("the tracker is not running on this site, so there is no swarm to pay for")
		return
	}

	elapsed := seedElapsed(ctx, jobSettings, time.Now())
	rows, err := snap.SeedingSnapshot(ctx, seedFreshFor)
	if err != nil {
		job.SetError("read swarm: " + err.Error())
		return
	}
	carry, err := w.data.SeedPointsCarry(ctx)
	if err != nil {
		// Carrying nothing pays slightly less this hour and loses no member
		// their fractions permanently, because the remainder is recomputed
		// from the same rows next run. Failing the whole payout would be worse.
		logger.Warn("seed points carry", "job", seedJobName, "err", err)
		carry = map[int64]float64{}
	}

	paid, capped := 0, 0
	for _, a := range awardFor(rows, s, elapsed, carry) {
		if a.Capped {
			capped++
		}
		if a.Points > 0 {
			if err := w.data.CreditSeedPoints(ctx, a.UserID, a.Points, seedLedgerDesc(s.Mode)); err != nil {
				// One member's row is not everybody's hour. Their carry is left
				// untouched, so the fraction is still owed next run.
				logger.Warn("credit seed points", "job", seedJobName, "user", a.UserID, "err", err)
				continue
			}
			paid += a.Points
		}
		if err := w.data.SetSeedPointsCarry(ctx, a.UserID, a.Carry); err != nil {
			logger.Warn("save seed points carry", "job", seedJobName, "user", a.UserID, "err", err)
		}
	}

	logger.Info("seeding points paid", "job", seedJobName, "mode", s.Mode,
		"seeding_rows", len(rows), "points", paid, "capped_members", capped)
	if err := jobSettings.SetJobSetting(ctx, seedJobName, seedLastRunKey, strconv.FormatInt(time.Now().Unix(), 10)); err != nil {
		logger.Warn("save seed points last run", "job", seedJobName, "err", err)
	}
	job.SetIdle(next)
}

// seedElapsed is how long this run is paying for: the time since the last one,
// clamped.
//
// Clamped at BOTH ends and each end is a different failure. With no recorded
// last run -- a first run, or a wiped setting -- it pays exactly one interval
// rather than everything since the epoch. And a long outage is capped at
// seedMaxCatchUp, because the per-member cap is per RUN: without this, one run
// after a week down would pay a week of points and the cap would scale up with
// it rather than restrain it.
func seedElapsed(ctx context.Context, jobSettings schedule.JobConfigStore, now time.Time) time.Duration {
	const oneInterval = seedIntervalMin * time.Minute
	vals, err := jobSettings.GetJobSettings(ctx, seedJobName)
	if err != nil {
		return oneInterval
	}
	secs, err := strconv.ParseInt(vals[seedLastRunKey], 10, 64)
	if err != nil || secs <= 0 {
		return oneInterval
	}
	d := now.Sub(time.Unix(secs, 0))
	switch {
	case d <= 0:
		return 0
	case d > seedMaxCatchUp:
		return seedMaxCatchUp
	}
	return d
}

// readSeedSettings reads the operator's economy off the job.
func readSeedSettings(job *schedule.JobInfo) seedSettings {
	return seedSettings{
		Mode:               strings.ToLower(strings.TrimSpace(job.GetConfigString("mode"))),
		ClassicPerTBHour:   float64(job.GetConfigInt("classic_per_tb_hour")),
		LoyaltyPctPerMonth: float64(job.GetConfigInt("loyalty_pct_per_month")),
		LoyaltyMaxPct:      float64(job.GetConfigInt("loyalty_max_pct")),
		PoolPerTBHour:      float64(job.GetConfigInt("pool_per_tb_hour")),
		CapPerHour:         float64(job.GetConfigInt("cap_per_hour")),
	}
}

// seedLedgerDesc names the economy in the member's own points history, so a
// row is explicable a month later when the mode has changed.
func seedLedgerDesc(mode string) string {
	if mode == seedModePool {
		return "Seeding (pool share)"
	}
	return "Seeding"
}
