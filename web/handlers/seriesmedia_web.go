package handlers

import "context"

// "Which of these copies do I want?" — the half of that question a filename
// cannot answer.
//
// The series page is where it matters most: six releases of one episode, and
// until this landed only the tags carved out of their names to choose between
// them. Those tags are what the poster CLAIMED. A mediainfo report is what
// somebody who actually downloaded the file measured — "HEVC at 10.4 Mb/s ·
// E-AC-3 JOC 6 channels" — and the first row this ever rendered had tags
// saying x264 against a report saying HEVC, which is the whole argument for
// the feature in one line.
//
// docs/BACKLOG.md #18 was this gap and it prescribed the order: the store
// method existed with no contract deliberately, because "inventing one before
// there is a second side is how SEAMS.md's bare-string tier grows. The
// consumer comes first, then the contract." The consumer came first; the
// contract (pluginapi.MediaSummaries) followed, and this now reads through it.
//
// It briefly did NOT. Between the two there was a version that built the
// plugin's PGStore per request via core.NewStorage(...).SchemaDB("mediainfo")
// — legitimate, and the same move cheatqueue_web.go makes for the tracker, but
// it meant this file knew how another plugin wires its storage. The seam took
// that knowledge back, which is the point of having one.

// releaseSummaries resolves the one-line "what is in this copy" for a page of
// releases, in ONE query, keyed by release id.
//
// BATCH is load-bearing, not an optimisation: a per-release call reads
// perfectly naturally at the call site and is fifty round trips inside a render
// loop. Resolved once per page and passed IN to the grouping so that stays a
// pure function — the same arrangement releaseMirrors has with the tracker's
// side.
//
// The result is SPARSE. A release nobody has reported on simply has no key,
// and that is the ordinary case rather than an error: most releases have no
// report, and a column of "unknown" on a page where three rows have data reads
// as a broken feature instead of an unanswered question.
func (w *web) releaseSummaries(ctx context.Context, releaseIDs []int64) map[int64]string {
	if len(releaseIDs) == 0 || w.mediaSummaries == nil {
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
	if len(ids) == 0 {
		return nil
	}
	out, err := w.mediaSummaries.SummariesFor(ctx, ids)
	if err != nil {
		// Logged and dropped, never propagated. This DECORATES a listing; a
		// host whose mediainfo store is unreadable should show the page it
		// always showed, not a 500. The same call releaseMirrors makes.
		w.log.Error("mediainfo summaries", "count", len(ids), "err", err)
		return nil
	}
	return out
}
