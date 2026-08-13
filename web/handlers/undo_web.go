package handlers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
)

// Undo, for the actions that cannot be taken back by doing them again.
//
// ui-patterns argues undo beats confirmation, and the reason is that a
// confirmation dialogue is answered reflexively after the third time — it
// trains the click it exists to prevent. Undo asks nothing up front and is
// there when the mistake is actually noticed, which is a second after it
// happened, not before.
//
// DELIBERATELY NARROW. Bookmarks and follows are toggles: the control that
// removed it puts it back, in the same place, with the same click. Wrapping
// those in undo records would add a table, a token and an expiry to something
// the interface already reverses — machinery that makes the code longer and
// the site no more forgiving.
//
// What is left is the short list of things that genuinely cannot be undone
// from the page you are on:
//
//	avatar.cleared   the picture is gone and the file with it
//	bio.replaced     the old text is not anywhere
//
// Both are host-owned. Plugin-owned destructive actions (deleting a post,
// leaving a community) would need the same treatment inside those plugins;
// this is the host's half and the mechanism generalises, which is why kinds
// dispatch through a map rather than a switch nobody can extend from outside.

// undoWindow is how long an action stays reversible.
//
// Longer than a toast, because this site has no toast: the offer lives in a
// notice on the page you land on, and a person who navigates away, notices in
// the next screen, and comes back should still find it. Short enough that
// "undo" is about a mistake rather than about changing your mind next week —
// and, for avatars, short enough that the deleted file is not kept around
// indefinitely waiting for a change of heart.
const undoWindow = 15 * time.Minute

// undoMigrate creates the table.
func undoMigrate(db *sqlx.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS undo_actions (
		    token      TEXT PRIMARY KEY,
		    user_id    BIGINT NOT NULL,
		    kind       TEXT   NOT NULL,
		    -- What is needed to reverse it, shaped by the kind. JSON rather
		    -- than columns because the kinds have nothing in common: an avatar
		    -- needs a path and a bio needs a body, and a table with both would
		    -- be half null forever.
		    payload    JSONB  NOT NULL,
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    expires_at TIMESTAMPTZ NOT NULL,
		    used_at    TIMESTAMPTZ
		)`,
		`CREATE INDEX IF NOT EXISTS undo_actions_expiry ON undo_actions (expires_at)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// newUndoToken returns an unguessable handle.
//
// Random, not the row id. An undo endpoint keyed on a sequential id lets
// anybody walk it and reverse other people's actions — the user check below
// stops that too, but a token that cannot be guessed means the check is the
// second line of defence rather than the only one.
func newUndoToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// recordUndo stores how to reverse something and returns the token to offer.
//
// Best effort by design: the caller has ALREADY done the destructive thing, and
// failing here must not fail their request. An action that happened without an
// undo record is a worse outcome than one that happened with one, but it is a
// far better outcome than an error page after the change already landed.
func (w *web) recordUndo(ctx context.Context, userID int64, kind string, payload any) string {
	if w.db() == nil || userID <= 0 {
		return ""
	}
	blob, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	token, err := newUndoToken()
	if err != nil {
		return ""
	}
	if _, err := w.db().ExecContext(ctx, `
		INSERT INTO undo_actions (token, user_id, kind, payload, expires_at)
		VALUES ($1, $2, $3, $4, now() + $5::interval)`,
		token, userID, kind, blob, undoWindow.String()); err != nil {
		return ""
	}
	return token
}

// undoKinds maps a kind to the code that reverses it. A map rather than a
// switch so a new kind is a registration next to the thing it undoes, instead
// of an edit to a function in another file that its author has no reason to
// open.
var undoKinds = map[string]func(ctx context.Context, db *sqlx.DB, userID int64, payload []byte) error{}

// registerUndo wires one kind. Called from init() next to the action itself.
// The handler receives the database handle rather than reaching for a
// package global. Undo handlers are registered from init(), where there is no
// server to hang a method off, which is exactly why the global existed — and
// exactly the sort of hidden dependency this refactor is removing. Passing it
// at call time keeps registration free of any handle at all.
func registerUndo(kind string, fn func(ctx context.Context, db *sqlx.DB, userID int64, payload []byte) error) {
	undoKinds[kind] = fn
}

// errUndoGone covers every "you cannot undo this" case with ONE message.
//
// Expired, already used, someone else's, or never existed — all the same
// sentence on purpose. Distinguishing them tells a stranger with a guessed
// token whether it was real, and none of the four differ in what the person
// looking at the page should now do.
var errUndoGone = errors.New("that can no longer be undone")

// performUndo reverses one recorded action.
func (w *web) performUndo(ctx context.Context, userID int64, token string) (string, error) {
	if w.db() == nil || token == "" || userID <= 0 {
		return "", errUndoGone
	}
	var row struct {
		Kind    string `db:"kind"`
		Payload []byte `db:"payload"`
	}
	// The UPDATE ... WHERE used_at IS NULL is what makes this single-use: two
	// clicks racing both run it and exactly one gets a row back, so an undo
	// cannot be applied twice.
	if err := w.db().GetContext(ctx, &row, `
		UPDATE undo_actions SET used_at = now()
		 WHERE token = $1 AND user_id = $2 AND used_at IS NULL AND expires_at > now()
		 RETURNING kind, payload`, token, userID); err != nil {
		return "", errUndoGone
	}
	fn, ok := undoKinds[row.Kind]
	if !ok {
		// A kind with no handler is a wiring bug of exactly the sort
		// contracts_web.go exists to report. Say so rather than silently
		// consuming the token and doing nothing.
		return "", fmt.Errorf("nothing knows how to undo %q", row.Kind)
	}
	if err := fn(ctx, w.db(), userID, row.Payload); err != nil {
		// Put the token back: the action was not reversed, so the offer should
		// still stand.
		_, _ = w.db().ExecContext(ctx,
			`UPDATE undo_actions SET used_at = NULL WHERE token = $1`, token)
		return "", err
	}
	return row.Kind, nil
}

// purgeUndo drops expired records. Called by the sweep job (avatarsweep_web.go)
// rather than on a timer of its own — one caretaker, not two.
func (w *web) purgeUndo(ctx context.Context) (int64, error) {
	if w.db() == nil {
		return 0, nil
	}
	res, err := w.db().ExecContext(ctx,
		`DELETE FROM undo_actions WHERE expires_at < now() - interval '1 day'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// undoPost serves POST /undo.
//
// Redirects back where it came from, because undo is always something you do
// FROM a page and expect to stay on. The next parameter is validated as a
// site-relative path: an open redirect on a POST that also changes state is
// worth more to an attacker than either alone.
func (w *web) undoPost(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	back := c.PostForm("next")
	if len(back) < 2 || back[0] != '/' || back[1] == '/' {
		back = "/"
	}
	sep := "?"
	if idx := len(back); idx > 0 && containsByte(back, '?') {
		sep = "&"
	}
	if _, err := w.performUndo(c.Request.Context(), u.ID, c.PostForm("token")); err != nil {
		w.log.Info("undo refused", "user", u.ID, "err", err)
		c.Redirect(http.StatusSeeOther, back+sep+"undone=0")
		return
	}
	c.Redirect(http.StatusSeeOther, back+sep+"undone=1")
}

// containsByte avoids pulling in strings for one character test.
func containsByte(s string, b byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return true
		}
	}
	return false
}
