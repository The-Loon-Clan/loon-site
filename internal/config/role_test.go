package config

import "testing"

// Which process this is, and therefore what it is responsible for.
//
// Getting this wrong is quiet in both directions, which is why it is worth
// pinning: a role that reads as "web" everywhere means nothing runs the
// crawler, and every container looks healthy while the index stops growing. A
// role that reads as "all" everywhere means every replica crawls the same
// groups and races to write the same rows.

func TestNoRoleMeansEverything(t *testing.T) {
	// The default has to stay "all". The README's promise is `docker compose
	// up` and a working site, and a default that served pages but ran no jobs
	// would be a site that works and never indexes anything.
	t.Setenv("LOON_ROLE", "")
	if got := Role(); got != RoleAll {
		t.Errorf("unset LOON_ROLE = %q, want %q", got, RoleAll)
	}
	if !RunsJobs() || !ServesWeb() {
		t.Error("the default process does not do both jobs and web")
	}
}

func TestTheThreeRolesAreRecognised(t *testing.T) {
	for _, tc := range []struct {
		set       string
		want      string
		runsJobs  bool
		servesWeb bool
	}{
		{"all", RoleAll, true, true},
		{"web", RoleWeb, false, true},
		{"worker", RoleWorker, true, false},
	} {
		t.Setenv("LOON_ROLE", tc.set)
		if got := Role(); got != tc.want {
			t.Errorf("LOON_ROLE=%q gave role %q, want %q", tc.set, got, tc.want)
		}
		if RunsJobs() != tc.runsJobs {
			t.Errorf("LOON_ROLE=%q: RunsJobs = %v, want %v", tc.set, RunsJobs(), tc.runsJobs)
		}
		if ServesWeb() != tc.servesWeb {
			t.Errorf("LOON_ROLE=%q: ServesWeb = %v, want %v", tc.set, ServesWeb(), tc.servesWeb)
		}
	}
}

func TestExactlyOneRoleDeclinesJobs(t *testing.T) {
	// The property that makes the split safe: with a web and a worker, exactly
	// one of them runs the crawler. If "web" ever started running jobs, scaling
	// the web tier would multiply every scheduled job by the replica count.
	t.Setenv("LOON_ROLE", RoleWeb)
	web := RunsJobs()
	t.Setenv("LOON_ROLE", RoleWorker)
	worker := RunsJobs()

	if web {
		t.Error("the web role runs jobs; scaling the web tier would run each job N times")
	}
	if !worker {
		t.Error("the worker role does not run jobs, which leaves nothing running them")
	}
}

func TestSpellingIsForgiven(t *testing.T) {
	// These arrive from compose files and shell exports, where a stray space or
	// a capital is ordinary. Rejecting "Worker" would be technically defensible
	// and would strand somebody whose only mistake was the shift key.
	for _, s := range []string{"worker", "Worker", "WORKER", " worker", "worker "} {
		t.Setenv("LOON_ROLE", s)
		if got := Role(); got != RoleWorker {
			t.Errorf("LOON_ROLE=%q gave %q, want %q", s, got, RoleWorker)
		}
		if !RoleIsValid() {
			t.Errorf("LOON_ROLE=%q was reported invalid", s)
		}
	}
}

func TestATypoFallsBackToEverythingAndSaysSo(t *testing.T) {
	// A typo must not leave a process doing nothing. "wroker" refusing to boot
	// would take a deployment down over a spelling; "wroker" running NOTHING
	// would be worse — containers up, site answering, crawler never running
	// again, and no error anywhere.
	//
	// So it falls back to doing everything, and RoleIsValid is how the caller
	// knows to warn about what it just ignored.
	for _, s := range []string{"wroker", "jobs", "cron", "true", "1"} {
		t.Setenv("LOON_ROLE", s)
		if got := Role(); got != RoleAll {
			t.Errorf("LOON_ROLE=%q gave %q, want the safe fallback %q", s, got, RoleAll)
		}
		if !RunsJobs() {
			t.Errorf("LOON_ROLE=%q produced a process that runs no jobs", s)
		}
		if RoleIsValid() {
			t.Errorf("LOON_ROLE=%q was reported VALID, so nothing warns about the typo", s)
		}
	}
}

func TestAnEmptyOrExplicitAllIsValid(t *testing.T) {
	for _, s := range []string{"", "all", "ALL", " all "} {
		t.Setenv("LOON_ROLE", s)
		if !RoleIsValid() {
			t.Errorf("LOON_ROLE=%q was reported invalid", s)
		}
	}
}

func TestTheRoleDoesNotDisturbTheFeatureFlags(t *testing.T) {
	// LOON_ROLE lives in the same package as the feature switches and answers a
	// different kind of question. Setting it must not read as switching
	// something on.
	for _, other := range []string{"LOON_TRACKER", "LOON_CHEATCHECK", "LOON_UI_INSPECT", "LOON_HITRUN", "LOON_SEEDLOCK"} {
		t.Setenv(other, "")
	}
	t.Setenv("LOON_ROLE", RoleWorker)

	for name, enabled := range flags {
		if enabled() {
			t.Errorf("setting LOON_ROLE turned on %s", name)
		}
	}
}
