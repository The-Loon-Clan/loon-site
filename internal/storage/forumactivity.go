package storage

import (
	"context"
)

// ListTopics returns threads this member started, most recently active first.
func (st *Store) ListTopics(ctx context.Context, userID int64) []ActivityRow {
	if userID <= 0 {
		return nil
	}
	var rows []ActivityRow
	if err := st.db.SelectContext(ctx, &rows, `
		SELECT t.id                              AS thread_id,
		       t.title,
		       COALESCE(c.name, '')              AS category,
		       ''                                AS excerpt,
		       t.reply_count                     AS replies,
		       to_char(t.last_post_at, 'DD Mon YYYY') AS at
		  FROM forum_threads t
		  LEFT JOIN forum_categories c ON c.id = t.category_id
		 WHERE t.user_id = $1 AND t.hidden_at IS NULL
		 ORDER BY t.last_post_at DESC
		 LIMIT $2`, userID, ForumActivityRows); err != nil {
		return nil
	}
	return rows
}

// ListPosts returns this member's replies, newest first, each with the thread
// it landed in so a row is worth clicking.
//
// Own threads are NOT excluded: a reply in your own thread is still a post,
// and filtering it out would make the two pages disagree about the same row.
func (st *Store) ListPosts(ctx context.Context, userID int64) []ActivityRow {
	if userID <= 0 {
		return nil
	}
	var rows []ActivityRow
	if err := st.db.SelectContext(ctx, &rows, `
		SELECT p.thread_id,
		       t.title,
		       COALESCE(c.name, '')             AS category,
		       -- Trimmed in SQL so a 40 kB post is not carried across the wire
		       -- to be cut to a line in Go. left() is by CHARACTERS here, not
		       -- bytes, so this cannot split a multi-byte rune.
		       left(p.body, 300)                AS excerpt,
		       0                                AS replies,
		       to_char(p.created_at, 'DD Mon YYYY') AS at
		  FROM forum_posts p
		  JOIN forum_threads t ON t.id = p.thread_id
		  LEFT JOIN forum_categories c ON c.id = t.category_id
		 WHERE p.user_id = $1
		   AND p.hidden_at IS NULL
		   -- A reply in a thread a moderator hid is not reachable either.
		   AND t.hidden_at IS NULL
		 ORDER BY p.created_at DESC
		 LIMIT $2`, userID, ForumActivityRows); err != nil {
		return nil
	}
	return rows
}

// ActivityRow is one line of either listing. One struct for both because they
// differ only in what Title points at — a thread of one's own, or the thread
// a reply landed in — and two near-identical structs is how the two templates
// drift apart.
type ActivityRow struct {
	ThreadID int    `st.db:"thread_id"`
	Title    string `st.db:"title"`
	Category string `st.db:"category"`
	Excerpt  string `st.db:"excerpt"`
	Replies  int    `st.db:"replies"`
	At       string `st.db:"at"`
}

// ForumActivityRows caps either listing. A prolific member's post history is
// unbounded, and this is an account page, not an archive.
const ForumActivityRows = 100
