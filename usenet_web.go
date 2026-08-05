package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-baseline/cache"
	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Public usenet surface only: NZB download + the search/browse view models.
// The plugin's ADMIN pages (setup wizard, crawler status) are plugin-owned
// views mounted generically in main.go — see admin_views.go.

// newznabAPI is the Newznab/Torznab endpoint (/api + /rss). The plugin owns the
// XML; the host parses the query + serves the response. Open (no apikey check) —
// it's a demo; a real host validates apikey against its user store here.
//
// Responses are read through the host cache, using the SAME key + namespace as
// the loon-api read tier (pluginapi.NewznabCacheKey) so a shared Redis is
// hit-compatible. The events subscriber in main() clears this namespace on an
// ingest, so entries only go stale when new releases land — which is why the TTL
// can be long.
func (w *web) newznabAPI(c *gin.Context) {
	if w.usenetAPI == nil {
		c.String(http.StatusServiceUnavailable, "indexer not configured")
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	// Clamp untrusted paging params before handing them to the plugin — a
	// negative or huge limit shouldn't reach the query layer.
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	req := pluginapi.NewznabRequest{
		Function:   c.Query("t"),
		Query:      c.Query("q"),
		Categories: parseCats(c.Query("cat")),
		Limit:      limit,
		Offset:     offset,
		ID:         c.Query("id"),
		BaseURL:    requestBaseURL(c),
		Title:      "loon demo indexer",
		APIKey:     c.Query("apikey"),
	}

	// Cache read functions; t=get streams NZB bytes, don't hold those.
	cacheable := w.cache != nil && req.Function != "get"
	var key string
	if cacheable {
		key = pluginapi.NewznabCacheKey(req)
		var cached pluginapi.NewznabResult
		if ok, _ := cache.GetJSON(c.Request.Context(), w.cache, key, &cached); ok {
			writeNewznab(c, cached, "hit")
			return
		}
	}
	res, err := w.usenetAPI.Newznab(c.Request.Context(), req)
	if err != nil {
		c.String(http.StatusInternalServerError, "api error")
		return
	}
	if cacheable {
		// Long TTL is safe: an ingest invalidates the namespace, so entries stay
		// fresh until new releases land.
		_ = cache.SetJSON(c.Request.Context(), w.cache, key, res, time.Hour)
	}
	writeNewznab(c, res, "miss")
}

// sanitizeFilename strips characters that would break the quoted
// Content-Disposition filename (quotes, backslashes, control chars). Go's
// net/http already blocks CRLF header injection; this keeps the quoting
// intact for crawled subjects that happen to contain a literal quote.
func sanitizeFilename(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == '"' || r == '\\' {
			return -1
		}
		return r
	}, s)
}

func writeNewznab(c *gin.Context, res pluginapi.NewznabResult, status string) {
	if res.Filename != "" {
		c.Header("Content-Disposition", `attachment; filename="`+sanitizeFilename(res.Filename)+`"`)
	}
	c.Header("X-Cache", status)
	c.Data(http.StatusOK, res.ContentType, res.Body)
}

func requestBaseURL(c *gin.Context) string {
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host
}

// parseCats splits a Newznab cat= value ("5070,2040") into category ids.
func parseCats(s string) []int {
	if s == "" {
		return nil
	}
	var out []int
	for _, part := range strings.Split(s, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(part)); err == nil && n > 0 {
			out = append(out, n)
		}
	}
	return out
}

// releasePage renders the detail view for one release (metadata, tags, file
// list, download button).
func (w *web) releasePage(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.String(http.StatusBadRequest, "bad id")
		return
	}
	if w.usenet == nil {
		w.render(c, "release.html", map[string]any{"Title": "Release", "Missing": true})
		return
	}
	d, ok, err := w.usenet.ReleaseByID(c.Request.Context(), id)
	if err != nil || !ok {
		c.Status(http.StatusNotFound)
		w.render(c, "release.html", map[string]any{"Title": "Not found", "Missing": true})
		return
	}
	vm := toReleaseVM(d)
	if w.catalogCovers != nil {
		if url, has, _ := w.catalogCovers.ReleaseCover(c.Request.Context(), id); has {
			vm.Cover = url
		}
	}
	w.render(c, "release.html", map[string]any{"Title": d.Title, "Release": vm})
}

type releaseFileVM struct {
	Name     string
	Size     string
	Segments int
}

type releaseVM struct {
	ID       int64
	Title    string
	Size     string
	Posted   string
	Group    string
	Poster   string
	Category string
	Cover    string
	Tags     []string
	Files    []releaseFileVM
}

