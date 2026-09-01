package handlers

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/core"
)

func withNeutralState(t *testing.T, s neutralState) {
	t.Helper()
	prev := neutralMirror
	t.Cleanup(func() { neutralMirror = prev })
	neutralMirror = atomic.Value{}
	neutralMirror.Store(s)
}

// THE TEST THIS FEATURE EXISTS FOR.
//
// Neutral used to be unreachable: expressed as a multiplier it resolved to
// (1, 0) -- ordinary freeleech, free downloads AND full upload credit, which
// is more generous than intended and failed silently. This drives the real
// contract end to end and asserts the flag actually comes back true, so if the
// upstream algebra ever swallows it again this fails here rather than in
// somebody's ratio.
func TestNeutralReachesThePolicyContract(t *testing.T) {
	withNeutralState(t, neutralState{Hashes: map[string]bool{strings.Repeat("a", 40): true}})

	c := &core.Core{}
	if err := c.Register(pluginapi.PolicyFlagPrefix+"host", hostPolicySource{}); err != nil {
		t.Fatal(err)
	}
	on := pluginapi.ResolvePolicyFlag(context.Background(), c, pluginapi.FlagNeutral,
		pluginapi.MultiplierContext{UserID: 1, InfoHash: strings.Repeat("a", 40)})
	if !on {
		t.Error("a marked torrent did not resolve as neutral through ResolvePolicyFlag")
	}

	off := pluginapi.ResolvePolicyFlag(context.Background(), c, pluginapi.FlagNeutral,
		pluginapi.MultiplierContext{UserID: 1, InfoHash: strings.Repeat("b", 40)})
	if off {
		t.Error("an unmarked torrent resolved as neutral")
	}
}

// The host must have no opinion on flags it does not implement, rather than
// answering "not on". ok=false is the contract's word for "ask somebody else";
// returning a definite false would let this host out-vote a plugin that does
// implement the flag -- except that ANY-combining saves it, which is precisely
// the kind of accident not to rely on.
func TestNeutralSourceHasNoOpinionOnOtherFlags(t *testing.T) {
	withNeutralState(t, neutralState{Hashes: map[string]bool{}})
	on, ok, err := hostPolicySource{}.Flag(context.Background(), "some-future-flag",
		pluginapi.MultiplierContext{UserID: 1})
	if err != nil {
		t.Fatalf("err = %v; this source cannot fail, by construction", err)
	}
	if ok {
		t.Error("the host claimed an opinion on a flag it does not implement")
	}
	if on {
		t.Error("the host asserted a flag it does not implement")
	}
}

// A definite NO for neutral, though: the host HAS looked.
func TestNeutralSourceAnswersDefinitelyForItsOwnFlag(t *testing.T) {
	withNeutralState(t, neutralState{Hashes: map[string]bool{}})
	on, ok, _ := hostPolicySource{}.Flag(context.Background(), pluginapi.FlagNeutral,
		pluginapi.MultiplierContext{UserID: 1, InfoHash: strings.Repeat("c", 40)})
	if !ok {
		t.Error("the host has no opinion on its own flag; it should answer no")
	}
	if on {
		t.Error("an unmarked torrent is neutral")
	}
}

// The site-wide window applies to everything, including an announce with no
// info hash at all.
func TestSiteWideWindowAppliesToEverything(t *testing.T) {
	withNeutralState(t, neutralState{
		Hashes: map[string]bool{},
		Until:  time.Now().Add(time.Hour),
	})
	for _, hash := range []string{strings.Repeat("d", 40), ""} {
		on, ok, _ := hostPolicySource{}.Flag(context.Background(), pluginapi.FlagNeutral,
			pluginapi.MultiplierContext{UserID: 1, InfoHash: hash})
		if !ok || !on {
			t.Errorf("hash %q: window is open but the flag is off", hash)
		}
	}
}

// An EXPIRED window stops applying without anything having to sweep it. The
// mirror holds a timestamp rather than a boolean precisely so that a window
// ends on time even if nothing refreshes the mirror.
func TestExpiredWindowStopsApplying(t *testing.T) {
	withNeutralState(t, neutralState{
		Hashes: map[string]bool{},
		Until:  time.Now().Add(-time.Minute),
	})
	on, _, _ := hostPolicySource{}.Flag(context.Background(), pluginapi.FlagNeutral,
		pluginapi.MultiplierContext{UserID: 1, InfoHash: strings.Repeat("e", 40)})
	if on {
		t.Error("a window that ended a minute ago is still neutral")
	}
}

// Hashes are matched case-insensitively. A client announcing an uppercase hash
// against a lowercase mark would otherwise pay full ratio on a torrent an
// operator believed was neutral -- and nothing would report it.
func TestHashMatchingIsCaseInsensitive(t *testing.T) {
	withNeutralState(t, neutralState{Hashes: map[string]bool{strings.Repeat("a", 40): true}})
	on, _, _ := hostPolicySource{}.Flag(context.Background(), pluginapi.FlagNeutral,
		pluginapi.MultiplierContext{UserID: 1, InfoHash: strings.Repeat("A", 40)})
	if !on {
		t.Error("an uppercase announce missed a lowercase mark")
	}
}

// A mistyped hash is refused rather than stored. Stored, it would sit in the
// list looking exactly like a working entry and never match an announce --
// an operator would believe a torrent was neutral when it was not.
func TestNeutralHashRejectsAnythingNotFortyHex(t *testing.T) {
	good := strings.Repeat("0123456789abcdef", 3)[:40]
	if got := neutralHash("  " + strings.ToUpper(good) + "  "); got != good {
		t.Errorf("a valid hash was not normalised: %q", got)
	}
	for _, bad := range []string{
		"", "short",
		strings.Repeat("a", 39),
		strings.Repeat("a", 41),
		strings.Repeat("g", 40),  // not hex
		strings.Repeat("a", 39) + "-",
	} {
		if got := neutralHash(bad); got != "" {
			t.Errorf("neutralHash(%q) = %q, want rejection", bad, got)
		}
	}
}
