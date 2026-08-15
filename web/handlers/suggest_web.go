package handlers

import (
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// The quick-search dropdown.
//
// A container swap, exactly as docs/ASYNC.md §2a describes: the input asks for
// a fragment, the fragment replaces one region, and the server returns HTML
// rather than JSON for the client to render. No new pattern — which is the
// point of having had a standard first.
//
// Speed is three things, and only one of them is the query:
//
//	the SOURCE      catalogue titles (12k rows, 6-8ms) rather than release
//	                titles (160k, 40-50ms). See storage.Suggest.
//	the CACHE       prefixes repeat enormously — everyone typing "brea" passes
//	                through the same four. A short TTL turns most keystrokes
//	                into no database work at all.
//	NOT ASKING      the debounce, the two-character floor and the in-flight
//	                abort in the markup. The fastest query is the one a
//	                keystroke did not send.

// suggestCacheTTL is deliberately short. The catalogue changes when the
// metadata chain runs, and a stale dropdown that omits a show somebody just
// added is a bug report nobody can reproduce. Thirty seconds is long enough to
// absorb a burst of typing and short enough that nobody notices.
const suggestCacheTTL = 30 * time.Second

// suggestPage serves GET /search/suggest.
func (w *web) suggestPage(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))

	// An empty region, not an empty page. The dropdown is hidden by having
	// nothing in it, so a short query has to return the same shape as a query
	// with no matches — anything else leaves the last result list on screen
	// while the reader deletes what produced it.
	if len([]rune(q)) < 2 {
		w.renderFragment(c, shellPage, "suggest", map[string]any{})
		return
	}

	var rows []suggestVM
	// Lower-cased, because "Star", "STAR" and "star" are the same question and
	// caching them separately triples the misses for nothing.
	key := "suggest:" + strings.ToLower(q)
	if !w.cacheGet(c.Request.Context(), key, &rows) {
		found, ok := w.data.Suggest(c.Request.Context(), q)
		if !ok {
			// A failed lookup renders as no suggestions rather than as an
			// error. The reader is mid-word: there is nothing for them to do
			// about it, and a red box under a search box they are still typing
			// in is worse than silence. It is logged, which is where it can be
			// acted on.
			w.log.Warn("search suggest", "title", q)
			w.renderFragment(c, shellPage, "suggest", map[string]any{})
			return
		}
		for _, s := range found {
			rows = append(rows, suggestVM{Title: s.Title, Kind: s.Kind, Year: s.Year})
		}
		w.cacheSet(c.Request.Context(), key, rows, suggestCacheTTL)
	}

	w.renderFragment(c, shellPage, "suggest", map[string]any{
		"Suggestions": rows,
		"Query":       q,
	})
}

// suggestVM is one dropdown row as the template wants it.
//
// Its own type rather than storage.Suggestion because it is cached as JSON, and
// a view model that happens to equal a storage row today will not tomorrow —
// the cached shape has to be the one the template reads.
type suggestVM struct {
	Title string `json:"t"`
	Kind  string `json:"k"`
	Year  int    `json:"y"`
}

// suggestSearchURL is where a chosen suggestion goes.
//
// It SEARCHES rather than jumping to the work's own page, because this site has
// no page for a catalogue entry — there is no /title/:id to link to. Linking a
// suggestion somewhere that does not exist is the kind of dead end that only
// shows up after the dropdown is written, so it is decided here and stated.
func suggestSearchURL(title string) string {
	return "/search?q=" + url.QueryEscape(title)
}
