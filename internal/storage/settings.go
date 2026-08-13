package storage

import "context"

// Per-member settings: the privacy flag, the notification preferences, and the
// profile text.

// IsPrivateProfile reports whether a member has hidden their profile.
func (st *Store) IsPrivateProfile(ctx context.Context, userID int64) bool {
	var private bool
	if err := st.db.GetContext(ctx, &private,
		`SELECT COALESCE(private_profile, false) FROM users WHERE id = $1`, userID); err != nil {
		return false
	}
	return private
}

// SetPrivateProfile stores the privacy choice.
func (st *Store) SetPrivateProfile(ctx context.Context, userID int64, private bool) error {
	_, err := st.db.ExecContext(ctx,
		`UPDATE users SET private_profile = $2 WHERE id = $1`, userID, private)
	return err
}

// SetNotificationPref records one kind's on/off state.
//
// Upsert rather than insert-or-update-in-Go: the row may or may not exist and
// the caller does not care which. The caller writes EVERY known kind on save,
// because an unchecked box posts nothing at all — reading only what arrived
// would silently leave a kind the member just turned off still enabled.
func (st *Store) SetNotificationPref(ctx context.Context, userID int64, kind string, enabled bool) error {
	_, err := st.db.ExecContext(ctx,
		`INSERT INTO notification_prefs (user_id, kind, enabled) VALUES ($1,$2,$3)
		 ON CONFLICT (user_id, kind) DO UPDATE SET enabled = EXCLUDED.enabled`,
		userID, kind, enabled)
	return err
}

// ReadBio returns a member's profile text, unrendered.
//
// Markdown is rendered at READ time by the caller, not stored as HTML: the
// renderer and its sanitising policy can then change without rewriting every
// row that was saved under the old one.
func (st *Store) ReadBio(ctx context.Context, userID int64) string {
	var bio string
	if err := st.db.GetContext(ctx, &bio,
		`SELECT COALESCE(bio, '') FROM users WHERE id = $1`, userID); err != nil {
		return ""
	}
	return bio
}

// SetBio stores a member's profile text.
func (st *Store) SetBio(ctx context.Context, userID int64, bio string) error {
	_, err := st.db.ExecContext(ctx, `UPDATE users SET bio = $1 WHERE id = $2`, bio, userID)
	return err
}

// ModerationSubject returns whose account an OPEN moderation item is about.
//
// resolved_at IS NULL is part of the question, not a filter on the answer: a
// vote cast on an item somebody else has already decided must find nothing
// rather than apply to a closed case.
func (st *Store) ModerationSubject(ctx context.Context, itemID int64) (int64, bool) {
	var subject int64
	if err := st.db.GetContext(ctx, &subject,
		`SELECT subject_user_id FROM moderation_items WHERE id = $1 AND resolved_at IS NULL`,
		itemID); err != nil {
		return 0, false
	}
	return subject, true
}
