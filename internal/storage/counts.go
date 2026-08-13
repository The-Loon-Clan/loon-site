package storage

import "context"

// Counts the site reports about itself.
//
// Shared, not dashboard-specific, and that turned out to matter: `SELECT
// COUNT(*) FROM users` was written twice, once in the staff dashboard and once
// on the public stats page, and the two had already drifted apart in how they
// handled a failure. One name, one statement, both callers.
//
// Named methods rather than the handler passing SQL strings to a generic
// helper. countOrDash(ctx, `SELECT COUNT(*) FROM users`) put the schema in the
// page that renders it: a table rename had to be chased through view code, and
// nothing could be checked without building a request.
//
// Each returns (value, ok) rather than (value, error). The dashboard's rule is
// that an unmeasurable figure shows an em dash rather than a zero — "0 members"
// and "members are not measurable here" are different claims, and on a staff
// page a wrong number is one that gets acted on. A bool carries exactly that
// distinction; what went wrong is not something the tile can say.

// CountUsers is every registered account.
func (st *Store) CountUsers(ctx context.Context) (int, bool) {
	return st.count(ctx, `SELECT COUNT(*) FROM users`)
}

// CountUsersJoinedLastWeek is accounts created in the last seven days.
func (st *Store) CountUsersJoinedLastWeek(ctx context.Context) (int, bool) {
	return st.count(ctx, `SELECT COUNT(*) FROM users WHERE created_at > now() - interval '7 days'`)
}

// CountOpenTickets is support tickets not yet closed.
func (st *Store) CountOpenTickets(ctx context.Context) (int, bool) {
	return st.count(ctx, `SELECT COUNT(*) FROM support_tickets WHERE status <> 'closed'`)
}

// CountForumThreads is threads a moderator has not hidden.
func (st *Store) CountForumThreads(ctx context.Context) (int, bool) {
	return st.count(ctx, `SELECT COUNT(*) FROM forum_threads WHERE hidden_at IS NULL`)
}

// count runs a single-value COUNT and reports whether it could be read.
//
// Unexported, and it takes SQL, which is the thing this file exists to stop
// handlers doing — the difference is that the statements are written here, next
// to the schema they name, and a caller cannot supply one.
func (st *Store) count(ctx context.Context, q string) (int, bool) {
	var n int
	if err := st.db.GetContext(ctx, &n, q); err != nil {
		return 0, false
	}
	return n, true
}

// CountForumPosts is posts a moderator has not hidden.
func (st *Store) CountForumPosts(ctx context.Context) (int, bool) {
	return st.count(ctx, `SELECT COUNT(*) FROM forum_posts WHERE hidden_at IS NULL`)
}
