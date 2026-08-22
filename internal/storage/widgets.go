package storage

import "context"

// Widget placement: which operator-placed widgets sit in which region, in what
// order, switched on or off, with what setting.
//
// The reads and the four mutations were spread between widgets_web.go and
// widgetsadmin_web.go, interleaved with the HTTP that triggers them. The
// decisions they encode — append at the end, toggle rather than delete, store
// a widget's setting verbatim — are about the DATA, and belong where the SQL
// they are implemented in lives.

// WidgetPlacement is one arranged widget.
type WidgetPlacement struct {
	Region   string `db:"region"`
	Slug     string `db:"slug"`
	Position int    `db:"position"`
	Enabled  bool   `db:"enabled"`
	Config   string `db:"config"`
	// Pages restricts this placement to some of the site. Empty is every
	// page — the behaviour before the column existed, and what every row
	// written before it still means. See handlers.pagesMatch for the rules.
	Pages string `db:"pages"`
}

// SetWidgetPages restricts one placement to some of the site, or to all of
// it when the rule is empty.
//
// Its own mutation rather than part of ConfigureWidget: config is the
// WIDGET's opaque setting, which the host must not parse, and this is the
// HOST's rule about where the widget goes. Overloading one column with both
// would mean the host reading a string it has promised not to read.
func (st *Store) SetWidgetPages(ctx context.Context, region, slug, pages string) error {
	_, err := st.db.ExecContext(ctx,
		`UPDATE widget_placement SET pages = $3 WHERE region = $1 AND slug = $2`,
		region, slug, pages)
	return err
}

// ReadPlacements returns a region's arrangement, in order.
func (st *Store) ReadPlacements(ctx context.Context, region string) []WidgetPlacement {
	var rows []WidgetPlacement
	if err := st.db.SelectContext(ctx, &rows,
		`SELECT region, slug, position, enabled, config, pages FROM widget_placement
		  WHERE region = $1 ORDER BY position, slug`, region); err != nil {
		return nil
	}
	return rows
}

// ReadAllPlacements returns every arrangement, grouped by region.
//
// One query rather than one per region. The chrome renders four regions on
// every page — header bar, both sidebars, footer — and on a site that has
// placed nothing, four queries per page view to learn that four times over is
// a cost with no benefit. The whole table is tiny by construction: it holds one
// row per placed widget, not per member or per release.
func (st *Store) ReadAllPlacements(ctx context.Context) map[string][]WidgetPlacement {
	var rows []WidgetPlacement
	if err := st.db.SelectContext(ctx, &rows,
		`SELECT region, slug, position, enabled, config, pages FROM widget_placement
		  ORDER BY region, position, slug`); err != nil {
		return nil
	}
	if len(rows) == 0 {
		return nil
	}
	// Sized from the rows, not from the region list: how many regions the
	// EDITOR offers is a presentation decision and does not belong here. It
	// was only ever a capacity hint.
	out := make(map[string][]WidgetPlacement, len(rows))
	for _, r := range rows {
		out[r.Region] = append(out[r.Region], r)
	}
	return out
}

// PlaceWidget appends a widget to a region.
//
// Position is the count of what is already there, computed in the statement so
// two operators adding at once cannot both claim the same slot. ON CONFLICT DO
// NOTHING makes a double-submit idempotent rather than moving a widget nobody
// touched.
func (st *Store) PlaceWidget(ctx context.Context, region, slug string) error {
	_, err := st.db.ExecContext(ctx,
		`INSERT INTO widget_placement (region, slug, position, enabled)
		 VALUES ($1, $2, coalesce((SELECT max(position)+1 FROM widget_placement WHERE region=$1), 0), TRUE)
		 ON CONFLICT (region, slug) DO NOTHING`, region, slug)
	return err
}

// RemoveWidget takes a widget out of a region entirely.
func (st *Store) RemoveWidget(ctx context.Context, region, slug string) error {
	_, err := st.db.ExecContext(ctx,
		`DELETE FROM widget_placement WHERE region=$1 AND slug=$2`, region, slug)
	return err
}

// ToggleWidget switches a placement on or off WITHOUT removing it.
//
// Off rather than deleted keeps the position, so switching a widget back on
// puts it where it was instead of at the bottom.
func (st *Store) ToggleWidget(ctx context.Context, region, slug string) error {
	_, err := st.db.ExecContext(ctx,
		`UPDATE widget_placement SET enabled = NOT enabled WHERE region=$1 AND slug=$2`, region, slug)
	return err
}

// ConfigureWidget stores one placement's setting, verbatim.
//
// A widget decides what its own string means; the host escaping or parsing it
// here would break every widget whose value is not what the host guessed.
// Whatever a widget does with it must be safe at RENDER — see the markdown
// widget, which runs the site's sanitising renderer.
func (st *Store) ConfigureWidget(ctx context.Context, region, slug, config string) error {
	_, err := st.db.ExecContext(ctx,
		`UPDATE widget_placement SET config=$3 WHERE region=$1 AND slug=$2`, region, slug, config)
	return err
}

// ReorderWidgets rewrites a region's positions densely, in the order given.
//
// Positions drift: removing the middle of three leaves 0 and 2, and swapping
// stored values would then be a no-op or a jump. Renumbering the whole region
// is cheap on a table this size and removes a class of "the button did
// nothing". One transaction, so a half-renumbered region cannot be observed.
func (st *Store) ReorderWidgets(ctx context.Context, region string, slugs []string) error {
	tx, err := st.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for i, slug := range slugs {
		if _, err := tx.ExecContext(ctx,
			`UPDATE widget_placement SET position=$3 WHERE region=$1 AND slug=$2`,
			region, slug, i); err != nil {
			return err
		}
	}
	return tx.Commit()
}
