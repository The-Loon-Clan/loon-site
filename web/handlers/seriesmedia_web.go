package handlers

import (
	"context"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/mediainfo"
)

// "Which of these copies do I want?" — the half of that question a filename
// cannot answer.
//
// The series page is where it matters most: six releases of one episode, and
// until now only the tags carved out of their names to choose between them.
// Those tags are what the poster CLAIMED. A mediainfo report is what somebody
// who actually downloaded the file measured — "HEVC at 10.4 Mb/s · E-AC-3 JOC
// 6 channels" — which is the difference between a 2160p label and a 2160p
// bitrate.
//
// docs/BACKLOG.md #18 is this gap, and it names the shape of the fix: the
// mediainfo plugin has answered the batch question for a while (SummariesFor)
// and nothing consumed it, deliberately, because "inventing a contract before
// there is a second side is how SEAMS.md's bare-string tier grows. The consumer
// comes first, then the contract." This is that consumer. A pluginapi contract
// is requested and slots in underneath without the page changing.
//
// The STORE is built per request rather than held on the web struct, and the
// reasoning is cheatStore's word for word: a host that never enables the plugin
// should not carry a handle to its schema, and one that enables it later should
// not need a restart for this page to start working.
func (w *web) mediaStore() *mediainfo.PGStore {
	if w.data == nil || !w.data.DB().Valid() {
		return nil
	}
	return mediainfo.NewPGStore(core.NewStorage(w.data.DB().Raw()).SchemaDB("mediainfo"))
}

// releaseSummaries resolves the one-line "what is in this copy" for a page of
// releases, in ONE query, keyed by release id. Absent ids simply have no line.
//
// Resolved once per page and passed IN to the grouping, so that stays a pure
// function — the same arrangement releaseMirrors already has with the tracker's
// side, and for the same reason: a per-row lookup here is an N+1 on a page that
// legitimately carries three hundred rows.
func (w *web) releaseSummaries(ctx context.Context, releaseIDs []int64) map[int64]string {
	if len(releaseIDs) == 0 {
		return nil
	}
	// Deduplicated before asking: a listing can carry the same release twice,
	// and the query should not be handed the repeat.
	seen := make(map[int64]bool, len(releaseIDs))
	ids := make([]int64, 0, len(releaseIDs))
	for _, id := range releaseIDs {
		if id > 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	st := w.mediaStore()
	if len(ids) == 0 || st == nil {
		return nil
	}
	out, err := st.SummariesFor(ctx, ids)
	if err != nil {
		// Logged and dropped, never propagated. This decorates a listing; a
		// host whose mediainfo schema is absent or unreadable should show the
		// page it always showed, not a 500. Same call releaseMirrors makes.
		w.log.Error("mediainfo summaries", "count", len(ids), "err", err)
		return nil
	}
	return out
}
