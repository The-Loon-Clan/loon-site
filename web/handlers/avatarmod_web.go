package handlers

import (
	"github.com/the-loon-clan/loon-site/internal/storage"

	"context"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

// The new-avatar review queue.
//
// avatar_web.go shipped uploads with no way for anybody but the owner to take
// one down, on a site whose registration mode can be open. Re-encoding the
// image handles the file being hostile; it does nothing about the picture being
// hostile, and that is the half a person has to look at.
//
// POST-moderation, not pre. The avatar goes live on upload and appears here for
// review afterwards. Holding it back until a moderator gets to it means a
// member uploads a picture, sees no change, and uploads it again — the queue
// fills with duplicates of the same image and the feature reads as broken. The
// tradeoff is real and it is the right way round: the window in which a bad
// avatar is visible is minutes, and the cost of the alternative is paid by
// everybody, constantly.
//
// NO NEW TABLE. Two timestamps on users answer it:
//
//	avatar_updated_at   when the picture last changed
//	avatar_reviewed_at  when a moderator last looked at it
//
// and "pending" is reviewed_at being null or older than updated_at. That falls
// out correctly for the case a separate queue table gets wrong: somebody whose
// avatar was approved uploads a NEW one, and it is pending again because the
// timestamp moved, without anything having to remember to re-enqueue them.

// modLog reports a failed moderation read. Package level, like storage.SubsLog: these
// two functions need a logger and nothing else off the web struct.
var modLog = func(ctx context.Context, what string, err error) {
	slog.Error("avatar moderation read", "list", what, "err", err)
}

// pendingAvatarWhere is the one definition of "needs review", used by the list
// and the count so a badge can never disagree with the page it links to.
//
// COALESCE on avatar_updated_at, not a bare comparison. An avatar uploaded
// before this migration existed has a NULL timestamp, and in SQL every
// comparison against NULL is NULL rather than false — so `reviewed_at <
// updated_at` was neither true nor false for those rows. Pending happened to
// come out right through the IS NULL branch; reviewed did not, and an approved
// legacy avatar matched NEITHER predicate and disappeared from both tabs. An
// audit trail with a silent hole in it is worse than no audit trail, because
// it is read as complete.
//
// The epoch is the honest reading of a missing timestamp: we do not know when
// the picture was uploaded, only that it was before now, so any review counts.
const pendingAvatarWhere = `avatar_path <> ''
	  AND (avatar_reviewed_at IS NULL
	       OR avatar_reviewed_at < COALESCE(avatar_updated_at, 'epoch'::timestamptz))`

// reviewedAvatarWhere is the exact complement, written out rather than derived
// with NOT so the NULL handling is visible in both places.
const reviewedAvatarWhere = `avatar_path <> ''
	  AND avatar_reviewed_at IS NOT NULL
	  AND avatar_reviewed_at >= COALESCE(avatar_updated_at, 'epoch'::timestamptz)`

// avatarReviewRow is one member awaiting review.
type avatarReviewRow struct {
	ID       int64  `db:"id"`
	Username string `db:"username"`
	Avatar   string `db:"avatar_path"`
	Uploaded string `db:"uploaded"`
	// Reviewed is the previous decision, shown on the history tab so "who
	// cleared this and when" has an answer.
	Reviewed string `db:"reviewed"`
}

// avatarModRows is one page of the queue. Fixed rather than configurable, for
// the reason the reports plugin fixes its own: an operator's sense of how deep
// the queue is should not shift underneath them.
const avatarModRows = 60

// listPendingAvatars returns avatars awaiting review, newest first.
//
// Newest first is deliberate and is the opposite of a support queue. The damage
// an avatar does is being seen, so the one uploaded a minute ago is the urgent
// one; the one from last week has already been seen by everybody it was going
// to be seen by.
func listPendingAvatars(ctx context.Context, db storage.Conn) []avatarReviewRow {
	if !db.Valid() {
		return nil
	}
	var rows []avatarReviewRow
	if err := db.SelectContext(ctx, &rows, `
		SELECT id, username, avatar_path,
		       COALESCE(to_char(avatar_updated_at, 'DD Mon YYYY HH24:MI'), '') AS uploaded,
		       '' AS reviewed
		  FROM users
		 WHERE `+pendingAvatarWhere+`
		 ORDER BY avatar_updated_at DESC NULLS LAST
		 LIMIT $1`, avatarModRows); err != nil {
		// Logged, never swallowed: an empty queue and a broken query look
		// identical on the page, and the first one is the one that gets
		// believed.
		modLog(ctx, "pending avatars", err)
		return nil
	}
	return rows
}

// listReviewedAvatars is the audit trail — avatars that have been looked at and
// left in place.
func listReviewedAvatars(ctx context.Context, db storage.Conn) []avatarReviewRow {
	if !db.Valid() {
		return nil
	}
	var rows []avatarReviewRow
	if err := db.SelectContext(ctx, &rows, `
		SELECT id, username, avatar_path,
		       COALESCE(to_char(avatar_updated_at, 'DD Mon YYYY HH24:MI'), '')  AS uploaded,
		       COALESCE(to_char(avatar_reviewed_at, 'DD Mon YYYY HH24:MI'), '') AS reviewed
		  FROM users
		 WHERE `+reviewedAvatarWhere+`
		 ORDER BY avatar_reviewed_at DESC
		 LIMIT $1`, avatarModRows); err != nil {
		modLog(ctx, "reviewed avatars", err)
		return nil
	}
	return rows
}

// countPendingAvatars feeds the badge. Best effort: a queue count that fails to
// read must not take the page with it.
func countPendingAvatars(ctx context.Context, db storage.Conn) int {
	if !db.Valid() {
		return 0
	}
	var n int
	if err := db.GetContext(ctx, &n,
		`SELECT count(*) FROM users WHERE `+pendingAvatarWhere); err != nil {
		return 0
	}
	return n
}

// markAvatarReviewed records that a moderator looked and left it alone.
func markAvatarReviewed(ctx context.Context, db storage.Conn, userID int64) error {
	_, err := db.ExecContext(ctx,
		`UPDATE users SET avatar_reviewed_at = now() WHERE id = $1`, userID)
	return err
}

// avatarModPage serves GET /moderation/avatars.
func (w *web) avatarModPage(c *gin.Context) {
	ctx := c.Request.Context()
	history := c.Query("reviewed") == "1"
	rows := listPendingAvatars(ctx, w.data.DB())
	if history {
		rows = listReviewedAvatars(ctx, w.data.DB())
	}
	w.render(c, "moderation_avatars.html", map[string]any{
		"Title":   "New avatars",
		"Rows":    rows,
		"History": history,
		"Pending": countPendingAvatars(ctx, w.data.DB()),
		"Limit":   avatarModRows,
		"Done":    c.Query(queryDone),
	})
}

// avatarModAction serves POST /moderation/avatars — approve or clear.
//
// Both actions mark the avatar reviewed, including the clear: an avatar that
// was removed must not sit in the queue afterwards, and "reviewed" is the
// honest record of what happened either way.
func (w *web) avatarModAction(c *gin.Context) {
	// Mounted behind staffOnly (auth.Require(core.RoleMod)), which has already
	// resolved and rejected; this only reads what that gate put in context.
	actor, ok := w.viewer(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	// A missing or nonsense id binds to zero: the form did not come from the
	// page, so redirect rather than report.
	in, _ := readAvatarModInput(c)
	id := in.ID
	if id <= 0 {
		c.Redirect(http.StatusFound, "/moderation/avatars")
		return
	}
	// The id travels in the BODY, not the path — the same reason the reports
	// plugin does it: an action route carrying :id would register a path
	// parameter, and this group already has none.
	switch in.Action {
	case "clear":
		// No undo token kept: the record belongs to the SUBJECT, not to the
		// moderator, and offering a moderator an undo for somebody else's row
		// would let them restore a picture on an account that is not theirs.
		// A moderator who clears the wrong one asks the member to upload it
		// again, which is the honest cost of the action.
		if _, err := w.clearAvatar(ctx, w.data.DB(), id); err != nil {
			w.log.Error("moderation clear avatar", "user", id, "actor", actor.ID, "err", err)
			c.Redirect(http.StatusFound, "/moderation/avatars")
			return
		}
		// Logged with WHO did it. On a queue whose whole job is judgement
		// calls, "an avatar disappeared" without a name attached is the report
		// nobody can follow up.
		w.log.Info("avatar cleared by moderator", "user", id, "actor", actor.ID, "actor_name", actor.Username)
		if err := markAvatarReviewed(ctx, w.data.DB(), id); err != nil {
			w.log.Error("mark avatar reviewed", "user", id, "err", err)
		}
		c.Redirect(http.StatusFound, "/moderation/avatars?done=cleared")
	default:
		if err := markAvatarReviewed(ctx, w.data.DB(), id); err != nil {
			w.log.Error("mark avatar reviewed", "user", id, "err", err)
		}
		w.log.Info("avatar approved", "user", id, "actor", actor.ID, "actor_name", actor.Username)
		c.Redirect(http.StatusFound, "/moderation/avatars?done=approved")
	}
}
