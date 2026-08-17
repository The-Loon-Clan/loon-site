package storage

import "context"

// The message catalogue: operator-authored localized strings, keyed by a
// dotted slug and a locale key from internal/i18n. Consumed through the
// achievements.l10n.* seams (i18nadmin_web.go); owned here because the
// strings are operator content and the locale set is the host's.

// I18nMessage is one cell of the catalogue.
type I18nMessage struct {
	Slug   string `db:"slug"`
	Locale string `db:"locale"`
	Text   string `db:"text"`
}

// MigrateI18n creates the catalogue. Idempotent, like every host migration,
// and NOT behind any feature flag — the site_settings lesson: a shared table
// behind a conditional migration is a boot error waiting for a fresh database.
func (st *Store) MigrateI18n() error {
	_, err := st.db.Exec(`CREATE TABLE IF NOT EXISTS i18n_messages (
	    slug   TEXT NOT NULL,
	    locale TEXT NOT NULL,
	    text   TEXT NOT NULL DEFAULT '',
	    PRIMARY KEY (slug, locale)
	)`)
	return err
}

// ListI18nMessages returns the whole catalogue, slug-then-locale ordered so
// the admin grid is stable across loads.
func (st *Store) ListI18nMessages(ctx context.Context) ([]I18nMessage, error) {
	var out []I18nMessage
	err := st.db.SelectContext(ctx, &out,
		`SELECT slug, locale, text FROM i18n_messages ORDER BY slug, locale`)
	return out, err
}

// UpsertI18nMessage writes one cell.
func (st *Store) UpsertI18nMessage(ctx context.Context, slug, locale, text string) error {
	_, err := st.db.ExecContext(ctx, `
		INSERT INTO i18n_messages (slug, locale, text) VALUES ($1, $2, $3)
		ON CONFLICT (slug, locale) DO UPDATE SET text = EXCLUDED.text`,
		slug, locale, text)
	return err
}

// I18nSlugs lists the distinct slugs, for definition-form dropdowns.
func (st *Store) I18nSlugs(ctx context.Context) ([]string, error) {
	var out []string
	err := st.db.SelectContext(ctx, &out,
		`SELECT DISTINCT slug FROM i18n_messages ORDER BY slug`)
	return out, err
}

// ResolveI18n answers a slug in the viewer's locale, then the default locale.
// A row whose text is empty does not count as an answer — an operator who
// added a slug and has not written the Japanese yet has not translated it,
// and serving "" would blank a badge name rather than falling back.
func (st *Store) ResolveI18n(ctx context.Context, slug, locale, fallback string) (string, bool) {
	var text string
	err := st.db.GetContext(ctx, &text, `
		SELECT text FROM i18n_messages
		 WHERE slug = $1 AND text <> '' AND locale IN ($2, $3)
		 ORDER BY (locale = $2) DESC
		 LIMIT 1`, slug, locale, fallback)
	if err != nil || text == "" {
		return "", false
	}
	return text, true
}
