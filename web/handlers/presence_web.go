package handlers

import (
	"context"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
)

// Last seen — docs/MOCKS.md M1, which stood in for it with an em dash.
//
// The write hangs off the session resolve, which runs on EVERY authenticated
// request. Writing there unthrottled would put an UPDATE on the users row in
// front of every page load, image and poll — so it is throttled in memory and
// the database sees at most one write per user per interval.
//
// Deliberately NOT a "who is online" list. That needs a presence window and an
// opinion about what counts as online, and this column answers a different,
// smaller question: when was this account last used.

// lastSeenInterval is how stale the column may get. Five minutes is well under
// any "active today" reading of the value and cuts the writes by orders of
// magnitude on a page that loads a dozen sub-resources.
const lastSeenInterval = 5 * time.Minute

// lastSeen throttles the write per user id.
//
// A plain map guarded by a mutex, not a cache: the entries are one timestamp
// each, bounded by the number of accounts that have made a request since boot,
// and losing the whole thing on restart costs exactly one extra UPDATE per
// user. Anything more elaborate would be more machinery than the feature.
var lastSeen = struct {
	sync.Mutex
	at map[int64]time.Time
}{at: map[int64]time.Time{}}

// touchLastSeen records that a user was active, at most once per interval.
// Best-effort throughout: this must never fail a request that was otherwise
// fine, so every error is dropped rather than surfaced.
func touchLastSeen(ctx context.Context, db *sqlx.DB, userID int64) {
	if db == nil || userID <= 0 {
		return
	}
	now := time.Now()
	lastSeen.Lock()
	if prev, ok := lastSeen.at[userID]; ok && now.Sub(prev) < lastSeenInterval {
		lastSeen.Unlock()
		return
	}
	lastSeen.at[userID] = now
	lastSeen.Unlock()

	// Detached context: this rides on a request but is not part of it, and a
	// client that disconnects mid-page should not cancel the write.
	go func() {
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = db.ExecContext(c, `UPDATE users SET last_seen_at = now() WHERE id = $1`, userID)
	}()
}

// lastSeenAt reads the column for a profile. ok is false when it has never
// been set — a brand-new account, or one that predates the column — so the
// tile can say nothing rather than claim the epoch.
func lastSeenAt(ctx context.Context, db *sqlx.DB, userID int64) (time.Time, bool) {
	if db == nil || userID <= 0 {
		return time.Time{}, false
	}
	var t *time.Time
	if err := db.GetContext(ctx, &t,
		`SELECT last_seen_at FROM users WHERE id = $1`, userID); err != nil || t == nil {
		return time.Time{}, false
	}
	return *t, true
}
