package handlers

import (
	"github.com/jmoiron/sqlx"
)

// Grab counting — the missing feature the parity list called out.
//
// Nothing recorded NZB downloads, which blocked three separate things at once:
// the economy plugin (its entire job is a per-grab uploader bonus, and
// UploaderGrabTotals had no source), UNIT3D's trending pages, and the "N
// downloads" figure every UNIT3D listing shows.
//
// Deliberately NOT mocked while it was missing — a faked download count would
// have corrupted the very features that were waiting to read it.

// grabsMigrate creates the table. One row per download, not a counter column:
// a counter cannot answer "this week", which is the question trending asks.
func grabsMigrate(db *sqlx.DB) error {
	stmts := []string{
		// user_id is NULLABLE: /nzb/:id is reachable by an anonymous visitor
		// and by an API key, and a grab still happened. Making it NOT NULL
		// would silently drop exactly the traffic a public indexer sees most.
		`CREATE TABLE IF NOT EXISTS release_grab (
		    id         BIGSERIAL PRIMARY KEY,
		    release_id BIGINT NOT NULL,
		    user_id    BIGINT REFERENCES users(id) ON DELETE SET NULL,
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		// The two questions asked of this table: "how many for this release"
		// and "what was grabbed most recently". One index each.
		`CREATE INDEX IF NOT EXISTS idx_release_grab_release ON release_grab (release_id)`,
		`CREATE INDEX IF NOT EXISTS idx_release_grab_recent ON release_grab (created_at DESC)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}
