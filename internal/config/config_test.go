package config

import "testing"

// Every flag in this package answers to the same spellings.
//
// They did not, quite. Each function open-coded `v == "1" || v == "true" ||
// v == "yes"` — six copies of one list, with a helper sitting unused above
// them. Six copies is five chances for one flag to start accepting "on" while
// the others do not, and the operator who hits that reads it as the feature
// being broken rather than the spelling being wrong.
//
// So the table is over the FUNCTIONS, not over one of them: a new flag added
// with its own hand-rolled comparison fails here.
var flags = map[string]func() bool{
	"LOON_HITRUN":     HitRunEnabled,
	"LOON_SEEDLOCK":   SeedLockEnabled,
	"LOON_TRACKER":    TrackerEnabled,
	"LOON_CHEATCHECK": CheatCheckEnabled,
	"LOON_UI_INSPECT": UIInspectEnabled,
}

func TestEveryFlagAcceptsTheSameAffirmatives(t *testing.T) {
	for env, enabled := range flags {
		for _, yes := range []string{"1", "true", "yes"} {
			t.Setenv(env, yes)
			if !enabled() {
				t.Errorf("%s=%q reads as off", env, yes)
			}
		}
	}
}

func TestEveryFlagIsOffUnlessAsked(t *testing.T) {
	// Off is the safe default for all of them: these switch on a tracker, ratio
	// accounting, cheat accusations, and a dev inspector that serves files off
	// disk. Anything that is not an affirmative spelling must be off — a flag
	// that treated "0" as "set, therefore on" would be the worst kind of bug
	// here, because it looks disabled in the environment that has it enabled.
	for env, enabled := range flags {
		for _, no := range []string{"", "0", "false", "no", "off", "maybe", "TRUE"} {
			t.Setenv(env, no)
			if enabled() {
				t.Errorf("%s=%q reads as ON — only 1/true/yes may enable a flag", env, no)
			}
		}
	}
}

func TestFlagsAreIndependent(t *testing.T) {
	// Cheat detection rides on its own flag rather than on the tracker's, and
	// seedlock on its own rather than on either. That is a deliberate decision
	// documented on each function; this is the test that keeps it true, since
	// the natural "simplification" is to have one imply another.
	for env := range flags {
		for other := range flags {
			t.Setenv(other, "")
		}
		t.Setenv(env, "1")
		for other, enabled := range flags {
			if other == env {
				continue
			}
			if enabled() {
				t.Errorf("setting %s also enabled %s", env, other)
			}
		}
	}
}
