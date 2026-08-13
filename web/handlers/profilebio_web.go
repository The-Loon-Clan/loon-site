package handlers

import (
	"github.com/the-loon-clan/loon-site/internal/markdown"

	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
)

// The profile's free-text block — EMP's user-written profile, the thing that
// makes a profile someone's rather than a readout.
//
// MARKDOWN, not BBCode. EMP uses BBCode and that is what was asked for first,
// but this site already renders markdown everywhere a member writes prose (the
// forum, the wiki, communities) through one goldmark pipeline with raw HTML off
// and an allowlist sanitizer behind it, plus a prose editor with a toolbar.
// BBCode would mean a second parser and a second sanitizer for one field, and
// two dialects a member has to keep straight depending on which box they are
// typing into.
//
// The column lives on users because it IS the user, and the host owns that
// table — the same place messages_web.go added avatar_path.

// bioMaxLen caps what is stored. Long enough for a real profile, short enough
// that it cannot be used as free hosting: the profile is a page every visitor
// loads, and an unbounded field is rendered on every view.
const bioMaxLen = 4000

// migrateProfileBio adds the column. IF NOT EXISTS so it is safe on every boot,
// the same shape messages_web.go used to add avatar_path.
func migrateProfileBio(db *sqlx.DB) error {
	_, err := db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS bio TEXT NOT NULL DEFAULT ''`)
	return err
}

// readBio returns a member's raw markdown, or "" when unset or unavailable.
// Best effort: a profile must still render when this read fails.
func readBio(ctx context.Context, userID int64) string {
	if usersDB == nil || userID <= 0 {
		return ""
	}
	var bio string
	if err := usersDB.GetContext(ctx, &bio,
		`SELECT COALESCE(bio, '') FROM users WHERE id = $1`, userID); err != nil {
		return ""
	}
	return bio
}

// renderBio turns stored markdown into safe HTML for the profile.
//
// Rendered at READ time rather than stored as HTML, so a change to the
// sanitizer applies to everything already written — storing rendered output
// freezes every post at the rules that were in force the day it was saved.
func renderBio(src string) template.HTML {
	src = strings.TrimSpace(src)
	if src == "" {
		return ""
	}
	return markdown.Render(src)
}

// undoKindBio is the kind recorded when profile text is replaced.
const undoKindBio = "bio.replaced"

func init() {
	registerUndo(undoKindBio, func(ctx context.Context, userID int64, payload []byte) error {
		var p struct {
			Previous string `json:"previous"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return errUndoGone
		}
		if _, err := usersDB.ExecContext(ctx,
			`UPDATE users SET bio = $1 WHERE id = $2`, p.Previous, userID); err != nil {
			return fmt.Errorf("could not restore your profile text")
		}
		return nil
	})
}

// settingsProfile serves GET /settings/profile.
func (w *web) settingsProfile(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	bio := readBio(c.Request.Context(), u.ID)
	w.render(c, "settings_profile.html", map[string]any{
		"Title": "About you",
		"Bio":   bio,
		// Value pre-fills the textarea. Without it the form loads empty and
		// saving silently WIPES whatever was there — the editor posts what it
		// shows.
		"Editor": w.renderEditor(map[string]any{
			"Name": "bio", "Rows": 12, "Value": bio,
			// Names the control for a screen reader; the visible heading above
			// the panel is not associated with the field.
			"Label":       "About you",
			"Placeholder": "Say something about yourself…",
		}),
		"Max":   bioMaxLen,
		"Saved": c.Query("saved") == "1",
		// The avatar half of this page (avatar_web.go). Its own status keys so
		// saving text does not report "avatar saved", and vice versa.
		"Avatar": readAvatarPath(c.Request.Context(), usersDB, u.ID),
		// The initials fallback needs a name, and the partial takes it as a
		// value rather than reading .User itself so it works on any page.
		"Username":    u.Username,
		"AvatarSaved": c.Query("avatar") == "saved",
		"AvatarGone":  c.Query("avatar") == "removed",
		"AvatarErr":   c.Query("averr"),
		// One token key for the page: only one destructive action can have just
		// happened, and the notice that offers Undo is the one being rendered.
		"UndoToken":    c.Query("undo"),
		"Undone":       c.Query("undone"),
		"AvatarMaxMB":  avatarMaxUpload >> 20,
		"AvatarPixels": avatarSize,
	})
}

// settingsProfileSave serves POST /settings/profile.
func (w *web) settingsProfileSave(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	bio := strings.TrimSpace(c.PostForm("bio"))
	// Truncated by RUNES, not bytes: cutting a multi-byte character in half
	// stores invalid UTF-8, and the first thing that breaks is the page trying
	// to display it.
	if r := []rune(bio); len(r) > bioMaxLen {
		bio = string(r[:bioMaxLen])
	}
	token := ""
	if usersDB != nil {
		// Read the old text BEFORE overwriting it. Saving an empty editor wipes
		// a profile with no warning and no copy of what was there, which is the
		// same irreversibility an avatar removal has and the reason both are
		// undoable while bookmarks and follows are not.
		previous := readBio(c.Request.Context(), u.ID)
		if _, err := usersDB.ExecContext(c.Request.Context(),
			`UPDATE users SET bio = $1 WHERE id = $2`, bio, u.ID); err != nil {
			w.log.Error("save bio", "user", u.ID, "err", err)
		} else if previous != "" && previous != bio {
			token = recordUndo(c.Request.Context(), u.ID, undoKindBio,
				map[string]string{"previous": previous})
		}
	}
	c.Redirect(http.StatusFound, "/settings/profile?saved=1&undo="+url.QueryEscape(token))
}
