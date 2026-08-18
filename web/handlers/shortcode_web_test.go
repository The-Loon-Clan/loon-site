package handlers

import (
	"html/template"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// shortcodeWeb builds a web with a registry holding the given widgets, which
// is what expansion resolves against — a fake resolver would prove nothing
// about the lookup the real page does.
func shortcodeWeb(t *testing.T, widgets ...core.Widget) (*web, *gin.Context, *core.Core) {
	t.Helper()
	c := &core.Core{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, x := range widgets {
		if err := c.RegisterWidget(x); err != nil {
			t.Fatal(err)
		}
	}
	w := &web{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	gc, _ := gin.CreateTestContext(httptest.NewRecorder())
	gc.Request = httptest.NewRequest("GET", "/pages/team", nil)
	return w, gc, c
}

func echoWidget(slug string) core.Widget {
	return core.Widget{
		Slug: slug, Title: slug, Public: true,
		Render: func(gc *gin.Context) (template.HTML, error) {
			// Echoes its config, so a test can prove the string arrived.
			return template.HTML("<b>" + slug + ":" + core.WidgetConfig(gc) + "</b>"), nil
		},
	}
}

func TestWidgetShortcodeExpansion(t *testing.T) {
	w, gc, reg := shortcodeWeb(t, echoWidget("staff"), echoWidget("ranks-groups"))

	cases := []struct {
		name   string
		body   string
		want   []string
		absent []string
	}{
		{
			name: "a shortcode alone in a paragraph takes the paragraph with it",
			// What markdown produces for a shortcode on its own line. Leaving
			// the <p> would put a panel inside a paragraph, which browsers
			// close early — everything after the widget escapes the paragraph
			// it was written in.
			body:   "<p>Who runs this:</p>\n<p>[widget staff]</p>\n<p>Mail us.</p>",
			want:   []string{`<div class="page-widget"><b>staff:</b></div>`, "<p>Who runs this:</p>", "<p>Mail us.</p>"},
			absent: []string{"<p><div", "[widget"},
		},
		{
			name:   "everything after the slug is the widget's own setting",
			body:   "<p>[widget staff admin]</p>",
			want:   []string{"<b>staff:admin</b>"},
			absent: []string{"[widget"},
		},
		{
			name:   "a shortcode mid-sentence expands where it stands",
			body:   "<p>Ask [widget staff] about it.</p>",
			want:   []string{"Ask ", "<b>staff:</b>", " about it."},
			absent: []string{"[widget"},
		},
		{
			name: "two of them, each with its own setting",
			body: "<p>[widget staff mod]</p><p>[widget ranks-groups assigned]</p>",
			want: []string{"<b>staff:mod</b>", "<b>ranks-groups:assigned</b>"},
		},
		{
			name: "a widget this site does not have leaves no trace for a member",
			body: "<p>[widget tracker-standing]</p>",
			// The whole paragraph goes: an empty <p> is a gap in the prose
			// that an operator cannot see in the editor to remove.
			absent: []string{"[widget", "tracker-standing", "<p></p>"},
		},
		{
			name:   "prose that merely looks like one is left alone",
			body:   "<p>Type [widget] or [widgetstaff] and nothing happens.</p>",
			want:   []string{"[widget]", "[widgetstaff]"},
			absent: []string{"page-widget"},
		},
		{
			name: "an unclosed shortcode does not eat the rest of the page",
			body: "<p>[widget staff</p>\n<p>The rest of the page.</p>",
			want: []string{"The rest of the page."},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(w.expandWidgetShortcodes(gc, template.HTML(tc.body), reg, nil))
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in:\n%s", want, got)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(got, absent) {
					t.Errorf("still carries %q in:\n%s", absent, got)
				}
			}
		})
	}
}

// A body with no shortcode must come back byte-identical: this runs on every
// page render, and a rewrite that reformats untouched prose is a change nobody
// asked for.
func TestWidgetShortcodeLeavesPlainBodiesAlone(t *testing.T) {
	w, gc, reg := shortcodeWeb(t, echoWidget("staff"))
	body := template.HTML("<h2>Rules</h2>\n<p>Be kind. Seed well.</p>")
	if got := w.expandWidgetShortcodes(gc, body, reg, nil); got != body {
		t.Errorf("a page with no shortcode was rewritten:\n%s", got)
	}
}

// A widget saying "nothing to show" is dropped, exactly as a placement drops
// it — that contract is what lets one page serve a site with the plugin and a
// site without.
func TestWidgetShortcodeDropsAnEmptyWidget(t *testing.T) {
	w, gc, reg := shortcodeWeb(t, core.Widget{
		Slug: "quiet", Title: "Quiet", Public: true,
		Render: func(*gin.Context) (template.HTML, error) { return "", nil },
	})
	got := string(w.expandWidgetShortcodes(gc, "<p>[widget quiet]</p>", reg, nil))
	if strings.TrimSpace(got) != "" {
		t.Errorf("an empty widget left %q behind", got)
	}
}

// A members-only widget on a public page renders nothing for a signed-out
// reader rather than refusing them the page: the operator says where, the
// widget says who.
func TestWidgetShortcodeHonoursWidgetVisibility(t *testing.T) {
	w, gc, reg := shortcodeWeb(t, core.Widget{
		Slug: "private", Title: "Private", Public: false, MinRole: core.RoleMod,
		Render: func(*gin.Context) (template.HTML, error) { return "<b>secret</b>", nil },
	})
	got := string(w.expandWidgetShortcodes(gc, "<p>Before.</p><p>[widget private]</p><p>After.</p>", reg, nil))
	if strings.Contains(got, "secret") {
		t.Fatal("a members-only widget rendered for an anonymous reader")
	}
	for _, want := range []string{"Before.", "After."} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lost %q along with the widget", want)
		}
	}
}

// The staff widget's config is a role FLOOR. The refusal matters as much as
// the acceptances: a misspelt role must not fall back to a list the operator
// did not ask to publish.
func TestStaffFloorRole(t *testing.T) {
	for _, tc := range []struct {
		cfg  string
		want core.Role
		ok   bool
	}{
		{"", core.RoleMod, true},
		{"staff", core.RoleMod, true},
		{"mod", core.RoleMod, true},
		{"  Moderator ", core.RoleMod, true},
		{"admin", core.RoleAdmin, true},
		{"ADMIN", core.RoleAdmin, true},
		{"contributor", core.RoleContributor, true},
		{"contributer", 0, false}, // the typo that must not publish anything
		{"user", 0, false},        // the whole membership is a directory, not a staff list
		{"member", 0, false},
		{"banned", 0, false},
	} {
		got, ok := staffFloorRole(tc.cfg)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("staffFloorRole(%q) = %v, %v; want %v, %v", tc.cfg, got, ok, tc.want, tc.ok)
		}
	}
}
