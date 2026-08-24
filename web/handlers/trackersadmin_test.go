package handlers

import (
	"testing"

	"github.com/the-loon-clan/loon-plugins/trackerdir"
)

// The filter logic, tested against the real embedded directory rather than a
// stub: the directory IS static data, so using it costs nothing and means a
// regeneration that breaks an assumption here fails a host test too.

func TestTrackerMatchesSearchesLegacyDomainsToo(t *testing.T) {
	var probe trackerdir.Tracker
	for _, tr := range trackerdir.All() {
		if len(tr.LegacyDomains) > 0 {
			probe = tr
			break
		}
	}
	if probe.Slug == "" {
		t.Skip("dataset has no legacy domains")
	}
	// The question an operator actually brings here: a dead link. Strip the
	// scheme and match on the middle of the host.
	frag := probe.LegacyDomains[0]
	frag = frag[len("https://") : len("https://")+8]
	if !trackerMatches(probe, frag) {
		t.Fatalf("legacy domain fragment %q of %s did not match", frag, probe.Slug)
	}
	if trackerMatches(probe, "zzz-no-tracker-has-this-zzz") {
		t.Fatal("an impossible query matched")
	}
}

func TestTrackerRowShowsTheFirstDomainAndCountsTheRest(t *testing.T) {
	var multi trackerdir.Tracker
	for _, tr := range trackerdir.All() {
		if len(tr.Domains) > 2 {
			multi = tr
			break
		}
	}
	if multi.Slug == "" {
		t.Skip("dataset has no multi-domain tracker")
	}
	row := trackerRowOf(multi)
	if row.Domain != multi.Domains[0] {
		t.Fatalf("row shows %q, the primary is %q", row.Domain, multi.Domains[0])
	}
	if row.Extra != len(multi.Domains)-1 {
		t.Fatalf("row counts %d extra domains, want %d", row.Extra, len(multi.Domains)-1)
	}
}

func TestTrackerRowFormatsDelayOnlyWhenAsked(t *testing.T) {
	r := trackerRowOf(trackerdir.Tracker{Slug: "x", Domains: []string{"https://x/"}, RequestDelaySeconds: 2.5})
	if r.Delay != "2.5s" {
		t.Fatalf("delay = %q, want 2.5s", r.Delay)
	}
	r = trackerRowOf(trackerdir.Tracker{Slug: "x", Domains: []string{"https://x/"}})
	if r.Delay != "" {
		t.Fatalf("unspecified delay rendered as %q; zero means unspecified, not zero seconds", r.Delay)
	}
}
