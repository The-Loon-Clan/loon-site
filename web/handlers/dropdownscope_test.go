package handlers

import (
	"io/fs"
	"os"
	"regexp"
	"strings"
	"testing"

	site "github.com/the-loon-clan/loon-demo-site"
)

// The account dropdown is the viewer's own things plus ONE staff door — see
// docs/NAVIGATION.md.
//
// This is a crude textual check on the template, and deliberately so. The
// failure it guards against is somebody adding one more staff link to a
// member-facing menu, which is how "New avatars" came to sit between "My
// profile" and "Log out"; a crude check catches that on the line it happens,
// where a subtler one would need the whole page rendered for three roles.

// dropdownMarkup returns the account dropdown's block from the chrome template.
func dropdownMarkup(t *testing.T) string {
	t.Helper()
	// Embedded FS — see credits_web_test.go for why a disk path silently
	// skipped this whole test after the package moved.
	b, err := fs.ReadFile(site.FS, "web/templates/site_chrome.html")
	if err != nil {
		t.Fatalf("read chrome template: %v", err)
	}
	src := string(b)
	// The menu runs from the account dropdown's <div class="dropdown__menu">
	// to the logout form that closes it.
	start := strings.Index(src, `<div class="dropdown__menu">`)
	if start < 0 {
		t.Fatal(`no <div class="dropdown__menu"> in site_chrome.html — has the chrome been restructured?`)
	}
	end := strings.Index(src[start:], `action="/logout"`)
	if end < 0 {
		t.Fatal("no logout form after the dropdown menu — cannot bound the block")
	}
	return src[start : start+end]
}

func TestAccountDropdownHasAtMostOneStaffDoor(t *testing.T) {
	menu := dropdownMarkup(t)
	links := regexp.MustCompile(`href="(/admin[^"]*|/moderation[^"]*)"`).FindAllStringSubmatch(menu, -1)

	seen := map[string]bool{}
	var hrefs []string
	for _, m := range links {
		if seen[m[1]] {
			continue // the same href in an active/inactive branch is one link
		}
		seen[m[1]] = true
		hrefs = append(hrefs, m[1])
	}
	// Admin and moderator each get a door, and they are mutually exclusive
	// branches — so two distinct hrefs is the ceiling, one per role.
	if len(hrefs) > 2 {
		t.Errorf("the account dropdown links %d staff destinations %v — it carries the viewer's "+
			"own things and ONE door per role. Queues and tools belong on the admin subnav "+
			"(admin_views.go); see docs/NAVIGATION.md", len(hrefs), hrefs)
	}
	// And the door must be a door, not a queue: nothing deeper than the
	// landing page each role can actually reach.
	for _, h := range hrefs {
		switch h {
		case "/admin", "/moderation/avatars":
		default:
			t.Errorf("dropdown links %q — a door is /admin (admins) or /moderation/avatars "+
				"(moderators, since /admin gates at RoleAdmin). Anything else is a queue "+
				"or a tool and belongs on the admin subnav", h)
		}
	}
}

// Removing the queue links from the dropdown is only safe if the bar names
// them, or a page becomes unreachable — which is what happened to
// /admin/access, reachable by typed URL alone until it was noticed.
func TestStaffPagesAreOnTheAdminBar(t *testing.T) {
	b, err := os.ReadFile("admin_views.go")
	if err != nil {
		t.Fatal(err)
	}
	nav := string(b)
	for _, href := range []string{
		"/moderation/avatars", // the avatar queue, formerly dropdown-only
		"/moderation",         // community moderation, formerly dropdown-only
		"/admin/access",
		"/admin/covers",
		"/admin/jobs",
		"/admin/plugins",
	} {
		if !strings.Contains(nav, `"`+href+`"`) {
			t.Errorf("%s is on no admin-subnav entry — with the dropdown carrying one door, "+
				"the bar is the only place it is named, so it is unreachable", href)
		}
	}
}
