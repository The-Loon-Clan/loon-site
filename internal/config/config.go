// Package config reads the site's operator switches.
//
// One place, because they were five identical three-line functions scattered
// across the handlers that happened to need them first, and because a reader
// asking "what can I turn on?" should not have to grep for os.Getenv. The
// production indexer keeps the same package (pkg/config).
//
// Every one of these is an ENV FLAG rather than a constant, and deliberately:
// this demo is also what people run locally to read the code, and a private
// tracker answering announces — or a dev inspector serving files off disk — is
// not something to switch on by merely checking the repository out.
//
// The accepted spellings are "1", "true" and "yes". Anything else, including
// an unset variable, is off. Off is the safe default for all of them.
package config

import (
	"os"
	"strings"
)

// on reports whether an env var holds one of the affirmative spellings.
func on(name string) bool {
	v := os.Getenv(name)
	return v == "1" || v == "true" || v == "yes"
}

// The process kinds this binary can run as.
//
// One image, one binary, three roles — chosen by LOON_ROLE at start-up. The
// plugin framework already filters on this: a plugin declares the kinds it runs
// in (the scraper is worker-only, the tracker is web and api), and core.Boot
// drops any that do not match. 33 of the 44 plugins say so; the host was the
// only thing not asking.
const (
	// RoleAll runs everything in one process: serves pages AND runs jobs.
	//
	// The DEFAULT, and it has to stay the default. The README's promise is
	// `docker compose up` and a working site, and splitting that into two
	// containers by default would trade a working first impression for a
	// property nobody has needed yet at that point.
	RoleAll = "all"

	// RoleWeb serves requests and runs no jobs.
	RoleWeb = "web"

	// RoleWorker runs jobs and background loops. It still starts an HTTP
	// listener — for /healthz, so an orchestrator can tell a wedged worker from
	// a busy one — but nothing routes traffic to it.
	RoleWorker = "worker"
)

// Role reports which kind of process this is.
//
// Unknown values fall back to RoleAll rather than failing, and say so at the
// call site. A typo'd role that refused to boot would take a deployment down
// over a spelling; a typo'd role that silently ran NOTHING would be worse
// still — the containers would be up, the site would answer, and the crawler
// would simply never run again.
func Role() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOON_ROLE"))) {
	case RoleWeb:
		return RoleWeb
	case RoleWorker:
		return RoleWorker
	default:
		return RoleAll
	}
}

// RoleIsValid reports whether LOON_ROLE holds something this understands, so
// the caller can warn about a typo it has just ignored.
func RoleIsValid() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("LOON_ROLE")))
	return v == "" || v == RoleAll || v == RoleWeb || v == RoleWorker
}

// RunsJobs reports whether this process is responsible for scheduled work.
//
// The question almost every caller actually has. Asking it by name keeps
// `role == "worker" || role == "all"` from being written out at each site, one
// of which will eventually get the condition backwards — and a backwards
// condition here means either every replica runs the crawler or none does.
func RunsJobs() bool { return Role() != RoleWeb }

// ServesWeb reports whether this process is meant to receive traffic.
func ServesWeb() bool { return Role() != RoleWorker }

// HitRunEnabled reports whether the operator asked for the rules.
//
// An env flag for the same reason the tracker has one: a system that disables
// a member's downloads is not something a host should acquire by checking the
// repository out.
func HitRunEnabled() bool {
	return on("LOON_HITRUN")
}

// SeedLockEnabled reports whether the operator asked for the one-host rule.
//
// Its own flag rather than riding on the tracker's: a site may well want a
// tracker without telling members which machine they may seed from, and the
// failure mode here — somebody locked out of their own torrent — deserves to be
// switched on deliberately.
func SeedLockEnabled() bool {
	return on("LOON_SEEDLOCK")
}

// TrackerEnabled reports whether the operator asked for the tracker.
//
// An env flag rather than a constant because the demo is also what people run
// locally to read the code, and a private tracker answering announces is not
// something to switch on by merely checking the repository out.
func TrackerEnabled() bool {
	return on("LOON_TRACKER")
}

// CheatCheckEnabled reports whether the operator asked for cheat detection.
//
// Its own flag rather than riding on LOON_TRACKER, because the two are
// different decisions: running a tracker is a feature, and judging members'
// accounting is a policy. A site can reasonably want the first without the
// second, and the flag that turns on accusations should have to be typed.
//
// The SAMPLING runs regardless — see the sweep. Only the judging is gated, so
// switching this on starts working at the next sweep rather than the one after.
func CheatCheckEnabled() bool {
	return on("LOON_CHEATCHECK")
}

// UIInspectEnabled reports whether the operator asked for the inspector.
func UIInspectEnabled() bool {
	return on("LOON_UI_INSPECT")
}
