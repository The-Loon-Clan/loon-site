package main

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/core"
)

// Durable points for the demo host.
//
// This replaces an in-memory map. Two things forced it:
//
//  1. A restart wiped every balance, which made anything built on points
//     impossible to verify twice.
//  2. The communities plugin SELECTs COALESCE(u.points, 0) for display. The
//     column has to exist, and a column that exists but never matches the real
//     balance is worse than no column — it is a number on a page that is
//     confidently wrong.
//
// So the balance lives on users.points and the history in points_ledger, and
// both move in ONE transaction. A balance that changes without a ledger row is
// exactly the inconsistency the history page exists to expose.
//
// The gates that spend points do NOT read this column — they go through
// core.PointsService (see the communities join gate, which calls Deduct). The
// column is a denormalised read for joins that want a balance without a second
// query.

// pointsMigrate adds the columns and table. Idempotent.
func pointsMigrate(db *sqlx.DB) error {
	stmts := []string{
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS points INTEGER NOT NULL DEFAULT 0`,
		// reputation_tier is read by the communities plugin for display chrome.
		// Nothing in this stack computes reputation, so it stays 0 — a column
		// that exists to satisfy a join, not a feature. If reputation ever
		// becomes real it gets a plugin, not an UPDATE here.
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS reputation_tier INTEGER NOT NULL DEFAULT 0`,
		`CREATE TABLE IF NOT EXISTS points_ledger (
		    id           BIGSERIAL PRIMARY KEY,
		    user_id      BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    amount       INTEGER NOT NULL,
		    balance      INTEGER NOT NULL,
		    kind         TEXT NOT NULL DEFAULT '',
		    description  TEXT NOT NULL DEFAULT '',
		    reference_id BIGINT,
		    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_points_ledger_user
		     ON points_ledger (user_id, created_at DESC)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// pgPoints is the durable core.PointsAdapter backing.
type pgPoints struct{ db *sqlx.DB }

// change applies a delta and records the ledger row in one transaction, so the
// two can never disagree. Returns ErrInsufficientPoints when the balance would
// go negative, which is the sentinel plugins compare against with errors.Is.
func (p pgPoints) change(ctx context.Context, userID int64, delta int, kind, desc string, ref int64) (int, error) {
	var balance int
	err := p.db.QueryRowContext(ctx,
		// The guard is IN the UPDATE: a WHERE that fails leaves the row
		// untouched and returns no rows, so a concurrent spend cannot slip a
		// balance negative between a read and a write.
		`UPDATE users SET points = points + $2
		  WHERE id = $1 AND points + $2 >= 0
		  RETURNING points`, userID, delta).Scan(&balance)
	if err != nil {
		// No row updated means the guard rejected it (or the user is gone).
		// Distinguishing those would need a second query for no gain: both
		// mean "this spend does not happen".
		return p.balance(ctx, userID), core.ErrInsufficientPoints
	}
	var refp *int64
	if ref != 0 {
		refp = &ref
	}
	if _, err := p.db.ExecContext(ctx,
		`INSERT INTO points_ledger (user_id, amount, balance, kind, description, reference_id, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		userID, delta, balance, kind, desc, refp, time.Now()); err != nil {
		return balance, err
	}
	return balance, nil
}

func (p pgPoints) balance(ctx context.Context, userID int64) int {
	var n int
	_ = p.db.GetContext(ctx, &n, `SELECT COALESCE(points, 0) FROM users WHERE id = $1`, userID)
	return n
}

func (p pgPoints) adapter() core.PointsAdapter {
	return core.PointsAdapter{
		BalanceFn: func(ctx context.Context, userID int64) (int, error) {
			return p.balance(ctx, userID), nil
		},
		AwardFn: func(ctx context.Context, userID int64, n int, kind, desc string, ref int64) (int, error) {
			return p.change(ctx, userID, n, kind, desc, ref)
		},
		DeductFn: func(ctx context.Context, userID int64, n int, kind, desc string, ref int64) (int, error) {
			return p.change(ctx, userID, -n, kind, desc, ref)
		},
		RefundFn: func(ctx context.Context, userID int64, n int, kind, desc string, ref int64) (int, error) {
			return p.change(ctx, userID, n, kind, desc, ref)
		},
		HistoryFn: func(ctx context.Context, userID int64, limit, offset int) ([]core.LedgerEntry, int, error) {
			var total int
			if err := p.db.GetContext(ctx, &total,
				`SELECT COUNT(*) FROM points_ledger WHERE user_id = $1`, userID); err != nil {
				return nil, 0, err
			}
			var rows []struct {
				Amount      int       `db:"amount"`
				Balance     int       `db:"balance"`
				Kind        string    `db:"kind"`
				Description string    `db:"description"`
				ReferenceID *int64    `db:"reference_id"`
				CreatedAt   time.Time `db:"created_at"`
			}
			if err := p.db.SelectContext(ctx, &rows,
				`SELECT amount, balance, kind, description, reference_id, created_at
				   FROM points_ledger WHERE user_id = $1
				  ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3`,
				userID, limit, offset); err != nil {
				return nil, total, err
			}
			out := make([]core.LedgerEntry, 0, len(rows))
			for _, r := range rows {
				out = append(out, core.LedgerEntry{
					Amount: r.Amount, Balance: r.Balance, Type: r.Kind,
					Description: r.Description, ReferenceID: r.ReferenceID,
					CreatedAt: r.CreatedAt,
				})
			}
			return out, total, nil
		},
	}
}
