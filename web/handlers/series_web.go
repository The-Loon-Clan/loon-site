package handlers

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The series pages: releases grouped by the show they belong to.
//
// A release title says which episode of which show it is — two thirds of a real
// index's do — and /browse lists releases in the order they were posted, which
// is the right page for "what is new" and the wrong one for "what do you have
// of this show". Here, every copy of S01E01 sits together: the 720p, the
// 1080p, the WEB-DL, one row each under one heading.
//
// The filter is a URL, not a control: a season is ?s=, an episode is ?e=, and
// removing one is removing it from the address. That is what makes every state
// of this page linkable and the back button work, and it is why the index read
// takes -1 for "every" rather than 0 — season 0 is specials and episode 0 is a
// whole-season pack, both real.
//
// Every row also says whether the tracker carries the same release, through
// releaseMirrors (mirrors_web.go). That is the mixing: one page, both ways of
// getting the thing, rather than an index page and a tracker page that never
// mention each other.

const (
	// seriesPageSize is how many shows the index lists at once.
	seriesPageSize = 60
	// seriesReleaseLimit bounds one show's page. A show with 5,000 releases is
	// a page nobody scrolls, and the season and episode filters are how a
	// reader narrows — not the scrollbar.
	seriesReleaseLimit = 300
)

// episodeGroup is one episode with every release of it.
type episodeGroup struct {
	Season  int
	Episode int
	Pack    bool
	// Label is what the group heading says: "S03E07", or "Season 3 · complete"
	// for a pack, which is a different thing from an episode.
	Label    string
	Releases []seriesRowVM
	// Href filters the page to this episode. Empty when the page is already
	// filtered to it, so the heading stops being a link to where you are.
	Href string
}

// seriesRowVM is one release under an episode heading.
type seriesRowVM struct {
	ID    int64
	Title string
	Size  string
	// Posted is a formatted date, or an em dash when the index has no posting
	// time for the release — a value rather than a blank cell, so "unknown"
	// cannot be mistaken for a rendering failure.
	Posted string
	Group  string
	Tags   []string
	// OnTracker marks a release the tracker also carries — the whole of
	// "mixing both": the same content, both ways of getting it, one row.
	//
	// Guarded on this rather than on the counts, for the reason the listing
	// rows already are: 0 seeders is a real figure meaning a dead torrent, and
	// must not read the same as a release that was never mirrored.
	// Media is the measured one-liner — "HEVC at 10.4 Mb/s · E-AC-3 JOC 6
	// channels" — from somebody who downloaded this copy, as opposed to Tags
	// above, which is what the poster named it. Empty when nobody has reported
	// on this release, which is most of them: the row then reads exactly as it
	// did before. See seriesmedia_web.go.
	Media string
	OnTracker bool
	// TorrentHref is where the TRACKER says its page for this is. Empty when
	// it publishes none, and the row then states the mirror without linking.
	TorrentHref string
	Seeders     int
	Leechers    int
}

// seriesIndexPage lists shows — /series.
func (w *web) seriesIndexPage(c *gin.Context) {
	if w.series == nil {
		w.seriesUnavailable(c)
		return
	}
	ctx := c.Request.Context()
	q := strings.TrimSpace(c.Query("q"))
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 {
		page = 1
	}
	data := map[string]any{"Title": "Series", "Query": q, "Series": []pluginapi.SeriesRow{}}
	rows, total, err := w.series.Series(ctx, q, seriesPageSize, (page-1)*seriesPageSize)
	if err != nil {
		w.log.Error("series index", "err", err)
	} else {
		data["Series"] = rows
		data["Total"] = total
		data["PaginationHTML"] = w.renderPagination(hostPagination(
			page, seriesPageSize, total, seriesIndexHref(q)))
	}
	w.render(c, "series_index.html", data)
}

// seriesIndexHref is the index's own URL with the search kept, so paging never
// drops the query you typed. hostPagination appends the separator and page=N.
func seriesIndexHref(q string) string {
	if q == "" {
		return "/series"
	}
	return "/series?q=" + url.QueryEscape(q)
}

