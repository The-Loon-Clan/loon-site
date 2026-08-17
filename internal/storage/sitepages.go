package storage

import (
	"context"
	"time"
)

// Editable site pages — the prose pages an operator owns (/faq, /rules,
// /about, and any page they add). A row REPLACES the built-in template for
// its slug; no row means the built-in still serves, which is why there is no
// seed here: the templates are the seed, and they keep working on a fresh
// database with nothing in this table.

// SitePage is one page.
type SitePage struct {
	Slug      string    `db:"slug"`
	Title     string    `db:"title"`
	BodyMD    string    `db:"body_md"`
	UpdatedAt time.Time `db:"updated_at"`
}

// MigrateSitePages creates the table. Idempotent, unconditional — the
// site_settings lesson again.
func (st *Store) MigrateSitePages() error {
	_, err := st.db.Exec(`CREATE TABLE IF NOT EXISTS site_pages (
	    slug       TEXT PRIMARY KEY,
	    title      TEXT NOT NULL,
	    body_md    TEXT NOT NULL DEFAULT '',
	    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`)
	return err
}

// ListSitePages returns every page, slug-ordered for a stable admin list.
func (st *Store) ListSitePages(ctx context.Context) ([]SitePage, error) {
	var out []SitePage
	err := st.db.SelectContext(ctx, &out,
		`SELECT slug, title, body_md, updated_at FROM site_pages ORDER BY slug`)
	return out, err
}

// GetSitePage reads one page. ok=false covers both "no row" and "table
// unreachable" — either way the caller falls back to the built-in template,
// which is the only honest thing a page request can do with a broken table.
func (st *Store) GetSitePage(ctx context.Context, slug string) (SitePage, bool) {
	var p SitePage
	if err := st.db.GetContext(ctx, &p,
		`SELECT slug, title, body_md, updated_at FROM site_pages WHERE slug = $1`, slug); err != nil {
		return SitePage{}, false
	}
	return p, true
}

// UpsertSitePage writes one page.
func (st *Store) UpsertSitePage(ctx context.Context, slug, title, bodyMD string) error {
	_, err := st.db.ExecContext(ctx, `
		INSERT INTO site_pages (slug, title, body_md, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (slug) DO UPDATE SET title = EXCLUDED.title,
		    body_md = EXCLUDED.body_md, updated_at = now()`,
		slug, title, bodyMD)
	return err
}

// DeleteSitePage removes one page. For a built-in slug this is REVERT, not
// removal — the template serves again — which the admin page says in as many
// words before offering the button.
func (st *Store) DeleteSitePage(ctx context.Context, slug string) error {
	_, err := st.db.ExecContext(ctx, `DELETE FROM site_pages WHERE slug = $1`, slug)
	return err
}
