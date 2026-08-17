package storage

import "context"

// The editable navigation — which links the four main dropdowns carry, in
// what order, under which tab. WordPress's Menus screen at Ghost's
// complexity: one level deep, four fixed groups, rows the operator can
// relabel, reorder, move, hide, add and (for their own additions) remove.
//
// Built-in rows are ENSURED, not seeded: every boot inserts any builtin href
// that is missing and touches nothing that exists, so a new page shipped in
// an update appears in the menu of an old database while every operator edit
// — a rename, a move, a hiding — survives. The code-side default list
// (navadmin_web.go) is also the render fallback when this table cannot be
// read: a broken settings table must degrade to the shipped menu, never to a
// site with no navigation.

// NavEntry is one menu row.
type NavEntry struct {
	Href    string `db:"href"`
	Label   string `db:"label"`
	Grp     string `db:"grp"`
	Ordinal int    `db:"ordinal"`
	Hidden  bool   `db:"hidden"`
	// Builtin rows come from the shipped menu: they may be edited and hidden
	// but not removed — deleting one only means the next boot puts it back,
	// so the editor offers hiding instead.
	Builtin bool `db:"builtin"`
}

// MigrateSiteNav creates the table.
func (st *Store) MigrateSiteNav() error {
	_, err := st.db.Exec(`CREATE TABLE IF NOT EXISTS site_nav (
	    href    TEXT PRIMARY KEY,
	    label   TEXT NOT NULL,
	    grp     TEXT NOT NULL,
	    ordinal INT NOT NULL DEFAULT 0,
	    hidden  BOOLEAN NOT NULL DEFAULT FALSE,
	    builtin BOOLEAN NOT NULL DEFAULT FALSE
	)`)
	return err
}

// ListSiteNav returns every row, group-then-ordinal ordered.
func (st *Store) ListSiteNav(ctx context.Context) ([]NavEntry, error) {
	var out []NavEntry
	err := st.db.SelectContext(ctx, &out,
		`SELECT href, label, grp, ordinal, hidden, builtin
		   FROM site_nav ORDER BY grp, ordinal, label`)
	return out, err
}

// EnsureSiteNav inserts one row only if its href is absent — the builtin
// reconciliation write, and the one the pages editor uses to place a page
// without clobbering a later manual edit.
func (st *Store) EnsureSiteNav(ctx context.Context, e NavEntry) error {
	_, err := st.db.ExecContext(ctx, `
		INSERT INTO site_nav (href, label, grp, ordinal, hidden, builtin)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (href) DO NOTHING`,
		e.Href, e.Label, e.Grp, e.Ordinal, e.Hidden, e.Builtin)
	return err
}

// UpsertSiteNav writes one row outright — the editor's save.
func (st *Store) UpsertSiteNav(ctx context.Context, e NavEntry) error {
	_, err := st.db.ExecContext(ctx, `
		INSERT INTO site_nav (href, label, grp, ordinal, hidden, builtin)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (href) DO UPDATE SET label = EXCLUDED.label,
		    grp = EXCLUDED.grp, ordinal = EXCLUDED.ordinal,
		    hidden = EXCLUDED.hidden, builtin = EXCLUDED.builtin`,
		e.Href, e.Label, e.Grp, e.Ordinal, e.Hidden, e.Builtin)
	return err
}

// DeleteSiteNav removes one row. The editor only offers it for custom rows;
// a deleted builtin would simply be re-ensured at the next boot.
func (st *Store) DeleteSiteNav(ctx context.Context, href string) error {
	_, err := st.db.ExecContext(ctx, `DELETE FROM site_nav WHERE href = $1`, href)
	return err
}
