package storage

import (
	"context"
	"log/slog"

	"github.com/jmoiron/sqlx"
)

// ListCommunitySubs returns the communities this member has joined.
func ListCommunitySubs(ctx context.Context, userID int64) []SubscriptionRow {
	if SubscriptionsDB == nil || userID <= 0 {
		return nil
	}
	var rows []SubscriptionRow
	if err := SubscriptionsDB.SelectContext(ctx, &rows, `
		SELECT c.name                                   AS title,
		       '/c/' || c.slug                          AS href,
		       (SELECT count(*)::text || ' member' ||
		               CASE WHEN count(*) = 1 THEN '' ELSE 's' END
		          FROM community_subscribers s2
		         WHERE s2.community_id = c.id)          AS sub,
		       to_char(s.created_at, 'DD Mon YYYY')     AS since
		  FROM community_subscribers s
		  JOIN communities c ON c.id = s.community_id
		 WHERE s.user_id = $1
		   AND c.hidden_at IS NULL
		 ORDER BY s.created_at DESC`, userID); err != nil {
		// Logged, not swallowed. A read that fails renders as "you are not in
		// any communities", which is a confident lie — and it is exactly how
		// the bookmarks half of this page shipped empty: the query named a
		// column that does not exist and said nothing.
		SubsLog(ctx, "communities", err)
		return nil
	}
	return rows
}

// ListBookmarkSubs returns the releases this member has bookmarked.
//
// Bookmarks already have their own page, and they are here too on purpose: this
// page answers "what am I keeping up with", and a bookmark is one of the
// answers. The link goes to the full list rather than duplicating it.
func ListBookmarkSubs(ctx context.Context, userID int64, limit int) []SubscriptionRow {
	if SubscriptionsDB == nil || userID <= 0 {
		return nil
	}
	var rows []SubscriptionRow
	if err := SubscriptionsDB.SelectContext(ctx, &rows, `
		SELECT n.title                                  AS title,
		       '/release/' || n.id::text                AS href,
		       COALESCE(n.group_name, '')               AS sub,
		       to_char(b.created_at, 'DD Mon YYYY')     AS since
		  FROM release_bookmark b
		  JOIN usenet.nzbs n ON n.id = b.release_id
		 WHERE b.user_id = $1
		 ORDER BY b.created_at DESC
		 LIMIT $2`, userID, limit); err != nil {
		SubsLog(ctx, "bookmarks", err)
		return nil
	}
	return rows
}

// SubscriptionRow is one thing being followed, whatever kind it is.
type SubscriptionRow struct {
	Title string `db:"title"`
	Href  string `db:"href"`
	Sub   string `db:"sub"` // the second line: members, category, size
	Since string `db:"since"`
}

var SubscriptionsDB *sqlx.DB

// SubsLog reports a failed read. Package-level so the two list functions stay
// free of a *web receiver they need for nothing else.
var SubsLog = func(ctx context.Context, what string, err error) {
	slog.Error("subscriptions read", "list", what, "err", err)
}
