package handlers

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
	"github.com/the-loon-clan/loon/core"
)

// Public usenet surface only: NZB download + the search/browse view models.
// The plugin's ADMIN pages (setup wizard, crawler status) are plugin-owned
// views mounted generically in main.go — see admin_views.go.

// newznabAPI is the Newznab/Torznab endpoint (/api + /rss). The plugin owns the
// XML; the host parses the query + serves the response. Authenticated and
// METERED (apiquota_web.go): ?apikey= resolves to the member it belongs to,
// a members-only site requires it, and the daily quota the operator sets on
// the Search API service refuses past its line.
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
	in, _ := readNewznabQueryInput(c)
	in = in.clamp()
	// Before the cache: a cached answer still counts against the quota, or
	// the cheapest requests would be the unmetered ones.
	if !w.apiGate(c, in.APIKey) {
		return
	}
	req := pluginapi.NewznabRequest{
		Function:   in.Function,
		Query:      in.Query,
		Categories: w.expandCatList(c.Request.Context(), parseCats(in.Cats)),
		Limit:      in.Limit,
		Offset:     in.Offset,
		ID:         in.ID,
		BaseURL:    requestBaseURL(c),
		Title:      "loon indexer",
		APIKey:     in.APIKey,
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
		//nolint:errcheck // a cache write that fails is a cache miss next time, nothing more
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

// expandCatList expands a Newznab cat= list through the taxonomy so a bare
// PARENT id (5000, 2000, …) becomes its subcategory ids.
//
// Sonarr/Radarr and every Newznab client routinely query a top-level category:
// cat=5000 means "all TV". But the indexer stores only SUBcategory ids
// (categorize never emits a bare parent) and matches category_id with an exact
// IN(…). So a parent alone matched nothing and the feed came back empty -- the
// automation read "no releases" for the whole of television. Reuses the browse
// path's expandCats per id and dedups; a non-parent id expands to itself, so a
// plain cat=5040 query is unchanged. No catalog wired -> pass-through, never an
// error on the query path.
func (w *web) expandCatList(ctx context.Context, cats []int) []int {
	if len(cats) == 0 || w.catalog == nil {
		return cats
	}
	seen := map[int]bool{}
	var out []int
	for _, c := range cats {
		for _, id := range w.expandCats(ctx, c) {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
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
	data := map[string]any{"Title": d.Title, "Release": vm}
	// What the NZB's file list PROVES (fileinfo_web.go). Derived, so it is
	// always true and always available — as opposed to the mediainfo panel
	// below it, which is somebody's word. The two are drawn separately so a
	// reader can tell which is which.
	if contents := describeFiles(d.Files); contents.Meaningful() {
		data["Contents"] = contents
	}
	if w.catalogCovers != nil {
		if url, has, _ := w.catalogCovers.ReleaseCover(c.Request.Context(), id); has {
			vm.Cover = url
			data["Release"] = vm
			// The wide art a source stored beside the poster. Only the
			// background is used: it is the one shape this page has room for,
			// behind the header. The banner is carried for a listing row,
			// which is a separate change.
			if _, bg := w.releaseArt(c.Request.Context(), url); bg != "" {
				data["Backdrop"] = bg
			}
			// Links out to the databases this release was matched against.
			if links := w.releaseExternals(c.Request.Context(), url); len(links) > 0 {
				data["ExternalLinks"] = links
			}
		}
	}
	// The swarm, when the site HAS a tracker and this release also exists as a
	// torrent on it. Absent for a release with none — which is nearly all of
	// them — so the page shows a swarm or says nothing, never "0 seeders",
	// which reads as a dead torrent rather than no torrent.
	//
	// The flavour check is not redundant with the read. This reads the
	// tracker's own table, which SURVIVES the tracker being switched off, so a
	// site that ran as "both" and moved to indexer-only kept drawing a swarm
	// panel whose "Download torrent" button pointed at a route that no longer
	// exists. Found by running the link audit against a flavour nobody had
	// audited — the same fault, in a third place.
	if sw, okSwarm := w.data.ReadTrackerSwarm(c.Request.Context(), id); okSwarm && flavourTracker() {
		data["Swarm"] = sw
	}
	// Tell widgets WHAT this page is about before rendering their region, so a
	// release widget can read the id instead of parsing the URL. Kind as well
	// as id: an id alone is how a release widget renders against a thread id.
	core.SetWidgetItem(c, "release", id)
	// TWO regions, because they are different shapes of space. The aside is a
	// narrow column beside the details, which suits a swarm figure and ruins a
	// conversation; the main one is full width under the files, which is where
	// anything with paragraphs in it belongs.
	//
	// Both carry the same subject — a widget in either can ask what release
	// this is — so the split is about layout and nothing else.
	regions := w.renderRegions(c, "release", "release-main")
	if ws := regions["release"]; len(ws) > 0 {
		data["RegionWidgets"] = ws
	}
	if ws := regions["release-main"]; len(ws) > 0 {
		data["MainWidgets"] = ws
	}
	// Bookmarked is set ONLY for a signed-in viewer, so the button is absent
	// rather than rendered in a false "not saved" state for anonymous readers.
	if u, okUser := w.currentUser(c); okUser && u != nil {
		data["CanBookmark"] = true
		data["Bookmarked"] = w.data.IsBookmarked(c.Request.Context(), u.ID, id)
		// Offer the torrent only to a member who could actually use it: the
		// tracker's own pages need an account and an entitlement, so the button
		// on an anonymous page would be a control that leads to a login wall.
		// Absent when this release already has one — the swarm panel above
		// links to it, and two ways to reach one torrent is one too many.
		if _, mirrored := data["Swarm"]; !mirrored && w.mirrorMaker != nil {
			data["CanMirror"] = true
		}
	}
	w.render(c, "release.html", data)
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
	// The show this release is an episode of, when the title said so — the
	// other direction of /series. Empty for the two thirds of an index that
	// says nothing, and the page then shows no series link rather than one
	// leading to a page with a single row on it.
	SeriesKey  string
	SeriesName string
	// EpisodeLabel is "S03E07", or "Season 3 · complete" for a pack.
	EpisodeLabel string
	// EpisodeHref opens /series filtered to this exact episode — "every other
	// copy of this", which is the question a reader on a release page has.
	EpisodeHref string
}

func toReleaseVM(d pluginapi.ReleaseDetail) releaseVM {
	vm := releaseVM{
		ID: d.ID, Title: d.Title, Size: humanBytes(d.Size),
		Group: d.Group, Poster: d.Poster, Category: d.Category, Posted: "—",
	}
	if !d.Posted.IsZero() {
		vm.Posted = d.Posted.Format("2006-01-02 15:04")
	}
	if d.SeriesKey != "" {
		vm.SeriesKey, vm.SeriesName = d.SeriesKey, d.SeriesName
		vm.EpisodeLabel = episodeLabel(d.Season, d.Episode, d.Pack)
		// A pack is a whole season, so it filters to the season rather than to
		// an episode number it does not have.
		vm.EpisodeHref = "/series/" + d.SeriesKey + "?s=" + strconv.Itoa(d.Season)
		if !d.Pack {
			vm.EpisodeHref += "&e=" + strconv.Itoa(d.Episode)
		}
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
	// Count the grab BEFORE writing the body, while the request context is
	// still live — after c.Data the client may already be gone. Best-effort:
	// storage.RecordGrab swallows its own errors, because a download that worked must
	// not fail on a counter that did not.
	var uid int64
	if u, ok := w.currentUser(c); ok && u != nil {
		uid = u.ID
	}
	w.data.RecordGrab(c.Request.Context(), id, uid)

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
	// Selectable and InCart are the cart's tick box (cart_web.go).
	//
	// ON THE ROW rather than on the page data, and that is not a preference:
	// release-row is called as {{template "release-row" .}} with a searchRow as
	// the dot, so inside it `$` IS the row — the page data is unreachable from
	// there. Threading it in would mean calling the row with a dict at six call
	// sites and rewriting every field reference in its body.
	//
	// Selectable is off for anonymous viewers and for the home page, so a tick
	// box only ever appears where it does something.
	Selectable bool
	// InCart draws the tick already ticked and disabled. Without it a member
	// re-ticks what they already have, presses add, and is told nothing was
	// added — which reads as a broken button rather than as work already done.
	InCart bool
	// Grabs is how many times the NZB has been downloaded. ZERO means "not
	// measured on this path", not "nobody grabbed it" — only the pages that
	// call attachGrabs populate it, and the templates guard on it so an
	// unmeasured row renders no figure rather than a false 0.
	Grabs int
	// The tracker swarm, for rows on a listing that also exist as torrents.
	// HasSwarm is what the template guards on, for the same reason Grabs needs
	// care and more so: 0 seeders is a REAL and meaningful figure (a dead
	// torrent), so it cannot double as "this release has no torrent". Only the
	// pages that call attachSwarm populate any of this.
	HasSwarm bool
	Seeders  int
	Leechers int
}

// attachGrabs fills Grabs for a page of rows in ONE query. Rows with no
// recorded grab keep 0 and their templates render nothing, which is the honest
// state: the table stores downloads, not zeroes.
func (w *web) attachGrabs(ctx context.Context, rows []searchRow) []searchRow {
	if len(rows) == 0 {
		return rows
	}
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	counts := w.data.GrabCounts(ctx, ids)
	if counts == nil {
		return rows
	}
	for i := range rows {
		rows[i].Grabs = counts[rows[i].ID]
	}
	return rows
}

// attachSwarm fills in the tracker figures for a listing, in one query.
//
// Paired with attachGrabs at every call site rather than folded into it: they
// answer different questions from different plugins, and a host running the
// indexer without the tracker should keep grabs and get nothing here. Returns
// rows untouched when the tracker is off, so callers need no gate of their own.
//
// The lookup itself is releaseMirrors (mirrors_web.go) — the ONE place this
// site asks which releases are also torrents, shared with the series pages so
// the two cannot answer differently.
func (w *web) attachSwarm(ctx context.Context, rows []searchRow) []searchRow {
	if len(rows) == 0 {
		return rows
	}
	mirrors := w.releaseMirrors(ctx, releaseIDsOf(rows))
	if len(mirrors) == 0 {
		return rows
	}
	for i := range rows {
		if m, ok := mirrors[rows[i].ID]; ok {
			rows[i].HasSwarm = true
			rows[i].Seeders = m.Seeders
			rows[i].Leechers = m.Leechers
		}
	}
	return rows
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
	// homeFeatured — posters in the featured strip. Six filled about half the
	// track on a wide screen and left the carousel's own arrows with nothing to
	// do, which reads as a broken control rather than a short list.
	//
	// 18 is three screenfuls at six visible. NOT the whole window: 53 of the 60
	// rows have cover art (measured 23 Aug 2026), so the strip could run to 53
	// without a gradient tile, but at that point it is a second browse page
	// stacked on top of the 25-row table directly below it. 18 leaves enough
	// headroom that a drop in cover coverage still never shows a fallback here.
	//
	// Costs nothing extra to fetch: the strip slices the SAME 60-row window the
	// rest of the page already queried, and the posters are loading="lazy" with
	// explicit width/height, so the ones off the right edge are never requested.
	homeFeatured  = 18
	homeTopGroups = 5 // rows in the busiest-groups panel

	// v2: the cached value changed shape when the home page became
	// movies-and-TV only. Reusing v1 would have served the old unfiltered rows
	// for the whole cache TTL after deploy, which reads as "the change did
	// nothing" — a failure this project has already spent rounds on.
	homeRowsKey   = "home:rows:v2"   // []searchRow, cover-decorated
	homeGroupsKey = "home:groups:v1" // homeGroupsVM
)

// homeCategories restricts the home page to films and television.
//
// The front page was showing whatever was newest, and what is newest on Usenet
// is not what a visitor came to see: of the 22 most recent releases, not one
// was a film or an episode. They were cracked installers posted as "Other"
// (8010) and "PC/Games" (4050) — Adobe, Foxit and Topaz builds advertising
// themselves as pre-activated, which is the shape of a malware drop whatever
// is actually in the archive — plus a run of German audiobooks under 3010.
//
// This is presentation, not indexing. Every category is still crawled, still
// searchable, still in the API and still reachable from /browse; the home page
// simply leads with the two categories the site is about. A demo whose shop
// window is 3,120 "Misc" and 14,085 audiobooks misrepresents what is in it:
// films and TV are 86% of the catalogue.
//
// The full Newznab movie and TV subtrees are listed rather than the ids that
// happen to be populated today, so a category that starts arriving later shows
// up without a code change. Ids come from catalog's taxonomy (the Newznab
// standard tree) — 2060 is 3D, 5070 anime, 5080 documentary.
var homeCategories = []int{
	2000, 2010, 2030, 2040, 2045, 2050, 2060,
	5000, 5020, 5030, 5040, 5045, 5060, 5070, 5080,
}

// homeReleases returns the recent-release rows for the home page, already
// cover-decorated, plus whether they came from the page cache (the X-Cache
// header). The DECORATED rows are what's cached, so a hit costs neither the
// release query nor the cover lookup. A read error degrades to no rows.
func (w *web) homeReleases(ctx context.Context) ([]searchRow, bool) {
	var rows []searchRow
	if w.cacheGet(ctx, homeRowsKey, &rows) {
		return rows, true
	}
	// Feed rather than Browse: Browse takes a newsgroup and no categories, so
	// filtering its result in Go would mean fetching 60 rows and throwing most
	// of them away — with the junk being the NEWEST rows, a window of 60 could
	// come back with nothing to show at all. Feed pushes the category list into
	// the query's IN(), so the window is 60 films and episodes.
	res, _, err := w.usenet.Feed(ctx, homeCategories, homeReleaseWindow, 0)
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
