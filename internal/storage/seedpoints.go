package storage

import (
	"context"
	"time"
)

// The host's half of the seeding economy: the unpaid-fraction carry, and the
// credit that pays it.
//
// The SWARM is not here. It used to be -- this file carried a direct read of
// tracker.user_stats and tracker.torrents, flagged in the code and to the
// plugin's owner rather than hidden -- and it is now pluginapi's
// tracker.swarmsnapshot seam instead. The host no longer depends on the shape
// of another plugin's tables, and the seeder count is computed by the tracker
// inside the same query as the rows it returns, which is the property the pool
// arithmetic actually needs.

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
