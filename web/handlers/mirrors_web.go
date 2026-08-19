package handlers

import (
	"context"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

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

// mirrorRelease turns one release into a torrent on this site's tracker, and
// sends the member to it — the write side of the same relationship.
//
// POST, and it has to be: this creates a row. A GET that mutates is one
// prefetching browser away from mirroring the whole index on somebody's behalf,
// and gin's CSRF middleware only covers the methods that can carry a token.
//
// Idempotent at the far end, so a double-click, a refresh or a second member
// finds the first torrent rather than making another. That means this handler
// needs no lock, no check-then-act, and no "already mirrored" error path.
func (w *web) mirrorRelease(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.String(http.StatusBadRequest, "bad id")
		return
	}
	if w.mirrorMaker == nil || w.usenet == nil {
		// No tracker, or no index to mirror FROM. Not an error page: the button
		// only renders when both are wired, so arriving here is a stale form.
		c.Redirect(http.StatusSeeOther, "/release/"+strconv.FormatInt(id, 10))
		return
	}
	u, ok := w.currentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusSeeOther, "/login?next=/release/"+strconv.FormatInt(id, 10))
		return
	}
	ctx := c.Request.Context()
	d, found, err := w.usenet.ReleaseByID(ctx, id)
	if err != nil || !found {
		c.Status(http.StatusNotFound)
		w.render(c, "release.html", map[string]any{"Title": "Not found", "Missing": true})
		return
	}

	// The NZB's real file list, which is what makes the torrent's STRUCTURE
	// true: an NZB names its files and their sizes. The piece hashes cannot be
	// real — this site holds pointers to articles, not bytes — and the tracker
	// says so in its own contract.
	req := pluginapi.MirrorRequest{
		ReleaseID: d.ID, Name: d.Title, Size: d.Size, UserID: u.ID,
		Files: make([]pluginapi.MirrorFile, 0, len(d.Files)),
	}
	for _, f := range d.Files {
		req.Files = append(req.Files, pluginapi.MirrorFile{Path: f.Filename, Length: f.Bytes})
	}
	m, err := w.mirrorMaker.Mirror(ctx, req)
	if err != nil {
		w.log.Error("mirror release", "release", id, "err", err)
		// Back to the release, which is where the member was and where the
		// absence of a swarm panel now says it did not work. A dedicated error
		// page for a button that can be pressed again teaches nothing.
		c.Redirect(http.StatusSeeOther, "/release/"+strconv.FormatInt(id, 10))
		return
	}
	w.log.Info("mirrored release to the tracker", "release", id, "hash", m.InfoHash, "user", u.ID)
	to := m.Href
	if to == "" {
		to = "/release/" + strconv.FormatInt(id, 10)
	}
	c.Redirect(http.StatusSeeOther, to)
}

// releaseIDsOf is the id slice releaseMirrors takes, off a page of rows.
func releaseIDsOf(rows []searchRow) []int64 {
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}
