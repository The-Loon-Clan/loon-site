package site

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/core"
)

// User settings — UNIT3D's user/privacy-setting and user/notification-setting.
//
// Its general-setting page is mostly locale, timezone and per-page counts, none
// of which this stack supports; the one part that IS real here — the theme — is
// already a switcher in the header, and duplicating it would be two controls
// for one setting. So this covers the two pages that have something behind them.
//
// Both settings are REAL and enforced, not display toggles:
//   - private_profile is checked by profilePage before rendering a subject
//   - notification prefs are checked in the delivery path, so a disabled kind
//     is never written to the inbox at all

// notifiableKinds is every notification kind this host actually emits, with the
// label its toggle carries. A kind NOT in this list is still delivered — see
// prefFilter — because an unknown kind means "the list is out of date", and
// silence is the wrong way to discover that.
var notifiableKinds = []struct{ Kind, Label, Help string }{
	{"ticket_created", "Ticket received", "When your support ticket is filed."},
	{"ticket_reply", "Ticket replies", "When someone replies to your ticket."},
	{"forum_quote", "Forum quotes", "When someone quotes your forum post."},
	{"forum_reply", "Forum replies", "When someone replies in a thread you started."},
}

// settingsMigrate adds the privacy column and the preference table.
func settingsMigrate(db *sqlx.DB) error {
	stmts := []string{
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS private_profile BOOLEAN NOT NULL DEFAULT false`,
		// A row per DISABLED kind would be smaller, but an explicit enabled
		// flag survives the default changing: if this host ever flips a kind to
		// off-by-default, existing opt-ins stay opted in.
		`CREATE TABLE IF NOT EXISTS notification_prefs (
		    user_id BIGINT  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    kind    TEXT    NOT NULL,
		    enabled BOOLEAN NOT NULL DEFAULT true,
		    PRIMARY KEY (user_id, kind)
		)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// prefFiltered wraps the host's NotifyFn and drops kinds a recipient turned
// off. Wrapped ONCE at the delivery entry point rather than checked at every
// call site: a preference enforced in one place cannot be forgotten by the next
// plugin that sends something.
//
// Fails OPEN. A missing row, an unknown kind, or a failed lookup all deliver:
// the cost of an unwanted notification is mild annoyance, and the cost of a
// silent drop is a user never learning their ticket was answered.
func prefFiltered(db *sqlx.DB, next func(context.Context, int64, core.Notification) error) func(context.Context, int64, core.Notification) error {
	return func(ctx context.Context, userID int64, n core.Notification) error {
		if db != nil && n.Kind != "" {
			var enabled bool
			err := db.GetContext(ctx, &enabled,
				`SELECT enabled FROM notification_prefs WHERE user_id = $1 AND kind = $2`,
				userID, n.Kind)
			// Only an explicit, successfully-read false suppresses. No row
			// (never configured) and any error both fall through to delivery.
			if err == nil && !enabled {
				return nil
			}
		}
		return next(ctx, userID, n)
	}
}

// notificationPrefs reads a viewer's toggles, defaulting anything unset to on.
func notificationPrefs(ctx context.Context, db *sqlx.DB, userID int64) map[string]bool {
	out := make(map[string]bool, len(notifiableKinds))
	for _, k := range notifiableKinds {
		out[k.Kind] = true
	}
	if db == nil {
		return out
	}
	var rows []struct {
		Kind    string `db:"kind"`
		Enabled bool   `db:"enabled"`
	}
	if err := db.SelectContext(ctx, &rows,
		`SELECT kind, enabled FROM notification_prefs WHERE user_id = $1`, userID); err != nil {
		return out
	}
	for _, r := range rows {
		out[r.Kind] = r.Enabled
	}
	return out
}

// isPrivateProfile reports whether a subject has hidden their profile.
func isPrivateProfile(ctx context.Context, userID int64) bool {
	if usersDB == nil {
		return false
	}
	var private bool
	if err := usersDB.GetContext(ctx, &private,
		`SELECT COALESCE(private_profile, false) FROM users WHERE id = $1`, userID); err != nil {
		return false
	}
	return private
}

// ── pages ───────────────────────────────────────────────────────────

func (w *web) settingsPrivacy(c *gin.Context) {
	u, ok := w.currentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	w.render(c, "settings_privacy.html", map[string]any{
		"Title":          "Privacy",
		"PrivateProfile": isPrivateProfile(c.Request.Context(), u.ID),
		"Saved":          c.Query("saved") == "1",
	})
}

func (w *web) settingsPrivacySave(c *gin.Context) {
	u, ok := w.currentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	private := c.PostForm("private_profile") == "1"
	if usersDB != nil {
		if _, err := usersDB.ExecContext(c.Request.Context(),
			`UPDATE users SET private_profile = $2 WHERE id = $1`, u.ID, private); err != nil {
			w.log.Error("privacy save", "err", err)
		}
	}
	c.Redirect(http.StatusFound, "/settings/privacy?saved=1")
}

func (w *web) settingsNotifications(c *gin.Context) {
	u, ok := w.currentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	prefs := notificationPrefs(c.Request.Context(), usersDB, u.ID)
	type kindVM struct {
		Kind, Label, Help string
		Enabled           bool
	}
	rows := make([]kindVM, 0, len(notifiableKinds))
	for _, k := range notifiableKinds {
		rows = append(rows, kindVM{k.Kind, k.Label, k.Help, prefs[k.Kind]})
	}
	w.render(c, "settings_notifications.html", map[string]any{
		"Title": "Notifications",
		"Kinds": rows,
		"Saved": c.Query("saved") == "1",
	})
}

func (w *web) settingsNotificationsSave(c *gin.Context) {
	u, ok := w.currentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	ctx := c.Request.Context()
	// An unchecked box posts NOTHING, so every known kind must be written
	// explicitly — reading only what was posted would silently leave a kind the
	// user just turned off still enabled.
	for _, k := range notifiableKinds {
		enabled := c.PostForm(k.Kind) == "1"
		if usersDB == nil {
			continue
		}
		if _, err := usersDB.ExecContext(ctx,
			`INSERT INTO notification_prefs (user_id, kind, enabled) VALUES ($1,$2,$3)
			 ON CONFLICT (user_id, kind) DO UPDATE SET enabled = EXCLUDED.enabled`,
			u.ID, k.Kind, enabled); err != nil {
			w.log.Error("notification pref save", "kind", k.Kind, "err", err)
		}
	}
	c.Redirect(http.StatusFound, "/settings/notifications?saved=1")
}

// mountSettings wires the settings pages. Separate from mountSitePages because
// these are all viewer-scoped and POST.
func (w *web) mountSettings(e *gin.Engine) {
	e.GET("/settings/privacy", w.settingsPrivacy)
	e.POST("/settings/privacy", w.settingsPrivacySave)
	e.GET("/settings/notifications", w.settingsNotifications)
	e.POST("/settings/notifications", w.settingsNotificationsSave)
	// The profile's free-text block (profilebio_web.go). A host page rather
	// than part of the account plugin's form: the text is rendered by this
	// site's markdown pipeline, so the editor belongs where that lives.
	e.GET("/settings/profile", w.settingsProfile)
	e.POST("/settings/profile", w.settingsProfileSave)
	// Its own route rather than a mode flag on the one above: the avatar form
	// is multipart and the bio form is not, and merging them would re-post the
	// image on every text save. See avatar_web.go.
	e.POST("/settings/avatar", w.settingsAvatarSave)
	// Second factor + email change (security_web.go).
	e.GET("/settings/security", w.securityPage)
	e.POST("/settings/security", w.securityAction)
	e.GET("/settings/email/confirm", w.emailConfirm)
}
