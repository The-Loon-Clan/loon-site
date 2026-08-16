package handlers

import (
	"testing"

	"github.com/the-loon-clan/loon-site/internal/storage"
)

// The two halves of the achievements wiring have to agree, and neither says
// anything when they do not.
//
// The plugin's own migration puts it plainly: "A metric with no registered
// source is INERT, never an error — the same rule the engine applies to a
// payout kind with no handler, so a half-configured site degrades rather than
// failing to boot." That is the right call for the plugin and it is exactly
// why the HOST has to check: a seeded achievement naming a metric this host
// never registers sits at zero forever, shows as locked with no progress, and
// logs nothing. It looks like an achievement nobody has earned yet.
func TestSeededAchievementsNameRegisteredMetrics(t *testing.T) {
	// nil DB: the map's KEYS are the contract under test, and building the
	// closures never touches the handle.
	// An empty Conn rather than nil: Conn is a value type now, and the point of
	// the test is unchanged — the map's KEYS are the contract, and building the
	// closures never touches the handle.
	metrics := achievementMetrics(storage.Conn{})
	if len(metrics) == 0 {
		t.Fatal("no achievement metrics registered at all")
	}
	for _, d := range achievementSeeds {
		if _, ok := metrics[d.Metric]; !ok {
			t.Errorf("achievement %q scores on metric %q, which this host does not register — "+
				"it can never progress, and nothing will say so", d.Slug, d.Metric)
		}
	}
}

// A threshold of zero or less trips the table's own CHECK (threshold > 0), so
// the insert fails at runtime and the seed stops halfway — leaving a partial
// catalogue, which is worse than none.
func TestSeededAchievementThresholdsArePositive(t *testing.T) {
	for _, d := range achievementSeeds {
		if d.Threshold <= 0 {
			t.Errorf("achievement %q has threshold %d; the table CHECKs threshold > 0",
				d.Slug, d.Threshold)
		}
	}
}

// Slugs are UNIQUE in the table. A duplicate here would silently insert once
// and skip the rest, because the seed uses ON CONFLICT DO NOTHING.
func TestSeededAchievementSlugsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range achievementSeeds {
		if seen[d.Slug] {
			t.Errorf("duplicate achievement slug %q", d.Slug)
		}
		seen[d.Slug] = true
		if d.Slug == "" || d.Name == "" {
			t.Errorf("achievement %+v has an empty required field", d)
		}
	}
}

// Each achievement must own its badge reward. Rewards are one_off, so a shared
// one means whichever achievement completes first takes it and the other can
// never complete for that member — showing as permanently in progress, with
// nothing logged.
//
// This is not hypothetical: the first version of this seed pointed first-post
// and forum-regular at the same reward, and alice (who already held it from the
// plugin's own demo grants) completed neither while bob completed both.
func TestEachSeededAchievementOwnsItsReward(t *testing.T) {
	// The seed derives the reward slug from the achievement slug, so uniqueness
	// of the achievement slugs IS uniqueness of the rewards. Asserted here as
	// its own statement so the reason survives if that ever stops being true.
	seen := map[string]bool{}
	for _, d := range achievementSeeds {
		if seen[d.Slug] {
			t.Errorf("two achievements would share the reward slug %q", d.Slug)
		}
		seen[d.Slug] = true
	}
}

// The dropdown and the counters must name the same things, in both directions.
//
// Each direction fails silently and differently, which is why one check is not
// enough:
//
//	a source that Counts with no metric  an admin picks it, creates a
//	                                     threshold, and it never moves. The
//	                                     plugin's own SourceDef comment says a
//	                                     source that Counts "must register a
//	                                     MetricSource".
//	a metric with no source              nothing can be scored on it, because
//	                                     the admin has no way to name it. The
//	                                     counter runs and reaches nobody.
//
// The second is what the catalogue looked like before it was registered at all:
// three working counters and an EMPTY dropdown, so no achievement could be
// created through the UI. Nothing logged that either.
//
// Checked with donations BOTH off and on, by driving the package var rather
// than the environment. scripts/go.sh forwards five env vars into the test
// container and LOON_DONATIONS is not one of them, so a version of this that
// set the variable and ran the suite twice would have run the same case twice
// and reported it as two.
func TestTheMetricDropdownAndTheCountersAgree(t *testing.T) {
	for _, donations := range []bool{false, true} {
		t.Run(map[bool]string{false: "donations off", true: "donations on"}[donations], func(t *testing.T) {
			defer withDonations(donations)()
			assertDropdownMatchesCounters(t)
		})
	}
}

// withDonations flips the master switch and returns the restore func.
func withDonations(on bool) func() {
	prev := donationsEnabled
	donationsEnabled = on
	return func() { donationsEnabled = prev }
}

func assertDropdownMatchesCounters(t *testing.T) {
	t.Helper()
	metrics := achievementMetrics(storage.Conn{})
	defs := achievementSourceDefs()
	if len(defs) == 0 {
		t.Fatal("the source catalogue is empty — the achievement admin has nothing to offer")
	}

	inCatalogue := map[string]bool{}
	for _, d := range defs {
		inCatalogue[d.Key] = true
		// Valid() refuses a def that neither Fires nor Counts, which is one
		// that can be selected and then do nothing.
		if err := d.Valid(); err != nil {
			t.Errorf("source %q is not usable: %v", d.Key, err)
		}
		if !d.Counts {
			continue
		}
		if _, ok := metrics[d.Key]; !ok {
			t.Errorf("source %q says it Counts but this host registers no metric for it — "+
				"an admin can pick it and the threshold will never move", d.Key)
		}
	}
	for key := range metrics {
		if !inCatalogue[key] {
			t.Errorf("metric %q is registered but is not in the dropdown — "+
				"nothing can ever be scored on it", key)
		}
	}
}

// donations.total_usd appears only where donations are switched on.
//
// Both halves matter. Present without the flag, it is a threshold nobody on
// that deployment can ever reach. Absent WITH the flag, a site taking real
// money cannot reward the people giving it — which is the state this host was
// in, for a different reason: the column the counter reads did not exist, so
// donations.CreateDonation could not write it and the webhook 500'd on every
// attributed donation.
func TestTheDonationMetricFollowsTheDonationsFlag(t *testing.T) {
	for _, on := range []bool{false, true} {
		defer withDonations(on)()
		_, counted := achievementMetrics(storage.Conn{})["donations.total_usd"]
		var offered bool
		for _, d := range achievementSourceDefs() {
			if d.Key == "donations.total_usd" {
				offered = true
			}
		}
		if counted != on || offered != on {
			t.Errorf("LOON_DONATIONS = %v: counter registered = %v, dropdown entry = %v; "+
				"offered without the counter is a tier that can never be reached, "+
				"counted without the offer is a tier that cannot be created",
				on, counted, offered)
		}
	}
}