// seriesPage is one show — /series/:key. Its seasons, and its releases grouped
// by episode.
func (w *web) seriesPage(c *gin.Context) {
	if w.series == nil {
		w.seriesUnavailable(c)
		return
	}
	ctx := c.Request.Context()
	key := strings.ToLower(strings.TrimSpace(c.Param("key")))
	name, ok, err := w.series.SeriesByKey(ctx, key)
	if err != nil {
		w.log.Error("series lookup", "key", key, "err", err)
	}
	if err != nil || !ok {
		// A key nobody has released under is "no such show". It arrives from a
		// URL, so a typo has to render a page rather than a 500.
		c.Status(http.StatusNotFound)
		w.render(c, "series.html", map[string]any{"Title": "Not found", "Missing": true})
		return
	}

	// -1 is "every", which is what an absent parameter means. Unreadable input
	// widens rather than narrowing: ?s=banana showing specials would look like
	// a broken page, where showing everything looks like no filter.
	season := seriesFilterParam(c, "s")
	episode := seriesFilterParam(c, "e")
	// An episode without a season is not a filter this index can answer — the
	// read narrows within a season — so it is dropped rather than half-applied.
	if season < 0 {
		episode = -1
	}

	seasons, err := w.series.Seasons(ctx, key)
	if err != nil {
		w.log.Error("series seasons", "key", key, "err", err)
	}
	rels, err := w.series.Releases(ctx, key, season, episode, seriesReleaseLimit)
	if err != nil {
		w.log.Error("series releases", "key", key, "err", err)
	}

	data := map[string]any{
		"Title":      name,
		"SeriesKey":  key,
		"SeriesName": name,
		"Seasons":    seasonChips(key, seasons, season),
		// The tracker's side of the mix, resolved once for the page rather than
		// once per row — and passed IN, so the grouping stays a pure function.
		"Groups": groupEpisodes(key, rels, episode,
			w.releaseMirrors(ctx, releaseIDsIn(rels)),
			w.releaseSummaries(ctx, releaseIDsIn(rels))),
		"Season":  season,
		"Episode": episode,
		// Filtered drives the "clear" control, so it means "something is
		// narrowing this page", not "a parameter was present".
		"Filtered":  season >= 0,
		"ClearHref": "/series/" + key,
		"Releases":  len(rels),
		// Total is how many releases the CURRENT scope holds, which is not
		// len(rels) once the page hits its bound — a show with 518 releases
		// showing its newest 300 must not describe itself as having 300.
		"Total": scopeTotal(seasons, season),
		// AtCap is honest about that bound: a reader who scrolls to the end of
		// a capped list and is told nothing takes it for the whole show.
		"AtCap": len(rels) >= seriesReleaseLimit,
	}
	if episode >= 0 {
		data["EpisodeLabel"] = episodeLabel(season, episode, false)
		// Up one level: the season, keeping the show. The chip strip can widen
		// all the way out, and this is the step in between.
		data["SeasonHref"] = "/series/" + key + "?s=" + strconv.Itoa(season)
	}
	w.render(c, "series.html", data)
}

// seriesUnavailable answers a host whose indexer publishes no series index.
//
// A 404 rather than an empty page: nothing on this site links here when the
// capability is absent, so arriving is a typed or stale URL, and the status has
// to say so for the crawlers and caches that will also arrive.
func (w *web) seriesUnavailable(c *gin.Context) {
	c.Status(http.StatusNotFound)
	w.render(c, "series.html", map[string]any{"Title": "Not found", "Missing": true})
}

// seriesFilterParam reads a filter parameter. Absent or unreadable is -1,
// "every".
func seriesFilterParam(c *gin.Context, name string) int {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return -1
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return -1
	}
	return n
}

// scopeTotal is how many releases the picked scope holds, from the season
// counts the index already returned — no second query for a number that is
// only ever a sentence.
//
// season < 0 is the whole show. An episode filter has no entry of its own, and
// needs none: the read's bound is 300 and no episode has 300 copies, so the
// page's own row count is the total there.
func scopeTotal(seasons []pluginapi.SeriesSeason, season int) int {
	total := 0
	for _, s := range seasons {
		if season < 0 || s.Season == season {
			total += s.Releases
		}
	}
	return total
}

// seasonChipVM is one season chip, with the counts a reader picks on.
type seasonChipVM struct {
	Label    string
	Href     string
	Releases int
	Episodes int
	Active   bool
}

