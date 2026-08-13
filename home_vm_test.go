package site

import (
	"context"
	"testing"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// batchCovers implements BOTH cover interfaces — the shape the catalog plugin
// actually publishes.
type batchCovers struct {
	urls  map[int64]string
	calls int // ReleaseCovers invocations
	ones  int // ReleaseCover invocations (must stay 0 on the batch path)
}

func (b *batchCovers) SetReleaseCover(context.Context, int64, string) error { return nil }

func (b *batchCovers) ReleaseCover(_ context.Context, id int64) (string, bool, error) {
	b.ones++
	u, ok := b.urls[id]
	return u, ok, nil
}

func (b *batchCovers) ReleaseCovers(_ context.Context, ids []int64) (map[int64]string, error) {
	b.calls++
	out := map[int64]string{}
	for _, id := range ids {
		if u, ok := b.urls[id]; ok {
			out[id] = u
		}
	}
	return out, nil
}

// singleCovers is a catalog build that predates CatalogCoverBatch — the
// fallback path the host must keep working.
type singleCovers struct {
	urls  map[int64]string
	calls int
}

func (s *singleCovers) SetReleaseCover(context.Context, int64, string) error { return nil }

func (s *singleCovers) ReleaseCover(_ context.Context, id int64) (string, bool, error) {
	s.calls++
	u, ok := s.urls[id]
	return u, ok, nil
}

func rows(ids ...int64) []searchRow {
	out := make([]searchRow, len(ids))
	for i, id := range ids {
		out[i] = searchRow{ID: id, Title: "release"}
	}
	return out
}

// The whole point of the batch capability: a page of posters is ONE round trip.
// A regression here is invisible in the rendered HTML and only shows up as N
// queries per page view, so assert the call count, not just the URLs.
func TestAttachCovers_BatchIsOneCall(t *testing.T) {
	cov := &batchCovers{urls: map[int64]string{1: "/a.jpg", 3: "/c.jpg"}}
	w := &web{catalogCovers: cov}

	rs := rows(1, 2, 3)
	w.attachCovers(context.Background(), rs)

	if cov.calls != 1 {
		t.Errorf("ReleaseCovers called %d times, want exactly 1", cov.calls)
	}
	if cov.ones != 0 {
		t.Errorf("per-id ReleaseCover called %d times — the batch path must not fall back", cov.ones)
	}
	if rs[0].Cover != "/a.jpg" || rs[2].Cover != "/c.jpg" {
		t.Errorf("covers not attached: %q %q", rs[0].Cover, rs[2].Cover)
	}
	// An id with no cover must be an empty string, not a missing/garbage URL:
	// the template keys its gradient fallback tile off exactly that.
	if rs[1].Cover != "" {
		t.Errorf("uncovered release got Cover=%q, want empty so the fallback tile renders", rs[1].Cover)
	}
}

// A catalog build without the optional batch interface must still produce the
// same rows — slower, never broken.
func TestAttachCovers_FallsBackPerID(t *testing.T) {
	cov := &singleCovers{urls: map[int64]string{2: "/b.jpg"}}
	w := &web{catalogCovers: cov}

	rs := rows(1, 2, 2) // duplicate id: the fallback dedups so callers needn't
	w.attachCovers(context.Background(), rs)

	if cov.calls != 2 {
		t.Errorf("ReleaseCover called %d times for 2 distinct ids, want 2", cov.calls)
	}
	if rs[1].Cover != "/b.jpg" || rs[2].Cover != "/b.jpg" {
		t.Errorf("fallback did not attach the cover: %q %q", rs[1].Cover, rs[2].Cover)
	}
	if rs[0].Cover != "" {
		t.Errorf("uncovered release got Cover=%q, want empty", rs[0].Cover)
	}
}

// No catalog plugin at all is a normal deployment, not an error.
func TestAttachCovers_NoCapability(t *testing.T) {
	w := &web{}
	rs := rows(1, 2)
	w.attachCovers(context.Background(), rs) // must not panic
	if rs[0].Cover != "" || rs[1].Cover != "" {
		t.Error("covers appeared without a catalog capability")
	}
}

// The featured strip exists to show artwork; a covered release must outrank a
// newer uncovered one, and the strip must still fill when nothing is covered.
func TestFeaturedRows(t *testing.T) {
	rs := []searchRow{
		{ID: 1}, {ID: 2, Cover: "/b.jpg"}, {ID: 3}, {ID: 4, Cover: "/d.jpg"},
	}
	got := featuredRows(rs, 3)
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3", len(got))
	}
	if got[0].ID != 2 || got[1].ID != 4 {
		t.Errorf("covered releases must come first, got ids %d,%d", got[0].ID, got[1].ID)
	}
	if got[2].ID != 1 {
		t.Errorf("top-up should take the newest uncovered release, got id %d", got[2].ID)
	}

	none := featuredRows([]searchRow{{ID: 7}, {ID: 8}}, 3)
	if len(none) != 2 {
		t.Errorf("with no covers at all the strip should still fill from what exists, got %d", len(none))
	}
}

