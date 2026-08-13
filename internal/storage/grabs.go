package storage

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
)

// RecordGrab writes one row. Best-effort by design: a download that succeeded
// must not fail because the counter did, so the caller ignores the error and
// this never blocks the response.
func (st *Store) RecordGrab(ctx context.Context, releaseID int64, userID int64) {
	var uid any
	if userID > 0 {
		uid = userID
	}
	_, _ = st.db.ExecContext(ctx,
		`INSERT INTO release_grab (release_id, user_id) VALUES ($1, $2)`, releaseID, uid)
}

// GrabCounts returns the grab tally for a set of releases in ONE query.
// Releases with no grabs are simply absent from the map — a caller rendering
// "N downloads" should show nothing rather than a zero it did not measure.
func (st *Store) GrabCounts(ctx context.Context, releaseIDs []int64) map[int64]int {
	if len(releaseIDs) == 0 {
		return nil
	}
	seen := make(map[int64]bool, len(releaseIDs))
	ids := make([]int64, 0, len(releaseIDs))
	for _, id := range releaseIDs {
		if id > 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	q, args, err := sqlx.In(
		`SELECT release_id, COUNT(*) AS n FROM release_grab
		  WHERE release_id IN (?) GROUP BY release_id`, ids)
	if err != nil {
		return nil
	}
	var rows []struct {
		ReleaseID int64 `st.db:"release_id"`
		N         int   `st.db:"n"`
	}
	if err := st.db.SelectContext(ctx, &rows, st.db.Rebind(q), args...); err != nil {
		return nil
	}
	out := make(map[int64]int, len(rows))
	for _, r := range rows {
		out[r.ReleaseID] = r.N
	}
	return out
}

// PopularGrabs answers "most grabbed in the last N days" — the read UNIT3D's
// trending pages and the home page's "popular this week" panel want.
//
// Returns release ids and their counts, ordered. The CALLER resolves the ids
// to releases through the usenet capability: this table stores no titles, so a
// release deleted from the index simply drops out rather than lingering as a
// stale row that outranks live ones.
func (st *Store) PopularGrabs(ctx context.Context, days, limit int) ([]int64, map[int64]int) {
	var rows []struct {
		ReleaseID int64 `st.db:"release_id"`
		N         int   `st.db:"n"`
	}
	if err := st.db.SelectContext(ctx, &rows,
		`SELECT release_id, COUNT(*) AS n FROM release_grab
		  WHERE created_at >= $1
		  GROUP BY release_id ORDER BY n DESC, release_id DESC LIMIT $2`,
		time.Now().AddDate(0, 0, -days), limit); err != nil {
		return nil, nil
	}
	ids := make([]int64, 0, len(rows))
	counts := make(map[int64]int, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ReleaseID)
		counts[r.ReleaseID] = r.N
	}
	return ids, counts
}

// UploaderGrabTotals is the shape the economy plugin's seam wants: grabs per
// release, so a host can attribute them to whoever indexed it. This indexer has
// no per-release uploader (releases come from crawling, not uploads), so the
// plugin stays unwired — but the DATA it needs now exists, which is the half
// that was actually missing.
func (st *Store) UploaderGrabTotals(ctx context.Context, since time.Time) (map[int64]int, error) {
	var rows []struct {
		ReleaseID int64 `st.db:"release_id"`
		N         int   `st.db:"n"`
	}
	if err := st.db.SelectContext(ctx, &rows,
		`SELECT release_id, COUNT(*) AS n FROM release_grab
		  WHERE created_at >= $1 GROUP BY release_id`, since); err != nil {
		return nil, err
	}
	out := make(map[int64]int, len(rows))
	for _, r := range rows {
		out[r.ReleaseID] = r.N
	}
	return out, nil
}
