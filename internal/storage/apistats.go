package storage

import (
	"context"
	"time"
)

// Per-subject API request accounting — the numbers behind the daily quota
// and the usage graph on the API-key page. A subject is "u:<id>" for a keyed
// request and "ip:<addr>" for an anonymous one on a public site, so
// dropping the key is not a way around the cap.
//
// One row per subject per day, incremented in the request path. That write
// is the cost of honest numbers; it is one indexed upsert, and the read tier
// this demo mirrors (loon-api) pays the same price in Redis.

// APIDay is one day of one subject's usage.
type APIDay struct {
	Day   time.Time `db:"day"`
	Count int64     `db:"count"`
}

// MigrateAPIRequests creates the table and prunes what no page shows any
// more — the graph reaches back two weeks, the prune keeps five for slack.
func (st *Store) MigrateAPIRequests() error {
	if _, err := st.db.Exec(`CREATE TABLE IF NOT EXISTS api_request_days (
	    subject TEXT NOT NULL,
	    day     DATE NOT NULL,
	    count   BIGINT NOT NULL DEFAULT 0,
	    PRIMARY KEY (subject, day)
	)`); err != nil {
		return err
	}
	_, err := st.db.Exec(`DELETE FROM api_request_days
	    WHERE day < CURRENT_DATE - INTERVAL '35 days'`)
	return err
}

// IncrAPIRequest counts one request and returns the subject's total for
// today — count and check in one round trip, so the quota gate costs the
// request path a single statement.
func (st *Store) IncrAPIRequest(ctx context.Context, subject string) (int64, error) {
	var n int64
	err := st.db.GetContext(ctx, &n, `
		INSERT INTO api_request_days (subject, day, count)
		VALUES ($1, CURRENT_DATE, 1)
		ON CONFLICT (subject, day) DO UPDATE
		   SET count = api_request_days.count + 1
		RETURNING count`, subject)
	return n, err
}

// APIRequestDays returns the subject's last n days that saw any traffic,
// oldest first. Days with no row are days with no requests; the caller
// fills the gaps, because the GRAPH owns its own axis.
func (st *Store) APIRequestDays(ctx context.Context, subject string, n int) ([]APIDay, error) {
	var out []APIDay
	err := st.db.SelectContext(ctx, &out, `
		SELECT day, count FROM api_request_days
		 WHERE subject = $1 AND day > CURRENT_DATE - ($2 * INTERVAL '1 day')
		 ORDER BY day`, subject, n)
	return out, err
}