func toReleaseVM(d pluginapi.ReleaseDetail) releaseVM {
	vm := releaseVM{
		ID: d.ID, Title: d.Title, Size: humanBytes(d.Size),
		Group: d.Group, Poster: d.Poster, Category: d.Category, Posted: "—",
	}
	if !d.Posted.IsZero() {
		vm.Posted = d.Posted.Format("2006-01-02 15:04")
	}
	for _, t := range []string{d.Resolution, d.Source, d.Codec, d.Audio, d.Language} {
		if t != "" {
			vm.Tags = append(vm.Tags, t)
		}
	}
	for _, f := range d.Files {
		vm.Files = append(vm.Files, releaseFileVM{Name: f.Filename, Size: humanBytes(f.Bytes), Segments: f.Segments})
	}
	return vm
}

// nzbDownload serves the decompressed .nzb bytes for a release id.
func (w *web) nzbDownload(c *gin.Context) {
	if w.usenet == nil {
		c.String(http.StatusServiceUnavailable, "indexer not configured")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.String(http.StatusBadRequest, "bad id")
		return
	}
	data, filename, err := w.usenet.NZB(c.Request.Context(), id)
	if err != nil || len(data) == 0 {
		c.String(http.StatusNotFound, "not found")
		return
	}
	if filename == "" {
		filename = "download.nzb"
	}
	c.Header("Content-Disposition", `attachment; filename="`+sanitizeFilename(filename)+`"`)
	c.Data(http.StatusOK, "application/x-nzb", data)
}

// ── search view model ───────────────────────────────────────────────

// searchRow is one release as the listing templates want it: preformatted
// where formatting is fixed, raw where the template still has a choice
// (PostedAt for {{timeAgo}}, SizeBytes for sorting/tooltips). Shared by the
// home page, /search and /browse — the fields a given page ignores cost one
// struct field, not a query.
type searchRow struct {
	ID         int64     // release id — /release/<id> and /nzb/<id>
	Title      string    // release title as indexed
	Size       string    // human-readable size ("4.5 GB")
	SizeBytes  int64     // raw byte count, for sorts/tooltips
	Posted     string    // "2006-01-02", or "—" when the post date is unknown
	PostedAt   time.Time // raw post time; ZERO when unknown — feed {{timeAgo}}
	Category   string    // display name ("TV/Anime"); empty without the catalog
	CategoryID int       // Newznab category id — /browse?cat=<id>
	Group      string    // newsgroup the release was assembled from
	Resolution string    // "1080p" etc; "" when unknown — the quality badge
	Source     string    // "BluRay"/"WEB-DL" etc; "" when unknown
	Cover      string    // cover-art URL; "" = none, render the fallback tile
	Tags       []string  // the non-empty Resolution/Source/Codec/Audio/Language
}

func toSearchRows(rs []pluginapi.Release) []searchRow {
	out := make([]searchRow, len(rs))
	for i, r := range rs {
		row := searchRow{
			ID: r.ID, Title: r.Title, Size: humanBytes(r.Size), SizeBytes: r.Size,
			Posted: "—", PostedAt: r.Posted, Category: r.Category, CategoryID: r.CategoryID,
			Group: r.Group, Resolution: r.Resolution, Source: r.Source,
		}
		if !r.Posted.IsZero() {
			row.Posted = r.Posted.Format("2006-01-02")
		}
		for _, t := range []string{r.Resolution, r.Source, r.Codec, r.Audio, r.Language} {
			if t != "" {
				row.Tags = append(row.Tags, t)
			}
		}
		out[i] = row
	}
	return out
}

// ── home page: usenet-derived blocks ────────────────────────────────

const (
	// homeReleaseWindow is how many recent releases one home render pulls.
	// Deliberately wider than any single panel shows: the featured strip picks
	// the newest releases that actually HAVE cover art out of this same list,
	// so the whole page is still one release query.
	homeReleaseWindow = 60
	homeTableRows     = 25 // rows in the main listing table
	homeFeatured      = 6  // posters in the featured strip
	homeTopGroups     = 5  // rows in the busiest-groups panel

	homeRowsKey   = "home:rows:v1"   // []searchRow, cover-decorated
	homeGroupsKey = "home:groups:v1" // homeGroupsVM
)

