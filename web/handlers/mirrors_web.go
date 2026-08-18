package handlers

import (
	"context"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// "Is this release also on the tracker?", asked for a whole page at once.
//
// This site holds the same content two ways — an NZB posted to Usenet and a
// torrent made from it — and `torrents.nzb_id` is the link between them. Every
// listing that wants to offer both goes through here, so there is one answer to
// the question rather than one per page.
//
// TWO SOURCES, in order:
//
//  1. pluginapi.TorrentMirrors, the seam the tracker publishes. This is the
//     right way: the host asks a capability and never learns the tracker's
//     schema, so a host whose tracker lives in another database still works.
//  2. internal/storage's SwarmCounts, which reads tracker.torrents directly.
//     It predates the seam and is the fallback for a tracker that is enabled
//     but has not registered — an idle one, or an older build.
//
// Both are absent on a pure indexer, and then every caller renders the NZB side
// alone, which is the whole page such a site has ever had.

// releaseMirrors maps release id → the torrent made from it, for those release
// ids that have one.
//
// Ids with no torrent are ABSENT from the map rather than present and zero: 0
// seeders is a real figure meaning a dead torrent, and it must not be
// indistinguishable from a release that was never mirrored.
func (w *web) releaseMirrors(ctx context.Context, releaseIDs []int64) map[int64]pluginapi.TorrentMirror {
	if len(releaseIDs) == 0 {
		return nil
	}
	// Deduplicated before asking: a listing can legitimately carry the same
	// release twice, and neither source should be handed the repeat.
	seen := make(map[int64]bool, len(releaseIDs))
	ids := make([]int64, 0, len(releaseIDs))
	for _, id := range releaseIDs {
		if id > 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	if w.mirrors != nil {
		out, err := w.mirrors.MirrorsOf(ctx, ids)
		if err != nil {
			// Logged and dropped, not propagated: a badge that could not be
			// resolved must not take down the listing it decorates.
			w.log.Error("tracker mirrors", "count", len(ids), "err", err)
			return nil
		}
		return out
	}
	swarms := w.data.SwarmCounts(ctx, ids)
	if len(swarms) == 0 {
		return nil
	}
	out := make(map[int64]pluginapi.TorrentMirror, len(swarms))
	for id, s := range swarms {
		out[id] = pluginapi.TorrentMirror{
			InfoHash: s.InfoHash, Seeders: s.Seeders, Leechers: s.Leechers,
		}
	}
	return out
}

// releaseIDsOf is the id slice releaseMirrors takes, off a page of rows.
func releaseIDsOf(rows []searchRow) []int64 {
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}
