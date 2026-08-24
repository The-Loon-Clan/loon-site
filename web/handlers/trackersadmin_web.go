package handlers

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/trackerdir"
)

// The tracker directory, browsable (the data lives in trackerdir).
//
// READ-ONLY at this stage for the same reason /admin/tv-gaps is: the next
// step wires search against a chosen subset, with per-tracker enablement and
// credentials, and THAT page will own writing. What an operator needs first
// is to see what exists -- 545 trackers is not a list anybody holds in their
// head -- and to judge which are worth wiring: episode-precise search, an id
// search that skips title matching, no anti-bot wall, credentials the site
// actually has.

// trackerRow is one line of the table.
type trackerRow struct {
	Slug    string
	Name    string
	Desc    string
	Type    string
	Content string
	TV      string
	TVIDs   string
	Auth    string
	Guarded bool
	Captcha bool
	TwoFA   bool
	Delay   string
	Domain  string
	Extra   int // additional current domains beyond the first
}

type trackersVM struct {
	Rows []trackerRow
	// Counts over the WHOLE directory, not the filtered view: the header
	// answers "what is out there", the filter answers "show me some of it",
	// and swapping counts under a filter makes the header lie.
	Total, Public, Private, Semi int
	Episode, WithIDs             int
	Type, TV, Query              string
	Shown                        int
	Commit                       string
}

func (w *web) adminTrackers(c *gin.Context) {
	typ := c.Query("type")
	tv := c.Query("tv")
	q := strings.ToLower(strings.TrimSpace(c.Query("q")))

	all := trackerdir.All()
	vm := trackersVM{Total: len(all), Type: typ, TV: tv, Query: c.Query("q"),
		Commit: shortCommit(trackerdir.Origin().Commit)}
	for _, t := range all {
		switch t.Type {
		case "public":
			vm.Public++
		case "private":
			vm.Private++
		default:
			vm.Semi++
		}
		if t.Search.TV == "episode" {
			vm.Episode++
		}
		if len(t.Search.TVIDs) > 0 {
			vm.WithIDs++
		}

		if typ != "" && t.Type != typ {
			continue
		}
		if tv == "episode" && t.Search.TV != "episode" {
			continue
		}
		if q != "" && !trackerMatches(t, q) {
			continue
		}
		vm.Rows = append(vm.Rows, trackerRowOf(t))
	}
	vm.Shown = len(vm.Rows)
	w.render(c, "admin_trackers.html", map[string]any{
		"Title": "Tracker directory",
		"VM":    vm,
	})
}

// trackerMatches is the search box: name, slug, or any domain, current or
// legacy. Legacy included on purpose -- "what was thepiratebay.se" is exactly
// the question an operator brings a dead link here to answer.
func trackerMatches(t trackerdir.Tracker, q string) bool {
	if strings.Contains(strings.ToLower(t.Name), q) || strings.Contains(t.Slug, q) {
		return true
	}
	for _, set := range [][]string{t.Domains, t.LegacyDomains} {
		for _, d := range set {
			if strings.Contains(strings.ToLower(d), q) {
				return true
			}
		}
	}
	return false
}

func trackerRowOf(t trackerdir.Tracker) trackerRow {
	row := trackerRow{
		Slug:    t.Slug,
		Name:    t.Name,
		Desc:    t.Description,
		Type:    t.Type,
		Content: strings.Join(t.Content, ", "),
		TV:      t.Search.TV,
		TVIDs:   strings.Join(t.Search.TVIDs, ", "),
		Auth:    t.Auth,
		Guarded: t.NeedsFlareSolverr,
		Captcha: t.LoginCaptcha,
		TwoFA:   t.Login2FA,
	}
	if t.RequestDelaySeconds > 0 {
		row.Delay = strconv.FormatFloat(t.RequestDelaySeconds, 'f', -1, 64) + "s"
	}
	if len(t.Domains) > 0 {
		row.Domain = t.Domains[0]
		row.Extra = len(t.Domains) - 1
	}
	return row
}

// shortCommit is the 12-character form, tolerant of an empty provenance.
func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}