// homeReleases returns the recent-release rows for the home page, already
// cover-decorated, plus whether they came from the page cache (the X-Cache
// header). The DECORATED rows are what's cached, so a hit costs neither the
// release query nor the cover lookup. A read error degrades to no rows.
func (w *web) homeReleases(ctx context.Context) ([]searchRow, bool) {
	var rows []searchRow
	if w.cacheGet(ctx, homeRowsKey, &rows) {
		return rows, true
	}
	res, err := w.usenet.Browse(ctx, "", homeReleaseWindow)
	if err != nil {
		w.logger().Error("home releases", "err", err)
		return nil, false
	}
	rows = toSearchRows(res)
	w.attachCovers(ctx, rows)
	w.cacheSet(ctx, homeRowsKey, rows, 30*time.Second)
	return rows, false
}

// capRows is the first n rows (or all of them) — the panels are slices of one
// fetch, not separate queries.
func capRows(rows []searchRow, n int) []searchRow {
	if len(rows) <= n {
		return rows
	}
	return rows[:n]
}

// featuredRows picks the poster strip: the newest releases that HAVE cover art,
// so the strip is never a row of blank tiles. When fewer than n are covered it
// tops up with the newest uncovered releases (which render the gradient
// fallback) rather than showing a short strip.
func featuredRows(rows []searchRow, n int) []searchRow {
	out := make([]searchRow, 0, n)
	for _, r := range rows {
		if r.Cover != "" {
			if out = append(out, r); len(out) == n {
				return out
			}
		}
	}
	for _, r := range rows {
		if r.Cover == "" {
			if out = append(out, r); len(out) == n {
				break
			}
		}
	}
	return out
}

// siteStatsVM are the site-wide figures on the home stat strip. Every field is
// derived from a real capability read — there is no placeholder here. A field
// the stack cannot answer stays zero and the template drops that tile.
type siteStatsVM struct {
	// Releases is the number of indexed NZBs, summed over the ACTIVE groups
	// (that is exactly what the Groups() capability reports). Releases in a
	// group an admin has since deactivated are not counted.
	Releases int64
	// Groups is how many newsgroups are being crawled — Groups() returns only
	// the active ones.
	Groups int
	// RetentionDays is the deepest per-group crawl depth any active group
	// reports. NOTE: the public Groups() read path does not select the
	// per-group override today, so this is 0 on this host and the retention
	// tile should stay hidden until it is populated. It is wired, not faked.
	RetentionDays int
	// Categories is how many top-level catalog categories an admin enabled.
	Categories int
}

// groupRowVM is one newsgroup in the "busiest groups" panel — the truthful
// stand-in for the mockup's "popular this week", which needs per-release grab
// counts this indexer does not collect.
type groupRowVM struct {
	Rank int    // 1-based position in the panel
	Name string // newsgroup name
	NZBs int64  // releases indexed from it
	URL  string // /search?group=<name>
}

// homeGroupsVM is the cached, derived shape of ONE Groups() read: the stat
// strip's figures plus the busiest-groups rows.
type homeGroupsVM struct {
	Stats siteStatsVM
	Top   []groupRowVM
}

// homeGroups reads the active groups once and derives both the stat strip and
// the busiest-groups panel from them. Cached for a minute — the underlying
// query is a GROUP BY over the whole nzbs table, not something a page view
// should pay for. ok is false when the read failed (the caller omits both).
func (w *web) homeGroups(ctx context.Context) (homeGroupsVM, bool) {
	var vm homeGroupsVM
	if w.cacheGet(ctx, homeGroupsKey, &vm) {
		return vm, true
	}
	gs, err := w.usenet.Groups(ctx)
	if err != nil {
		w.logger().Error("home groups", "err", err)
		return homeGroupsVM{}, false
	}
	vm.Stats.Groups = len(gs)
	for _, g := range gs {
		vm.Stats.Releases += g.NZBs
		if g.RetentionDays > vm.Stats.RetentionDays {
			vm.Stats.RetentionDays = g.RetentionDays
		}
	}
	ranked := append([]pluginapi.GroupInfo(nil), gs...)
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].NZBs > ranked[j].NZBs })
	for _, g := range ranked {
		// A "busiest groups" list padded with zero-release groups is noise.
		if g.NZBs == 0 || len(vm.Top) == homeTopGroups {
			break
		}
		vm.Top = append(vm.Top, groupRowVM{
			Rank: len(vm.Top) + 1, Name: g.Name, NZBs: g.NZBs,
			URL: "/search?group=" + url.QueryEscape(g.Name),
		})
	}
	w.cacheSet(ctx, homeGroupsKey, vm, time.Minute)
	return vm, true
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
