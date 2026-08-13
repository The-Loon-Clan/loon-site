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

import "os"

// on reports whether an env var holds one of the affirmative spellings.
func on(name string) bool {
	v := os.Getenv(name)
	return v == "1" || v == "true" || v == "yes"
}

// HitRunEnabled reports whether the operator asked for the rules.
//
// An env flag for the same reason the tracker has one: a system that disables
// a member's downloads is not something a host should acquire by checking the
// repository out.
func HitRunEnabled() bool {
	v := os.Getenv("LOON_DEMO_HITRUN")
	return v == "1" || v == "true" || v == "yes"
}

// SeedLockEnabled reports whether the operator asked for the one-host rule.
//
// Its own flag rather than riding on the tracker's: a site may well want a
// tracker without telling members which machine they may seed from, and the
// failure mode here — somebody locked out of their own torrent — deserves to be
// switched on deliberately.
func SeedLockEnabled() bool {
	v := os.Getenv("LOON_DEMO_SEEDLOCK")
	return v == "1" || v == "true" || v == "yes"
}

// TrackerEnabled reports whether the operator asked for the tracker.
//
// An env flag rather than a constant because the demo is also what people run
// locally to read the code, and a private tracker answering announces is not
// something to switch on by merely checking the repository out.
func TrackerEnabled() bool {
	v := os.Getenv("LOON_DEMO_TRACKER")
	return v == "1" || v == "true" || v == "yes"
}

// CheatCheckEnabled reports whether the operator asked for cheat detection.
//
// Its own flag rather than riding on LOON_DEMO_TRACKER, because the two are
// different decisions: running a tracker is a feature, and judging members'
// accounting is a policy. A site can reasonably want the first without the
// second, and the flag that turns on accusations should have to be typed.
//
// The SAMPLING runs regardless — see the sweep. Only the judging is gated, so
// switching this on starts working at the next sweep rather than the one after.
func CheatCheckEnabled() bool {
	v := os.Getenv("LOON_DEMO_CHEATCHECK")
	return v == "1" || v == "true" || v == "yes"
}

// UIInspectEnabled reports whether the operator asked for the inspector.
func UIInspectEnabled() bool {
	v := os.Getenv("LOON_DEMO_UI_INSPECT")
	return v == "1" || v == "true" || v == "yes"
}
