package handlers

import (
	"runtime/debug"
	"strings"
)

// What version this is, so the question "what am I running" has an answer.
//
// It had none. A deployment could be any commit, and the only way to find out
// was to ask whoever deployed it — which is fine until the person asking is
// trying to work out whether a fix they read about is present.

// Version is stamped at build time:
//
//	go build -ldflags "-X github.com/the-loon-clan/loon-site/web/handlers.Version=v0.1.0"
//
// The release workflow sets it from the tag. Left as "dev" for an ordinary
// build, which is honest — a binary built from a working tree is not a release
// and should not claim to be one.
var Version = "dev"

// Commit is the revision, stamped the same way. Falls back to the VCS stamp Go
// embeds automatically, which covers `go build` from a clone even when nobody
// passed ldflags.
var Commit = ""

// BuildInfo returns a single line naming the version, the revision and whether
// the tree was dirty when it was built.
//
// One string rather than three fields, because it is written to a log at boot
// and read by somebody comparing it with a bug report. "v0.1.0 (a1b2c3d)" is
// what they need; the parts are not separately interesting.
func BuildInfo() string {
	rev, dirty := vcsStamp()
	if Commit != "" {
		rev = Commit
	}
	out := Version
	if rev != "" {
		if len(rev) > 7 {
			rev = rev[:7]
		}
		out += " (" + rev
		// Marked, not hidden. A dirty build in production is worth knowing
		// about precisely because it means the source cannot be recovered from
		// the version — there is no commit that matches what is running.
		if dirty {
			out += ", modified"
		}
		out += ")"
	}
	return out
}

// vcsStamp reads the revision Go records automatically for builds from a VCS
// checkout. It returns empty strings when the build had no VCS information —
// which is the normal case inside the Docker build, where the repository is
// COPYed in without .git and `-buildvcs=false` is set.
func vcsStamp() (rev string, dirty bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = strings.EqualFold(s.Value, "true")
		}
	}
	return rev, dirty
}