// The home page is UNIT3D's foreach over $blocks: a FIXED order, filtered down
// to the blocks that resolved. Both halves matter — a block with no data must
// vanish (that is how a missing usenet/catalog/forum capability degrades), and
// the ones that remain must keep the declared order regardless of the order
// home() happened to fill them in.
func TestOrderedBlocks(t *testing.T) {
	got := orderedBlocks(map[string]any{
		blockTopPosters:     []forumPosterVM{{Rank: 1}},
		blockFeatured:       rows(1, 2),
		blockLatestReleases: rows(1),
	})
	var names []string
	for _, b := range got {
		names = append(names, b.Name)
	}
	want := []string{blockFeatured, blockLatestReleases, blockTopPosters}
	if len(names) != len(want) {
		t.Fatalf("blocks = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("blocks = %v, want %v (homeBlockOrder is the source of truth)", names, want)
		}
	}
	if _, ok := got[0].Data.([]searchRow); !ok {
		t.Errorf("featured carries %T, want the []searchRow the template ranges over", got[0].Data)
	}

	// No capability wired at all: an empty stack, never a nil-deref or a
	// block with nothing in it.
	if n := len(orderedBlocks(map[string]any{})); n != 0 {
		t.Errorf("an empty host produced %d blocks, want 0", n)
	}
	// A name nobody declared is ignored rather than appended blind — the
	// template has no arm for it and would render a hole.
	if n := len(orderedBlocks(map[string]any{"not_a_block": []int{1}})); n != 0 {
		t.Errorf("an unknown block name produced %d blocks, want 0", n)
	}
}

// Every declared block must be spelled the same way in the order list and in
// the constants the handler fills, or a block silently never renders.
func TestHomeBlockOrderIsComplete(t *testing.T) {
	want := map[string]bool{
		blockWidgets: true, blockFeatured: true, blockLatestReleases: true,
		blockNoReleases: true, blockPopular: true, blockTopGroups: true,
		blockLatestTopics: true, blockTopPosters: true,
	}
	seen := map[string]bool{}
	for _, name := range homeBlockOrder {
		if !want[name] {
			t.Errorf("homeBlockOrder names %q, which is not one of the block constants", name)
		}
		if seen[name] {
			t.Errorf("homeBlockOrder lists %q twice — it would render twice", name)
		}
		seen[name] = true
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("block %q has a constant but is not in homeBlockOrder, so it can never render", name)
		}
	}

	// The empty state is the releases panel, so it has to occupy the releases
	// SLOT. Anywhere else and a build with no indexer would show the setup
	// guidance under the forum panels instead of where the table belongs.
	at := func(name string) int {
		for i, n := range homeBlockOrder {
			if n == name {
				return i
			}
		}
		return -1
	}
	if at(blockNoReleases) != at(blockLatestReleases)+1 {
		t.Errorf("homeBlockOrder puts %q at %d and %q at %d — the empty state must sit directly in the releases slot",
			blockLatestReleases, at(blockLatestReleases), blockNoReleases, at(blockNoReleases))
	}
}

func TestCapRows(t *testing.T) {
	if n := len(capRows(rows(1, 2, 3), 2)); n != 2 {
		t.Errorf("capRows(3, 2) = %d rows, want 2", n)
	}
	if n := len(capRows(rows(1), 5)); n != 1 {
		t.Errorf("capRows must not pad a short list, got %d rows", n)
	}
}

