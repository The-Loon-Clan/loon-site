package handlers

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon-site/internal/storage"
	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Invites — the host capability the store's invite items were waiting on.
//
// The store plugin can sell an invite item and its buy path handles the reward,
// but it needs pluginapi.InviteGranterName published by the HOST: invites live
// on users, not in a sibling plugin. Without it every invite purchase produced
// a spend/refund pair in the ledger — the plugin deducting, failing to grant,
// and making the buyer whole. That loop is now closed.
//
// A balance, not a table of codes. UNIT3D issues individual invite rows with
// codes and an invite tree; that is a feature with its own admin surface, and
// this host has no registration gate for a code to unlock. What the granter
// contract actually needs is "credit N invites", so the honest minimum is a
// counter — and the counter is real, spendable state rather than a display
// number.

// inviteGranter is the host's pluginapi.InviteGranter.
type inviteGranter struct{ db *sqlx.DB }

var _ pluginapi.InviteGranter = inviteGranter{}

// GrantInvites credits n invites and returns the receipt label the store shows
// the buyer. The contract says n must be > 0, so a non-positive n is a caller
// bug and is rejected rather than silently treated as zero.
func (g inviteGranter) GrantInvites(ctx context.Context, userID, n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("invites: n must be > 0, got %d", n)
	}
	res, err := g.db.ExecContext(ctx,
		`UPDATE users SET invites = invites + $2 WHERE id = $1`, userID, n)
	if err != nil {
		return "", err
	}
	// No row updated means the user does not exist. Reporting that as an error
	// matters: the store REFUNDS on a failed grant, so swallowing it here would
	// take the buyer's points and credit nobody.
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		return "", fmt.Errorf("invites: no such user %d", userID)
	}
	if n == 1 {
		return "1 invite", nil
	}
	return fmt.Sprintf("%d invites", n), nil
}

// wireInvites publishes the capability on the extension registry. Call before
// core.Boot so a plugin's Provision can Lookup it.
func wireInvites(c *core.Core, data *storage.Store) error {
	if err := data.MigrateInvites(); err != nil {
		return fmt.Errorf("invites migrate: %w", err)
	}
	return c.Register(pluginapi.InviteGranterName, inviteGranter{db: data.DB()})
}

// inviteBalance is the viewer's own invite count, for the profile tile.
func (w *web) inviteBalance(ctx context.Context, userID int64) (int, bool) {
	if w.db() == nil {
		return 0, false
	}
	var n int
	if err := w.data.DB().GetContext(ctx, &n,
		`SELECT COALESCE(invites, 0) FROM users WHERE id = $1`, userID); err != nil {
		return 0, false
	}
	return n, true
}
