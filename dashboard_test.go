package main

import (
	"html/template"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-baseline/session"
	"github.com/the-loon-clan/loon-baseline/webauth"
)

// A HANDLER test, deliberately, because the template sweeps in templates_test.go
// cannot catch this class of regression: they execute a page against their own
// fixture data, so removing "Dash" from adminDashboard's map leaves every one of
// them green. And {{if .Dash.Alerts}} does NOT error on an absent key the way a
// {{range}} or {{len}} does — it renders the page as an empty dashboard and
// passes. Without the assertions below, a staff page could quietly go blank.

// dashWeb builds a web with just enough wired to render a page: the template
// set and a session, and nothing else. Every data source stays nil ON PURPOSE —
// that is the shape this has to survive, and it is what proves the "—" fallback
// is reached rather than a zero being printed.
func dashWeb(t *testing.T) *web {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := &web{log: slog.Default(), tmpls: map[string]*template.Template{}}
	w.auth = webauth.Auth{Session: session.Config{Secret: []byte("test-secret-test-secret-abc")}}
	tmpl, err := parseSet(w, "admin_dashboard.html")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	w.tmpls["admin_dashboard.html"] = tmpl
	return w
}

func getDashboard(t *testing.T, w *web) string {
	t.Helper()
	e := gin.New()
	e.Use(w.auth.Session.Middleware())
	e.GET("/admin", w.adminDashboard)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	return rec.Body.String()
}

func TestDashboardRendersTilesFromTheHandler(t *testing.T) {
	body := getDashboard(t, dashWeb(t))

	if !strings.Contains(body, "</html>") {
		t.Fatal("render stopped early")
	}
	// The handler must actually put tiles on the page. This is the assertion
	// that goes red if adminDashboard stops setting Dash — the template-only
	// sweeps stay green in that case.
	if !strings.Contains(body, "dash-tile__value") {
		t.Error("no tiles reached the page — the handler/template contract is broken")
	}
	// Jobs is unconditional: it reads the scheduler, which always answers.
	if !strings.Contains(body, "Jobs") {
		t.Error("the Jobs tile is missing")
	}
	if strings.Contains(body, "<no value>") {
		t.Error(`rendered a literal "<no value>"`)
	}
}

// TestDashboardShowsDashNotZeroForUnwiredSources is the honesty rule for this
// page: with no database, "Members" must read "—". Printing 0 would state that
// the site has no members, which is a different claim from "not measurable
// here" — and on a staff page a wrong number is one that gets acted on.
func TestDashboardShowsDashNotZeroForUnwiredSources(t *testing.T) {
	if usersDB != nil {
		t.Skip("a real users DB is wired; this asserts the unwired path")
	}
	body := getDashboard(t, dashWeb(t))
	for _, label := range []string{"Members", "Open tickets"} {
		i := strings.Index(body, label)
		if i < 0 {
			t.Errorf("%s tile missing", label)
			continue
		}
		// The value span follows the label within the same tile.
		seg := body[i:min(i+260, len(body))]
		if !strings.Contains(seg, "—") {
			t.Errorf("%s rendered without a dash on an unwired source: %s", label, seg)
		}
	}
}

// TestDashboardAlertsOnlyWhenSomethingIsWrong: an always-present "all clear"
// panel is one staff stop reading, so the section must be absent when empty.
func TestDashboardAlertsOnlyWhenSomethingIsWrong(t *testing.T) {
	body := getDashboard(t, dashWeb(t))
	// Nothing is wired, so there is nothing to alert on.
	if strings.Contains(body, "Needs attention") {
		t.Error("the alerts panel rendered with no alerts to show")
	}
}

// itoa is read at a glance on this page, so its grouping is pinned.
func TestItoaGroupsThousands(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want string
	}{
		{0, "0"}, {7, "7"}, {999, "999"}, {1000, "1,000"},
		{7829, "7,829"}, {26737749, "26,737,749"}, {-1, "—"},
	} {
		if got := itoa(tc.in); got != tc.want {
			t.Errorf("itoa(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestRenderPreservesHandlerStatus is a regression guard with a real cause:
// render() briefly hard-coded 200, which silently overrode the c.Status(404)
// that profile, release and the follow lists set before rendering their
// "not found" body. The pages looked right in a browser and lied to everything
// else.
func TestRenderPreservesHandlerStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := &web{log: slog.Default(), tmpls: map[string]*template.Template{}}
	w.auth = webauth.Auth{Session: session.Config{Secret: []byte("test-secret-test-secret-abc")}}
	tmpl, err := parseSet(w, "admin_dashboard.html")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	w.tmpls["admin_dashboard.html"] = tmpl

	for _, tc := range []struct {
		name string
		set  int
		want int
	}{
		{"untouched defaults to 200", 0, http.StatusOK},
		{"handler set 404 survives", http.StatusNotFound, http.StatusNotFound},
		{"handler set 403 survives", http.StatusForbidden, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := gin.New()
			e.Use(w.auth.Session.Middleware())
			e.GET("/x", func(c *gin.Context) {
				if tc.set != 0 {
					c.Status(tc.set)
				}
				w.render(c, "admin_dashboard.html", map[string]any{"Title": "t", "Dash": dashVM{}})
			})
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
			if rec.Code != tc.want {
				t.Errorf("status %d, want %d", rec.Code, tc.want)
			}
			if !strings.Contains(rec.Body.String(), "</html>") {
				t.Error("body did not render")
			}
		})
	}
}
