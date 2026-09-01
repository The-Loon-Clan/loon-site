package storage

import (
	"context"
	"time"
)

// Reading the swarm so seeding can be paid for.
//
// The rows are the TRACKER PLUGIN'S (tracker.torrents, tracker.user_stats) and
// this host only reads them. That coupling is deliberate but it is not free, so
// it is confined to this file: one query, named columns, no writes. A seam on
// the plugin side would be better and is filed as a request; until it exists,
// a host that already runs the plugin's migrations reading two of its columns
// is a smaller compromise than a second copy of the swarm.
//
// EVERY CALLER MUST BE BEHIND flavourTracker(). On an indexer-flavoured site
// the tracker plugin never boots, so this schema does not exist at all -- not
// empty, absent -- and a query against it is an error rather than zero rows.
// TrackerSwarmReady answers that question without one.

// SeedRow is one member seeding one torrent, with what the payout needs to
// know about both.
type SeedRow struct {
	UserID   int64  `db:"user_id"`
	InfoHash string `db:"info_hash"`
	// SizeBytes is the torrent's size, which both formulas scale by.
	SizeBytes int64 `db:"size_bytes"`
	// Seeders is how many members are seeding THIS torrent, counted from the
	// same rows this query returns rather than read from the denormalised
	// counter on torrents.
	//
	// That is not fussiness. The pool is divided by this number and paid to
	// these rows, so if the divisor came from a counter that had drifted, the
	// shares would not sum to the pool -- silently minting or losing points
	// every hour, in a direction nobody could see.
	Seeders int `db:"seeders"`
	// Seedtime is cumulative seconds this member has seeded this torrent,
	// which the classic formula's loyalty term reads.
	Seedtime int64 `db:"seedtime"`
}

// TrackerSwarmReady reports whether the tracker's tables exist to be read.
//
// Separate from "are there any rows": an indexer-flavoured site has no tracker
// schema, and telling that apart from a quiet swarm is the difference between
// "nothing to pay" and "this job is broken".
func (st *Store) TrackerSwarmReady(ctx context.Context) bool {
	var n int
	if err := st.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.tables
		  WHERE table_schema = 'tracker' AND table_name IN ('torrents','user_stats')`,
	).Scan(&n); err != nil {
		return false
	}
	return n == 2
}

// SeedingSnapshot returns everyone seeding right now, one row per member per
// torrent.
//
// SEEDING means left_bytes = 0 (they hold the whole thing) and an announce
// inside the freshness window. Both halves matter: a member who has the file
// but whose client is closed is not seeding it, and a stale row would draw a
// share of a pool it is not helping to serve.
func (st *Store) SeedingSnapshot(ctx context.Context, freshFor time.Duration) ([]SeedRow, error) {
	rows := []SeedRow{}
	err := st.db.SelectContext(ctx, &rows,
		`SELECT s.user_id,
		        s.info_hash,
		        t.size AS size_bytes,
		        s.seedtime,
		        COUNT(*) OVER (PARTITION BY s.info_hash) AS seeders
		   FROM tracker.user_stats s
		   JOIN tracker.torrents t ON t.info_hash = s.info_hash
		  WHERE s.left_bytes = 0
		    AND s.last_seen > now() - make_interval(secs => $1)`,
		freshFor.Seconds())
	return rows, err
}

// SeedPointsCarry is every member's unpaid fraction of a point.
//
// An hourly award is rarely a whole number, and points are integers. Dropping
// the remainder every hour is not a rounding detail: a member seeding one small
// torrent can earn 0.4 points an hour forever and be paid nothing, ever, while
// the member beside them seeding ten is paid in full. The remainder is carried
// instead, so every fraction eventually becomes a point.
func (st *Store) SeedPointsCarry(ctx context.Context) (map[int64]float64, error) {
	var rows []struct {
		UserID   int64   `db:"user_id"`
		Fraction float64 `db:"fraction"`
	}
	if err := st.db.SelectContext(ctx, &rows,
		`SELECT user_id, fraction FROM seed_points_carry`); err != nil {
		return nil, err
	}
	out := make(map[int64]float64, len(rows))
	for _, r := range rows {
		out[r.UserID] = r.Fraction
	}
	return out, nil
}

// SetSeedPointsCarry stores one member's unpaid fraction.
func (st *Store) SetSeedPointsCarry(ctx context.Context, userID int64, fraction float64) error {
	_, err := st.db.ExecContext(ctx,
		`INSERT INTO seed_points_carry (user_id, fraction) VALUES ($1, $2)
		 ON CONFLICT (user_id) DO UPDATE SET fraction = EXCLUDED.fraction`,
		userID, fraction)
	return err
}

// CreditSeedPoints pays one member and records the ledger row, in the one
// transaction the rest of this economy uses.
//
// A balance that moves without a ledger row is the inconsistency the history
// page exists to expose, so the two are never separated -- see points_web.go,
// which states the rule for the whole points system.
//
// Per member rather than one big statement on purpose: an hourly payout runs
// over everybody seeding, and one member whose row has gone (deleted account,
// mid-run) must not roll back everybody else's hour.
func (st *Store) CreditSeedPoints(ctx context.Context, userID int64, amount int, desc string) error {
	tx, err := st.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var balance int
	if err := tx.QueryRowContext(ctx,
		`UPDATE users SET points = points + $2 WHERE id = $1 RETURNING points`,
		userID, amount).Scan(&balance); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO points_ledger (user_id, amount, balance, kind, description, created_at)
		 VALUES ($1,$2,$3,'seeding',$4,$5)`,
		userID, amount, balance, desc, time.Now()); err != nil {
		return err
	}
	return tx.Commit()
}
