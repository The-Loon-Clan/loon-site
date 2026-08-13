package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// These run with no database and no HTTP request.
//
// That is the point of the two narrow interfaces bookmarkedRows takes. The
// handler layer sits at 16% coverage not because the logic is untestable but
// because it was reachable only through a *storage.Store, and a test that needs
// Postgres to assert "a deleted release is skipped" does not get written.

// fakeBookmarks is the saved-id side.
type fakeBookmarks struct {
	ids       []int64
	gotUser   int64
	gotLimit  int
	callCount int
}

func (f *fakeBookmarks) BookmarkedIDs(_ context.Context, userID int64, limit int) []int64 {
	f.callCount++
	f.gotUser, f.gotLimit = userID, limit
	return f.ids
}

// fakeIndex is the release-lookup side: pluginapi.UsenetIndex, of which only
// ReleaseByID is exercised here. The rest is embedded so this keeps compiling
// when the plugin interface grows a method — a fake that has to be updated for
// an unrelated change is a fake people delete.
type fakeIndex struct {
	pluginapi.UsenetIndex
	byID map[int64]pluginapi.ReleaseDetail
	errs map[int64]error
}

func (f fakeIndex) ReleaseByID(_ context.Context, id int64) (pluginapi.ReleaseDetail, bool, error) {
	if err, ok := f.errs[id]; ok {
		return pluginapi.ReleaseDetail{}, false, err
	}
	d, ok := f.byID[id]
	return d, ok, nil
}

func release(id int64, title string) pluginapi.ReleaseDetail {
	return pluginapi.ReleaseDetail{Release: pluginapi.Release{ID: id, Title: title}}
}

func TestBookmarkedRowsResolvesInSavedOrder(t *testing.T) {
	// The order storage returns is the order rendered: it is "most recently
	// saved first", and re-sorting here would quietly discard that.
	list := &fakeBookmarks{ids: []int64{3, 1, 2}}
	index := fakeIndex{byID: map[int64]pluginapi.ReleaseDetail{
		1: release(1, "first"), 2: release(2, "second"), 3: release(3, "third"),
	}}

	rows := bookmarkedRows(context.Background(), list, index, 7, 50)

	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	for i, want := range []string{"third", "first", "second"} {
		if rows[i].Title != want {
			t.Errorf("row %d = %q, want %q", i, rows[i].Title, want)
		}
	}
	if list.gotUser != 7 || list.gotLimit != 50 {
		t.Errorf("storage asked for user %d limit %d, want 7 and 50", list.gotUser, list.gotLimit)
	}
}

func TestABookmarkOutlivingItsReleaseIsSkipped(t *testing.T) {
	// The behaviour this function was extracted for. Usenet retention removes
	// the post; the saved pointer stays. The page must render the ones that are
	// still there rather than erroring or showing a blank row — a member with
	// one dead bookmark still has a bookmarks page.
	list := &fakeBookmarks{ids: []int64{1, 999, 2}}
	index := fakeIndex{byID: map[int64]pluginapi.ReleaseDetail{
		1: release(1, "still here"), 2: release(2, "also here"),
	}}

	rows := bookmarkedRows(context.Background(), list, index, 1, 50)

	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 — the missing release should be skipped, not rendered", len(rows))
	}
	for _, r := range rows {
		if r.Title == "" {
			t.Error("a blank row was rendered for a release that no longer exists")
		}
	}
}

func TestALookupErrorSkipsThatRowOnly(t *testing.T) {
	// A failing lookup is not a failing page. One release the index cannot
	// answer for must not take the other bookmarks down with it.
	list := &fakeBookmarks{ids: []int64{1, 2, 3}}
	index := fakeIndex{
		byID: map[int64]pluginapi.ReleaseDetail{1: release(1, "a"), 3: release(3, "c")},
		errs: map[int64]error{2: errors.New("index unavailable")},
	}

	rows := bookmarkedRows(context.Background(), list, index, 1, 50)

	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0].Title != "a" || rows[1].Title != "c" {
		t.Errorf("got %q and %q, want \"a\" and \"c\"", rows[0].Title, rows[1].Title)
	}
}

func TestNoBookmarksRendersAnEmptyListNotNil(t *testing.T) {
	// The template ranges over this. A nil slice would range fine today, but
	// the empty-state arm keys off length, and returning a non-nil empty slice
	// keeps "no bookmarks" a rendering decision rather than a nil check nobody
	// remembers to make.
	list := &fakeBookmarks{ids: nil}

	rows := bookmarkedRows(context.Background(), list, fakeIndex{}, 1, 50)

	if rows == nil {
		t.Error("got nil, want an empty slice")
	}
	if len(rows) != 0 {
		t.Errorf("got %d rows, want 0", len(rows))
	}
	if list.callCount != 1 {
		t.Errorf("storage was asked %d times, want exactly 1", list.callCount)
	}
}
