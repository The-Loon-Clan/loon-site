package storage

import "context"

// Per-tracker API keys for the private-tracker search adapters.
//
// A private tracker is only searchable with the member's own key, and this is
// where an operator's keys live. Host-owned, like every other credential the
// site holds: the search client (loon-plugins/trackersearch) is stateless
// about which trackers a site has accounts on -- that is site configuration,
// and configuration is the host's.
//
// The key is stored as given. It is the operator's own tracker API key, the
// same thing Prowlarr keeps in its config database; it is never rendered back
// to a page (the admin shows a masked hint and the last four), so it does not
// leak to a shoulder over the admin's screen. A production host behind this
// contract would encrypt at rest; the reference host does not, and says so.

// TrackerKey is one configured private tracker.
type TrackerKey struct {
	Slug    string `db:"slug"`
	APIKey  string `db:"api_key"`
	BaseURL string `db:"base_url"`
	Enabled bool   `db:"enabled"`
}

// MigrateTrackerKeys creates the table. Idempotent.
func (st *Store) MigrateTrackerKeys() error {
	_, err := st.db.Exec(`
		CREATE TABLE IF NOT EXISTS tracker_key (
			slug       TEXT PRIMARY KEY,
			api_key    TEXT NOT NULL,
			base_url   TEXT NOT NULL DEFAULT '',
			enabled    BOOLEAN NOT NULL DEFAULT TRUE,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`)
	return err
}

// TrackerKeys lists every configured tracker, enabled or not, slug order.
func (st *Store) TrackerKeys(ctx context.Context) ([]TrackerKey, error) {
	var out []TrackerKey
	err := st.db.SelectContext(ctx, &out, `
		SELECT slug, api_key, base_url, enabled
		  FROM tracker_key
		 ORDER BY slug`)
	return out, err
}

// EnabledTrackerKeys is the set the search client is configured from: only the
// enabled ones with a key, so a disabled row stops the tracker being searched
// without losing the stored key.
func (st *Store) EnabledTrackerKeys(ctx context.Context) ([]TrackerKey, error) {
	var out []TrackerKey
	err := st.db.SelectContext(ctx, &out, `
		SELECT slug, api_key, base_url, enabled
		  FROM tracker_key
		 WHERE enabled AND api_key <> ''
		 ORDER BY slug`)
	return out, err
}

// SaveTrackerKey stores or updates one tracker's key. An empty key is rejected
// by the caller; this upserts on the slug so re-saving rotates the key.
func (st *Store) SaveTrackerKey(ctx context.Context, k TrackerKey) error {
	_, err := st.db.ExecContext(ctx, `
		INSERT INTO tracker_key (slug, api_key, base_url, enabled, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (slug) DO UPDATE
		   SET api_key = EXCLUDED.api_key,
		       base_url = EXCLUDED.base_url,
		       enabled = EXCLUDED.enabled,
		       updated_at = now()`,
		k.Slug, k.APIKey, k.BaseURL, k.Enabled)
	return err
}

// SetTrackerKeyEnabled flips one tracker on or off without touching its key,
// so an operator can pause a source and resume it without re-pasting.
func (st *Store) SetTrackerKeyEnabled(ctx context.Context, slug string, enabled bool) error {
	_, err := st.db.ExecContext(ctx,
		`UPDATE tracker_key SET enabled = $2, updated_at = now() WHERE slug = $1`,
		slug, enabled)
	return err
}

// DeleteTrackerKey removes one tracker's configuration and key entirely.
func (st *Store) DeleteTrackerKey(ctx context.Context, slug string) error {
	_, err := st.db.ExecContext(ctx, `DELETE FROM tracker_key WHERE slug = $1`, slug)
	return err
}
