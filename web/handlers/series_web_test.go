package handlers

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The series page's two decisions are what its URL means and how its rows are
// grouped. Both are pure functions, and both are wrong in ways a rendered page
// hides: a mis-parsed filter still draws a table, and a mis-ordered group still
// draws rows.

// -1 is "every", and it has to be reachable from an ABSENT parameter and from a
// malformed one alike. 0 cannot stand in for it: season 0 is specials and
// episode 0 is a whole-season pack, both real values a reader can filter to.
func TestSeriesFilterParam(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"", -1},          // no filter
		{"?s=", -1},       // present but empty — a cleared form field
		{"?s=0", 0},       // specials, a real season
		{"?s=3", 3},       // the ordinary case
		{"?s=banana", -1}, // unreadable widens rather than narrowing
		{"?s=-1", -1},     // a negative is not a season
		{"?s=3.5", -1},    // nor is a fraction
		{"?t=3", -1},      // a different parameter entirely
	} {
		t.Run(tc.query, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("GET", "/series/x"+tc.query, nil)
			if got := seriesFilterParam(c, "s"); got != tc.want {
				t.Errorf("seriesFilterParam = %d, want %d", got, tc.want)
			}
		})
	}
}

// The chip strip always leads with the way OUT. A filtered page whose only
// controls narrow it further is a page a reader gets stuck on.
func TestSeasonChipsLeadWithTheWayOut(t *testing.T) {
	chips := seasonChips("someshow", []pluginapi.SeriesSeason{
		{Season: 1, Releases: 40, Episodes: 10},
		{Season: 2, Releases: 36, Episodes: 9},
	}, 2)
	if len(chips) != 3 {
		t.Fatalf("got %d chips, want 3 (two seasons plus the clear)", len(chips))
	}
	if chips[0].Href != "/series/someshow" {
		t.Errorf("first chip href = %q, want the unfiltered page", chips[0].Href)
	}
	// Its count is the whole show, not a season's — the chip claims to show
	// everything and the number beside it has to agree.
	if chips[0].Releases != 76 {
		t.Errorf("clear chip = %d releases, want 76 (the total)", chips[0].Releases)
	}
	if chips[0].Active {
		t.Error("the clear chip is active on a filtered page — it is the way out, not where you are")
	}
	if !chips[2].Active || chips[2].Label != "Season 2" {
		t.Errorf("chip 2 = %+v, want the picked season marked active", chips[2])
	}
	if chips[1].Href != "/series/someshow?s=1" {
		t.Errorf("season chip href = %q, want ?s=1", chips[1].Href)
	}
}

// Season 0 is a real season — specials — so it gets a chip like any other and
// is NOT confused with "no season picked".
func TestSeasonZeroIsASeason(t *testing.T) {
	chips := seasonChips("x", []pluginapi.SeriesSeason{{Season: 0, Releases: 4, Episodes: 4}}, 0)
	if len(chips) != 2 {
		t.Fatalf("got %d chips, want 2", len(chips))
	}
	if chips[0].Active {
		t.Error("?s=0 marked the clear chip active — season 0 is a filter, not the absence of one")
	}
	if !chips[1].Active {
		t.Error("season 0's own chip is not active while the page is filtered to it")
	}
}

func TestEpisodeLabel(t *testing.T) {
	for _, tc := range []struct {
		season, episode int
		pack            bool
		want            string
	}{
		{3, 7, false, "S03E07"},
		{1, 1, false, "S01E01"},
		{10, 122, false, "S10E122"},
		// A pack is a whole season, and saying "S03E00" would claim an episode
		// zero that does not exist.
		{3, 0, true, "Season 3 · complete"},
	} {
		if got := episodeLabel(tc.season, tc.episode, tc.pack); got != tc.want {
			t.Errorf("episodeLabel(%d,%d,%v) = %q, want %q", tc.season, tc.episode, tc.pack, got, tc.want)
		}
	}
}

