package main

import "testing"

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
	metrics := achievementMetrics(nil)
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
		if d.Slug == "" || d.Name == "" || d.RewardSlug == "" {
			t.Errorf("achievement %+v has an empty required field", d)
		}
	}
}
