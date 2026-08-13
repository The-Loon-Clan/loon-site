package handlers

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-site/internal/storage"
	"github.com/the-loon-clan/loon/core"
)

// Community moderation — the queue reported things land in, as distinct from
// the staff queue in avatarmod_web.go.
//
// ADMIN-ONLY for now (see the routes in main.go). The voting below is built and
// wired, and the gate is the only thing standing between this and a
// community-driven queue: admin-curated first, community-driven second, same
// table, same tallies, same resolution path. Reporting is open to every member
// throughout, because a curated queue nobody can file into is an empty one.
//
// Two different questions, deliberately kept apart. "Has a moderator looked at
// this?" is a staff workflow and belongs to staff. "Does this community want
// this here?" is not a question a moderator can answer on the community's
// behalf, and a site that routes it to one anyway has replaced sentiment with
// one person's taste.
//
// ONE item kind is wired: a reported avatar. The prod site votes on screenshots
// and release edits as well, and neither of those exists in this stack — there
// is no screenshot feature and no release-edit flow, so tabs for them would be
// advertising something that is not there. The schema takes a kind so the
// second and third arrive as rows rather than as a second queue; see
// itemKindAvatar and the switch in applyResolution, which are the only two
// places that know what an avatar is.
//
// Reporting something already reported is a VOTE, not a second item. The
// alternative is fifty rows for one picture, each with a handful of votes and
// none of them reaching quorum — the queue looks busy and decides nothing.

// itemKindAvatar is the only kind wired today.
const itemKindAvatar = "avatar"

const (
	// voteQuorum is how many votes an item needs before sentiment counts at
	// all. Below it the queue does nothing, because two people are not a
	// community and the first two to arrive are not a representative pair.
	//
	// This is THE number a real deployment tunes, and it wants to be a
	// meaningful fraction of the people who actually vote — not of the
	// membership, most of whom never will.
	voteQuorum = 3

	// voteMajority is the share of cast votes needed to act. Two thirds, not a
	// bare half: removing somebody's picture on a 3-2 split is a decision the
	// losing half will reasonably call arbitrary, and the cost of waiting for
	// a clearer answer is that a picture stays up a little longer.
	voteMajorityNum = 2
	voteMajorityDen = 3
)

// resolution values.
const (
	resolutionRemoved = "removed"
	resolutionKept    = "kept"
)

// modItem is one open item as the queue page shows it.
type modItem struct {
	ID          int64  `db:"id"`
	Kind        string `db:"kind"`
	Subject     string `db:"subject"`
	SubjectRef  string `db:"subject_ref"`
	Reason      string `db:"reason"`
	ReportedBy  string `db:"reported_by_name"`
	Created     string `db:"created"`
	Removes     int    `db:"removes"`
	Keeps       int    `db:"keeps"`
	Resolution  string `db:"resolution"`
	ResolvedBy  string `db:"resolved_by_name"`
	Resolved    string `db:"resolved"`
	YouVoted    bool   `db:"-"`
	YouedRemove bool   `db:"-"`
	// IsSubject hides the controls from the person being voted on.
	IsSubject bool `db:"-"`
	// Needed is how many more votes before sentiment counts, so the page can
	// say "3 more votes" instead of leaving people guessing why nothing
	// happened.
	Needed int `db:"-"`
}

// reportAvatar opens an item, or records a remove vote when one is already
// open.
//
// Returns whether this call opened a NEW item, so the caller can say "reported"
// or "your report was added to one already open" — a member who reports
// something and is told nothing reports it again.
func (w *web) reportAvatar(ctx context.Context, subjectID, reporterID int64, reason string) (opened bool, err error) {
	if !w.db().Valid() {
		return false, errors.New("moderation is not available")
	}
	if subjectID == reporterID {
		return false, errors.New("you cannot report your own avatar")
	}
	avatar := readAvatarPath(ctx, w.data.DB(), subjectID)
	if avatar == "" {
		return false, errors.New("that member has no avatar")
	}
	reason = strings.TrimSpace(reason)
	if len(reason) > 300 {
		reason = reason[:300]
	}

	var id int64
	err = w.db().GetContext(ctx, &id, `
		INSERT INTO moderation_items (kind, subject_user_id, subject_ref, reason, reported_by)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT DO NOTHING
		RETURNING id`, itemKindAvatar, subjectID, avatar, reason, reporterID)
	switch {
	case err == nil:
		// A new item. The reporter's own opinion is already on the record, so
		// it is cast as a vote too — otherwise an item opens at 0-0 and the
		// person who raised it has to click again to say what they meant.
		return true, w.castVote(ctx, id, reporterID, true)
	case errors.Is(err, sql.ErrNoRows):
		// The partial unique index rejected it: one is already open.
		if err := w.db().GetContext(ctx, &id, `
			SELECT id FROM moderation_items
			 WHERE kind = $1 AND subject_user_id = $2 AND resolved_at IS NULL`,
			itemKindAvatar, subjectID); err != nil {
			return false, err
		}
		return false, w.castVote(ctx, id, reporterID, true)
	default:
		return false, err
	}
}