// The grouping is the whole feature: every copy of one episode under one
// heading, newest episode first, packs after the episodes of their season.
func TestGroupEpisodes(t *testing.T) {
	rels := []pluginapi.Release{
		{ID: 1, Title: "Show.S02E04.1080p", Season: 2, Episode: 4},
		{ID: 2, Title: "Show.S02.COMPLETE", Season: 2, Pack: true},
		{ID: 3, Title: "Show.S02E04.720p", Season: 2, Episode: 4},
		{ID: 4, Title: "Show.S03E01.1080p", Season: 3, Episode: 1},
		{ID: 5, Title: "Show.S02E05.1080p", Season: 2, Episode: 5},
	}
	got := groupEpisodes("show", rels, -1, nil)

	want := []string{"S03E01", "S02E05", "S02E04", "Season 2 · complete"}
	if len(got) != len(want) {
		t.Fatalf("got %d groups, want %d", len(got), len(want))
	}
	for i, label := range want {
		if got[i].Label != label {
			t.Errorf("group %d = %q, want %q", i, got[i].Label, label)
		}
	}
	// The two copies of S02E04 are ONE group — that is the point of the page.
	if n := len(got[2].Releases); n != 2 {
		t.Errorf("S02E04 has %d releases, want both copies of it", n)
	}
	// Order within a group is the index's, newest posting first.
	if got[2].Releases[0].ID != 1 {
		t.Errorf("S02E04 leads with release %d, want the order the index returned", got[2].Releases[0].ID)
	}
	// Every episode heading is a link to that episode alone.
	if got[0].Href != "/series/show?s=3&e=1" {
		t.Errorf("episode href = %q, want the season+episode filter", got[0].Href)
	}
	// Except the pack, which has no episode to filter to.
	if got[3].Href != "" {
		t.Errorf("a season pack offered an episode filter (%q); it has no episode", got[3].Href)
	}
}

// On a page already filtered to one episode, the heading stops being a link:
// a link to where you already are teaches a reader that the links do nothing.
func TestGroupEpisodesDropsTheLinkWhenAlreadyFiltered(t *testing.T) {
	got := groupEpisodes("show", []pluginapi.Release{
		{ID: 1, Season: 2, Episode: 4},
	}, 4, nil)
	if len(got) != 1 {
		t.Fatalf("got %d groups, want 1", len(got))
	}
	if got[0].Href != "" {
		t.Errorf("href = %q on a page already filtered to this episode", got[0].Href)
	}
}

// A mirrored release carries the tracker's OWN link, and an unmirrored one
// carries no swarm at all — never a zero, which would read as a dead torrent
// rather than as no torrent.
func TestGroupEpisodesMarksTrackerMirrors(t *testing.T) {
	got := groupEpisodes("show", []pluginapi.Release{
		{ID: 1, Season: 1, Episode: 1, Posted: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)},
		{ID: 2, Season: 1, Episode: 1},
	}, -1, map[int64]pluginapi.TorrentMirror{
		1: {InfoHash: "abc", Href: "/tracker/t/abc", Seeders: 12, Leechers: 3},
	})
	rows := got[0].Releases
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if !rows[0].OnTracker || rows[0].TorrentHref != "/tracker/t/abc" || rows[0].Seeders != 12 {
		t.Errorf("mirrored row = %+v, want the tracker's link and swarm", rows[0])
	}
	if rows[1].OnTracker {
		t.Error("an unmirrored release was marked as being on the tracker")
	}
	// The posting date renders as a date, and its absence as an em dash rather
	// than an empty cell.
	if rows[0].Posted != "2026-08-01" {
		t.Errorf("posted = %q, want the formatted date", rows[0].Posted)
	}
	if rows[1].Posted != "—" {
		t.Errorf("unknown posting time rendered as %q, want an em dash", rows[1].Posted)
	}
}

// Paging must not drop the search. The pager appends "page=N" to whatever this
// returns, so the separator is its problem, not the caller's.
func TestSeriesIndexHrefKeepsTheQuery(t *testing.T) {
	if got := hostPagination(2, 60, 300, seriesIndexHref("")).BaseURL; got != "/series?" {
		t.Errorf("unfiltered base = %q, want /series?", got)
	}
	got := hostPagination(2, 60, 300, seriesIndexHref("the blacklist")).BaseURL
	if got != "/series?q=the+blacklist&" {
		t.Errorf("base = %q, want the escaped query kept and an & separator", got)
	}
}