// A release the crawler never learned a post date for must not print an age
// counted from the zero time.
func TestTimeAgo(t *testing.T) {
	if got := timeAgo(time.Time{}); got != "" {
		t.Errorf("timeAgo(zero) = %q, want empty", got)
	}
	for _, tc := range []struct {
		ago  time.Duration
		want string
	}{
		{10 * time.Second, "just now"},
		{-time.Minute, "just now"}, // clock skew
		{time.Hour, "1 hour ago"},
		{5 * time.Hour, "5 hours ago"},
		{50 * time.Hour, "2 days ago"},
		{20 * 24 * time.Hour, "2 weeks ago"},
	} {
		if got := timeAgo(time.Now().Add(-tc.ago)); got != tc.want {
			t.Errorf("timeAgo(-%s) = %q, want %q", tc.ago, got, tc.want)
		}
	}
}

func TestInitialsAndHue(t *testing.T) {
	if got := initials("[SubGrp] Some.Show.S01E02"); got != "SS" {
		t.Errorf("initials = %q, want \"SS\"", got)
	}
	if got := initials(""); got != "" {
		t.Errorf("initials(\"\") = %q, want empty", got)
	}
	// The fallback tile must not change colour between page loads.
	if hueBucket("a title") != hueBucket("a title") {
		t.Error("hueBucket is not deterministic")
	}
	// The palette is components.css's .poster--h0 … .poster--h7 and nothing
	// wider: a class with no matching rule is not a build or render error, it
	// just silently falls back to the default hue. So assert the exact upper
	// bound, over a spread of inputs rather than one that lands in one bucket.
	for _, s := range []string{"a", "b", "c", "Some.Show.S01E02", "", "zzz", "9", "x y z"} {
		if h := hueBucket(s); h < 0 || h > 7 {
			t.Errorf("hueBucket(%q) = %d, outside the 0-7 palette (.poster--h0..h7)", s, h)
		}
	}
}

func TestDict(t *testing.T) {
	m, err := dict("Row", 1, "Size", "lg")
	if err != nil {
		t.Fatalf("dict: %v", err)
	}
	if m["Row"] != 1 || m["Size"] != "lg" {
		t.Errorf("dict built %v", m)
	}
	if _, err := dict("odd"); err == nil {
		t.Error("an odd argument count must fail the render, not drop a value")
	}
	if _, err := dict(1, "x"); err == nil {
		t.Error("a non-string key must fail the render")
	}
}

func TestOrdinal(t *testing.T) {
	for in, want := range map[int]string{1: "1st", 2: "2nd", 3: "3rd", 4: "4th", 11: "11th", 21: "21st"} {
		if got := ordinal(in); got != want {
			t.Errorf("ordinal(%d) = %q, want %q", in, got, want)
		}
	}
}

// searchRow keeps the raw values the templates still need to decide on
// (relative time, byte count) alongside the preformatted ones.
func TestToSearchRows(t *testing.T) {
	posted := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	got := toSearchRows([]pluginapi.Release{{
		ID: 7, Title: "Some.Show.S01E01.1080p.WEB-DL", Size: 1536 * 1024 * 1024,
		Posted: posted, Group: "alt.binaries.test", Resolution: "1080p",
		Source: "WEB-DL", CategoryID: 5000, Category: "TV",
	}, {ID: 8, Title: "No date"}})

	r := got[0]
	if r.Size != "1.5 GB" || r.SizeBytes != 1536*1024*1024 {
		t.Errorf("size = %q / %d", r.Size, r.SizeBytes)
	}
	if r.Posted != "2026-08-01" || !r.PostedAt.Equal(posted) {
		t.Errorf("posted = %q / %s", r.Posted, r.PostedAt)
	}
	if r.CategoryID != 5000 || r.Group != "alt.binaries.test" || r.Resolution != "1080p" || r.Source != "WEB-DL" {
		t.Errorf("row lost a field: %+v", r)
	}
	if len(r.Tags) != 2 {
		t.Errorf("tags = %v, want the two non-empty quality fields", r.Tags)
	}
	// An unknown post date renders as the em dash, and PostedAt stays zero so
	// {{timeAgo}} prints nothing rather than an age since year 1.
	if got[1].Posted != "—" || !got[1].PostedAt.IsZero() {
		t.Errorf("undated release = %q / %s", got[1].Posted, got[1].PostedAt)
	}
}