// castVote records one member's opinion. Re-voting CHANGES the vote rather than
// failing: somebody who clicks the wrong button should be able to say so.
func (w *web) castVote(ctx context.Context, itemID, userID int64, remove bool) error {
	_, err := w.db().ExecContext(ctx, `
		INSERT INTO moderation_votes (item_id, user_id, remove)
		VALUES ($1, $2, $3)
		ON CONFLICT (item_id, user_id) DO UPDATE SET remove = EXCLUDED.remove, created_at = now()`,
		itemID, userID, remove)
	return err
}

// tally is the vote count for one item.
type tally struct {
	Removes int `db:"removes"`
	Keeps   int `db:"keeps"`
}

// decide applies community sentiment if the item has reached quorum.
//
// Called after every vote. Synchronous on purpose: a queue that decides on a
// timer leaves a window where the page says the vote passed and the avatar is
// still there, and that window is exactly when somebody looks.
func (w *web) decide(ctx context.Context, itemID int64) error {
	var t tally
	if err := w.db().GetContext(ctx, &t, `
		SELECT count(*) FILTER (WHERE remove)     AS removes,
		       count(*) FILTER (WHERE NOT remove) AS keeps
		  FROM moderation_votes WHERE item_id = $1`, itemID); err != nil {
		return err
	}
	total := t.Removes + t.Keeps
	if total < voteQuorum {
		return nil
	}
	switch {
	case t.Removes*voteMajorityDen >= total*voteMajorityNum:
		return w.resolveItem(ctx, itemID, resolutionRemoved, 0)
	case t.Keeps*voteMajorityDen >= total*voteMajorityNum:
		return w.resolveItem(ctx, itemID, resolutionKept, 0)
	}
	// Quorum reached, no majority either way. Left open deliberately: a split
	// community is a real answer, and the item stays for staff or for more
	// votes rather than defaulting to whichever side is cheaper.
	return nil
}

// resolveItem closes an item and carries out what was decided.
//
// actorID is 0 when the community decided it, which is why resolved_by is
// nullable — "who did this" and "nobody, the vote did" are different answers
// and both need to be recordable.
func (w *web) resolveItem(ctx context.Context, itemID int64, resolution string, actorID int64) error {
	var it struct {
		Kind    string `db:"kind"`
		Subject int64  `db:"subject_user_id"`
	}
	if err := w.db().GetContext(ctx, &it,
		`SELECT kind, subject_user_id FROM moderation_items WHERE id = $1 AND resolved_at IS NULL`,
		itemID); err != nil {
		// Already resolved, or gone. Not an error: two votes landing at once
		// both call this and the second has nothing to do.
		return nil
	}
	if resolution == resolutionRemoved {
		if err := w.applyResolution(ctx, it.Kind, it.Subject); err != nil {
			return err
		}
	}
	var by any
	if actorID > 0 {
		by = actorID
	}
	_, err := w.db().ExecContext(ctx, `
		UPDATE moderation_items
		   SET resolved_at = now(), resolution = $1, resolved_by = $2
		 WHERE id = $3 AND resolved_at IS NULL`, resolution, by, itemID)
	return err
}

// applyResolution carries out a removal for one kind. The ONLY place that knows
// what an item actually is — a second kind adds a case here and nothing else.
func (w *web) applyResolution(ctx context.Context, kind string, subjectID int64) error {
	switch kind {
	case itemKindAvatar:
		// The undo token is discarded: this is a decision the community made,
		// not a slip, and an undo offered to nobody in particular is an undo
		// nobody can use. The subject can upload a new picture.
		_, err := w.clearAvatar(ctx, w.data.DB(), subjectID)
		return err
	}
	// An unknown kind must not be silently treated as done: that would close
	// the item and leave the thing it was about untouched.
	return errors.New("moderation: no removal handler for kind " + kind)
}

