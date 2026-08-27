package handlers

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestFleetAgeResolvesFinerThanOnlineWindow is the regression for a roster that
// rendered "offline - just now" on every silent agent: the age string was
// borrowed from the TV-gap page, where anything under an hour is "just now",
// while the online badge flips after agentOnlineWindow. An operator reading a
// dead worker was told it had checked in a moment ago.
//
// The invariant is not about any one string: it is that the two cannot
// contradict each other. "just now" must imply online.
func TestFleetAgeResolvesFinerThanOnlineWindow(t *testing.T) {
	for d := time.Duration(0); d <= 30*time.Minute; d += 5 * time.Second {
		online := d <= agentOnlineWindow
		age := fleetAge(d)
		if age == "just now" && !online {
			t.Fatalf("after %s the roster says %q but the badge says offline", d, age)
		}
	}
}

// TestFleetAgeMinuteResolution pins the resolution itself, so a later edit that
// widened the buckets back out would fail here rather than silently reintroduce
// the contradiction above only for hosts with a longer window.
func TestFleetAgeMinuteResolution(t *testing.T) {
	cases := []struct{ d time.Duration; want string }{
		{2 * time.Second, "just now"},
		{42 * time.Second, "42s ago"},
		{4 * time.Minute, "4m ago"},
		{59 * time.Minute, "59m ago"},
		{3 * time.Hour, "3h ago"},
		{25 * time.Hour, "yesterday"},
		{72 * time.Hour, "3 days ago"},
	}
	for _, c := range cases {
		if got := fleetAge(c.d); got != c.want {
			t.Errorf("fleetAge(%s) = %q, want %q", c.d, got, c.want)
		}
	}
}

// TestFleetRosterDoesNotUseHumanAge keeps the two helpers from being confused
// again by name: humanAge is hour-resolution and belongs to the TV gap page.
func TestFleetRosterDoesNotUseHumanAge(t *testing.T) {
	b, err := os.ReadFile("agentadmin_web.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "humanAge(") {
		t.Error("the fleet roster is using humanAge; it needs fleetAge's resolution")
	}
}
