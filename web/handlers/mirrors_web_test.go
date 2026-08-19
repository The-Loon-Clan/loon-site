package handlers

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// releaseMirrors is where every page on this site asks whether a release is
// also a torrent. Its edges decide what a badge means, so they are worth
// pinning: which source answers, what a failure does, and what an id with no
// torrent looks like.

// fakeMirrors is the tracker's read seam, recording what it was asked.
type fakeMirrors struct {
	out  map[int64]pluginapi.TorrentMirror
	err  error
	seen []int64
}

func (f *fakeMirrors) MirrorsOf(ctx context.Context, ids []int64) (map[int64]pluginapi.TorrentMirror, error) {
	f.seen = ids
	return f.out, f.err
}

func testWeb(m pluginapi.TorrentMirrors) *web {
	return &web{log: slog.New(slog.DiscardHandler), mirrors: m}
}

// A listing can legitimately carry the same release twice, and neither source
// should be handed the repeat — nor a zero, which is not a release id.
func TestReleaseMirrorsAsksOncePerRelease(t *testing.T) {
	f := &fakeMirrors{out: map[int64]pluginapi.TorrentMirror{}}
	testWeb(f).releaseMirrors(context.Background(), []int64{7, 7, 0, 9, 7})
	if len(f.seen) != 2 || f.seen[0] != 7 || f.seen[1] != 9 {
		t.Errorf("asked for %v, want [7 9] — deduplicated, zero dropped, order kept", f.seen)
	}
}

// An unresolvable badge must not take down the listing it decorates.
func TestReleaseMirrorsSwallowsAFailure(t *testing.T) {
	f := &fakeMirrors{err: errors.New("tracker is having a day")}
	if got := testWeb(f).releaseMirrors(context.Background(), []int64{7}); got != nil {
		t.Errorf("got %v on a failed lookup, want nothing rather than a wrong answer", got)
	}
}

// Nothing to ask about is not a question. The nil short-circuit is what keeps a
// pure indexer — and every empty page — from touching the seam at all.
func TestReleaseMirrorsAsksNothingForNothing(t *testing.T) {
	f := &fakeMirrors{}
	for _, ids := range [][]int64{nil, {}, {0}, {-1}} {
		f.seen = nil
		if got := testWeb(f).releaseMirrors(context.Background(), ids); got != nil {
			t.Errorf("ids %v returned %v, want nil", ids, got)
		}
		if f.seen != nil {
			t.Errorf("ids %v reached the seam as %v", ids, f.seen)
		}
	}
}

// attachSwarm and the series page read through the SAME helper, so a row on
// /browse and a row on /series cannot describe one torrent differently.
//
// And the guard is presence, never the counts: 0 seeders is a real figure
// meaning a dead torrent, and a release with no torrent must not look the same.
func TestAttachSwarmMarksPresenceNotCounts(t *testing.T) {
	f := &fakeMirrors{out: map[int64]pluginapi.TorrentMirror{
		1: {InfoHash: "abc", Seeders: 0, Leechers: 0},
	}}
	rows := testWeb(f).attachSwarm(context.Background(), []searchRow{{ID: 1}, {ID: 2}})
	if !rows[0].HasSwarm {
		t.Error("a dead torrent (0/0) was not marked as having a swarm — it exists, it is just dead")
	}
	if rows[1].HasSwarm {
		t.Error("a release with no torrent was marked as having a swarm")
	}
}