// listModItems returns the open queue, or the decided history.
func (w *web) listModItems(ctx context.Context, viewerID int64, history bool) []modItem {
	if !w.db().Valid() {
		return nil
	}
	// Typed SQL so the concatenation below type-checks — and so a future
	// edit that put a request value here would fail to compile.
	where, order := storage.SQL(`i.resolved_at IS NULL`), storage.SQL(`i.created_at DESC`)
	if history {
		where, order = `i.resolved_at IS NOT NULL`, `i.resolved_at DESC`
	}
	var rows []modItem
	if err := w.db().SelectContext(ctx, &rows, `
		SELECT i.id, i.kind, i.subject_ref, i.reason,
		       s.username                                   AS subject,
		       COALESCE(r.username, '')                     AS reported_by_name,
		       to_char(i.created_at, 'DD Mon YYYY HH24:MI') AS created,
		       COALESCE(i.resolution, '')                   AS resolution,
		       COALESCE(a.username, '')                     AS resolved_by_name,
		       COALESCE(to_char(i.resolved_at, 'DD Mon YYYY HH24:MI'), '') AS resolved,
		       (SELECT count(*) FROM moderation_votes v WHERE v.item_id = i.id AND v.remove)     AS removes,
		       (SELECT count(*) FROM moderation_votes v WHERE v.item_id = i.id AND NOT v.remove) AS keeps
		  FROM moderation_items i
		  JOIN users s ON s.id = i.subject_user_id
		  LEFT JOIN users r ON r.id = i.reported_by
		  LEFT JOIN users a ON a.id = i.resolved_by
		 WHERE `+where+`
		 ORDER BY `+order+`
		 LIMIT 100`); err != nil {
		slog.Error("community moderation read", "history", history, "err", err)
		return nil
	}
	if len(rows) == 0 {
		return rows
	}

	// The viewer's own votes, in one query rather than one per row.
	mine := map[int64]bool{}
	var votes []struct {
		ItemID int64 `db:"item_id"`
		Remove bool  `db:"remove"`
	}
	if viewerID > 0 {
		_ = w.db().SelectContext(ctx, &votes,
			`SELECT item_id, remove FROM moderation_votes WHERE user_id = $1`, viewerID)
	}
	for _, v := range votes {
		mine[v.ItemID] = v.Remove
	}
	for i := range rows {
		r, voted := mine[rows[i].ID]
		rows[i].YouVoted, rows[i].YouedRemove = voted, r
		if n := voteQuorum - (rows[i].Removes + rows[i].Keeps); n > 0 {
			rows[i].Needed = n
		}
	}
	return rows
}

// communityModPage serves GET /moderation.
func (w *web) communityModPage(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	history := c.Query("decided") == "1"
	items := w.listModItems(c.Request.Context(), u.ID, history)
	// Marked here rather than in SQL so the query stays viewer-independent and
	// cacheable later.
	for i := range items {
		items[i].IsSubject = items[i].Subject == u.Username
	}
	w.render(c, "moderation_community.html", map[string]any{
		"Title":   "Community moderation",
		"Items":   items,
		"History": history,
		"Quorum":  voteQuorum,
		"IsStaff": u.AtLeast(core.RoleMod),
		"Done":    c.Query("done"),
		"Err":     c.Query("err"),
	})
}

// communityModVote serves POST /moderation/vote.
func (w *web) communityModVote(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	id, err := strconv.ParseInt(c.PostForm("id"), 10, 64)
	if err != nil || id <= 0 {
		c.Redirect(http.StatusFound, "/moderation")
		return
	}

	// Staff may close an item outright. Not a bigger vote — a different act,
	// and recorded as one: resolved_by carries their id where a community
	// decision leaves it null.
	if act := c.PostForm("staff"); act != "" && u.AtLeast(core.RoleMod) {
		res := resolutionKept
		if act == "remove" {
			res = resolutionRemoved
		}
		if err := w.resolveItem(ctx, id, res, u.ID); err != nil {
			w.log.Error("staff resolve moderation item", "item", id, "err", err)
			c.Redirect(http.StatusFound, "/moderation?err=could+not+resolve+that")
			return
		}
		w.log.Info("moderation item resolved by staff", "item", id, "resolution", res, "actor", u.Username)
		c.Redirect(http.StatusFound, "/moderation?done="+res)
		return
	}

	// The subject does not vote on themselves. Enforced here as well as hidden
	// in the template, because a hidden control is not an absent one.
	subject, openItem := w.data.ModerationSubject(ctx, id)
	if !openItem {
		c.Redirect(http.StatusFound, "/moderation")
		return
	}
	if subject == u.ID {
		c.Redirect(http.StatusFound, "/moderation?err=you+cannot+vote+on+your+own+avatar")
		return
	}
	if err := w.castVote(ctx, id, u.ID, c.PostForm("vote") == "remove"); err != nil {
		w.log.Error("cast moderation vote", "item", id, "err", err)
		c.Redirect(http.StatusFound, "/moderation?err=could+not+record+your+vote")
		return
	}
	if err := w.decide(ctx, id); err != nil {
		w.log.Error("apply moderation decision", "item", id, "err", err)
	}
	c.Redirect(http.StatusFound, "/moderation?done=voted")
}

// reportAvatarPost serves POST /u/:name/report-avatar.
func (w *web) reportAvatarPost(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	name := c.Param("name")
	subject, err := w.store.ByUsername(c.Request.Context(), name)
	if err != nil || subject == nil {
		c.Status(http.StatusNotFound)
		return
	}
	opened, err := w.reportAvatar(c.Request.Context(), subject.ID, u.ID, c.PostForm("reason"))
	if err != nil {
		c.Redirect(http.StatusSeeOther, "/u/"+name+"?report="+url.QueryEscape(err.Error()))
		return
	}
	w.log.Info("avatar reported", "subject", subject.ID, "by", u.ID, "opened", opened)
	if opened {
		c.Redirect(http.StatusSeeOther, "/u/"+name+"?report=opened")
		return
	}
	c.Redirect(http.StatusSeeOther, "/u/"+name+"?report=added")
}