// seasonChips builds the strip, with "All seasons" first as the way back out.
func seasonChips(key string, seasons []pluginapi.SeriesSeason, picked int) []seasonChipVM {
	out := make([]seasonChipVM, 0, len(seasons)+1)
	var releases, episodes int
	for _, s := range seasons {
		releases += s.Releases
		episodes += s.Episodes
	}
	out = append(out, seasonChipVM{
		Label: "All seasons", Href: "/series/" + key,
		Releases: releases, Episodes: episodes, Active: picked < 0,
	})
	for _, s := range seasons {
		// Season 0 keeps its number rather than being renamed "Specials": it is
		// what the release titles say, and a chip whose label cannot be found in
		// any title is a chip a reader cannot map back to anything.
		out = append(out, seasonChipVM{
			Label:    "Season " + strconv.Itoa(s.Season),
			Href:     "/series/" + key + "?s=" + strconv.Itoa(s.Season),
			Releases: s.Releases, Episodes: s.Episodes,
			Active: picked == s.Season,
		})
	}
	return out
}

// groupEpisodes turns a flat release list into one group per episode.
//
// Newest season and episode first, which is what a reader following a running
// show wants; within an episode the releases keep the index's order, newest
// posting first.
func groupEpisodes(key string, rels []pluginapi.Release, episode int, mirrors map[int64]pluginapi.TorrentMirror, media map[int64]string) []episodeGroup {
	if len(rels) == 0 {
		return nil
	}
	type slot struct {
		season, episode int
		pack            bool
	}
	idx := map[slot]int{}
	var groups []episodeGroup
	for _, r := range rels {
		at := slot{r.Season, r.Episode, r.Pack}
		i, seen := idx[at]
		if !seen {
			g := episodeGroup{
				Season: r.Season, Episode: r.Episode, Pack: r.Pack,
				Label: episodeLabel(r.Season, r.Episode, r.Pack),
			}
			// The heading links to this episode alone — unless the page is
			// already filtered to it, when a link to where you are is noise.
			// Packs have no episode to filter to.
			if episode < 0 && !r.Pack {
				g.Href = "/series/" + key +
					"?s=" + strconv.Itoa(r.Season) + "&e=" + strconv.Itoa(r.Episode)
			}
			groups = append(groups, g)
			i = len(groups) - 1
			idx[at] = i
		}
		row := seriesRowVM{
			ID: r.ID, Title: r.Title, Size: humanBytes(r.Size),
			Group: r.Group, Posted: "—",
		}
		if !r.Posted.IsZero() {
			row.Posted = r.Posted.Format("2006-01-02")
		}
		for _, t := range []string{r.Resolution, r.Source, r.Codec, r.Audio, r.Language} {
			if t != "" {
				row.Tags = append(row.Tags, t)
			}
		}
		if m, ok := mirrors[r.ID]; ok {
			row.OnTracker, row.TorrentHref = true, m.Href
			row.Seeders, row.Leechers = m.Seeders, m.Leechers
		}
		row.Media = media[r.ID]
		groups[i].Releases = append(groups[i].Releases, row)
	}
	// Newest season first; within a season, newest episode first, with the
	// whole-season packs last — they are the boxed set, and a reader after one
	// episode should not have to scroll past them.
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].Season != groups[j].Season {
			return groups[i].Season > groups[j].Season
		}
		if groups[i].Pack != groups[j].Pack {
			return !groups[i].Pack
		}
		return groups[i].Episode > groups[j].Episode
	})
	return groups
}

// releaseIDsIn is the id slice releaseMirrors takes, off a page of releases.
func releaseIDsIn(rels []pluginapi.Release) []int64 {
	ids := make([]int64, 0, len(rels))
	for _, r := range rels {
		ids = append(ids, r.ID)
	}
	return ids
}

// episodeLabel names a group the way the genre does.
func episodeLabel(season, episode int, pack bool) string {
	if pack {
		return "Season " + strconv.Itoa(season) + " · complete"
	}
	return "S" + pad2(season) + "E" + pad2(episode)
}

// pad2 is the two-digit form every scene title uses: S03E07, not S3E7.
func pad2(n int) string {
	if n >= 0 && n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}
