package handlers

import (
	"strings"
	"testing"
)

// The access map is a page an operator reads to decide whether the site is
// configured the way they meant. Its whole claim is in its own comment: the
// answers are derived by asking the predicates the middleware uses, never from
// a hand-written list, because "stale access documentation is worse than none
// — it is read as an assurance".
//
// That claim is the thing worth testing. A table that says "public" beside a
// page the gate refuses is a bug an operator cannot see; a table that says
// "members" beside a page anyone can read is one they find out about later.

// gateSays answers what the real gate does with a path, for an anonymous
// visitor, in the given mode — the same predicate the middleware runs.
func gateSays(mode, path string) string {
	// The table lists patterns, not URLs. A concrete path is what the gate
	// actually sees, so :id and /* are resolved to something real.
	p := strings.ReplaceAll(path, "/*", "/anything")
	p = strings.ReplaceAll(p, ":id", "1")
	p = strings.ReplaceAll(p, ":name", "alice")
	if allow, _ := browsingGate(mode, p, p, false); allow {
		return "public"
	}
	return "members"
}

func TestTheAccessMapAgreesWithTheGateInMembersMode(t *testing.T) {
	withBrowsing(t, BrowseMembers)

	for _, row := range buildAccessMap() {
		if row.Access == "staff" {
			// Staff rows are gated by role rather than by the browsing mode;
			// the route-gate test covers that they are mounted behind it.
			continue
		}
		if want := gateSays(BrowseMembers, row.Path); row.Access != want {
			t.Errorf("the access page tells the operator %s is %q, "+
				"but the gate treats it as %q for an anonymous visitor",
				row.Path, row.Access, want)
		}
	}
}

func TestTheAccessMapAgreesWithTheGateInPublicMode(t *testing.T) {
	withBrowsing(t, BrowsePublic)

	for _, row := range buildAccessMap() {
		if row.Access == "staff" {
			continue
		}
		// Per-viewer pages are the one deliberate difference: they say
		// "members" in every mode, because they are about you and there is no
		// you without a session — the gate lets them through and the handler
		// then refuses. The table is describing the outcome, which is right.
		if isPerViewer(row.Path) {
			if row.Access != "members" {
				t.Errorf("%s is about the viewer but the table says %q", row.Path, row.Access)
			}
			continue
		}
		if want := gateSays(BrowsePublic, row.Path); row.Access != want {
			t.Errorf("public mode: the access page says %s is %q, the gate says %q",
				row.Path, row.Access, want)
		}
	}
}

func TestTheDoorsAreShownAsPublicEvenOnAPrivateSite(t *testing.T) {
	// An operator switching to members-only needs to see, on this page, that
	// the doors stay open — otherwise the reasonable conclusion is that the
	// site is now sealed and they are about to lock themselves out.
	withBrowsing(t, BrowseMembers)

	want := map[string]bool{
		"/login": true, "/register": true, "/forgot": true,
		"/api": true, "/rss": true, "/robots.txt": true, "/healthz": true,
	}
	for _, row := range buildAccessMap() {
		if want[row.Path] && row.Access != "public" {
			t.Errorf("%s is shown as %q on a members-only site; it is still reachable "+
				"and the page must say so", row.Path, row.Access)
		}
		delete(want, row.Path)
	}
	for p := range want {
		t.Errorf("%s is not on the access page at all", p)
	}
}

func TestEveryRowIsAnsweredAndLabelled(t *testing.T) {
	// A blank access column reads as "no gate" rather than "we did not work it
	// out", which is the wrong way for this particular table to fail.
	seen := map[string]bool{}
	rows := buildAccessMap()
	if len(rows) != len(accessRoutes) {
		t.Fatalf("buildAccessMap returned %d rows for %d routes", len(rows), len(accessRoutes))
	}
	for _, row := range rows {
		switch row.Access {
		case "public", "members", "staff":
		default:
			t.Errorf("%s has access %q, which is not one of public/members/staff", row.Path, row.Access)
		}
		if row.Label == "" {
			t.Errorf("%s has no label", row.Path)
		}
		if !strings.HasPrefix(row.Path, "/") {
			t.Errorf("%q is not a path", row.Path)
		}
		if seen[row.Path] {
			t.Errorf("%s is listed twice; the operator sees it in two states", row.Path)
		}
		seen[row.Path] = true
	}
}

func TestPerViewerPagesSayWhyTheyAlwaysNeedAnAccount(t *testing.T) {
	// Without the note, a page marked "members" on an otherwise public site
	// looks like a misconfiguration, and the natural response is to go looking
	// for the setting that causes it. There isn't one.
	withBrowsing(t, BrowsePublic)

	for _, row := range buildAccessMap() {
		if !isPerViewer(row.Path) {
			continue
		}
		if row.Note == "" {
			t.Errorf("%s is always members-only but carries no explanation", row.Path)
		}
	}
}

func TestIsPerViewerCoversTheViewersOwnPagesAndNothingElse(t *testing.T) {
	for _, p := range []string{
		"/settings/security", "/settings/privacy", "/subscriptions",
		"/bookmarks", "/inbox", "/achievements", "/calendar", "/rewards",
	} {
		if !isPerViewer(p) {
			t.Errorf("isPerViewer(%q) = false; that page is about the viewer", p)
		}
	}
	for _, p := range []string{
		"/", "/browse", "/search", "/u/alice", "/release/1", "/login", "/api",
	} {
		if isPerViewer(p) {
			t.Errorf("isPerViewer(%q) = true; that page is the same for everyone", p)
		}
	}
}

func TestTheMapIsGroupedSoLikeRowsSitTogether(t *testing.T) {
	// Sorted by access, stable within each group: an operator scanning for
	// "what can a stranger see" reads one block, not thirty interleaved rows.
	withBrowsing(t, BrowseMembers)

	rows := buildAccessMap()
	for i := 1; i < len(rows); i++ {
		if rows[i-1].Access > rows[i].Access {
			t.Fatalf("row %d (%s) breaks the grouping: %q comes after %q",
				i, rows[i].Path, rows[i].Access, rows[i-1].Access)
		}
	}

	// Stability: within one access group, the declared order is preserved, so
	// the page does not reshuffle itself between visits.
	var members []string
	for _, r := range rows {
		if r.Access == "members" {
			members = append(members, r.Path)
		}
	}
	var declared []string
	for _, r := range accessRoutes {
		for _, m := range members {
			if r.Path == m {
				declared = append(declared, r.Path)
				break
			}
		}
	}
	for i := range members {
		if members[i] != declared[i] {
			t.Errorf("the members group is not in declared order: got %v, want %v", members, declared)
			break
		}
	}
}
