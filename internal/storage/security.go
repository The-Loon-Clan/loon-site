package storage

import "context"

// Account security: the two-factor columns, its recovery codes, and the
// verified email-change tokens.
//
// The HASHING stays in the HTTP layer — it needs the site's password hasher,
// and which hasher a deployment uses is not a storage decision. What lives
// here is every statement, and with them the two atomicity properties this
// domain depends on and a reader has to be able to find:
//
//   - an email-change token is claimed and read in ONE statement, so a link
//     opened twice (a mail client prefetching it) cannot apply twice;
//   - a recovery code is marked used inside the statement that claims it, and
//     the caller checks the row count, so two simultaneous logins cannot both
//     spend one code.
//
// Both were correct in the handlers and are easy to lose in a rewrite that
// only sees "read, then write". Stated here, next to the SQL that implements
// them, they are harder to undo by accident.

// TOTPStatus is what the security page needs to know about a member's second
// factor: whether it is on, whether a setup is half-finished, and how many
// recovery codes remain unspent.
type TOTPStatus struct {
	Enabled      bool
	Pending      string // the un-confirmed secret, empty unless setup is underway
	RecoveryLeft int
}

// ReadTOTPStatus reports the member's two-factor state.
func (st *Store) ReadTOTPStatus(ctx context.Context, userID int64) TOTPStatus {
	var row struct {
		Secret  string `db:"totp_secret"`
		Pending string `db:"totp_pending"`
	}
	if err := st.db.GetContext(ctx, &row,
		`SELECT totp_secret, totp_pending FROM users WHERE id = $1`, userID); err != nil {
		return TOTPStatus{}
	}
	out := TOTPStatus{Enabled: row.Secret != "", Pending: row.Pending}
	if out.Enabled {
		_ = st.db.GetContext(ctx, &out.RecoveryLeft,
			`SELECT COUNT(*) FROM totp_recovery_codes WHERE user_id = $1 AND used_at IS NULL`, userID)
	}
	return out
}

// TOTPSecret returns the live secret, or "" when the factor is off.
func (st *Store) TOTPSecret(ctx context.Context, userID int64) string {
	var s string
	_ = st.db.GetContext(ctx, &s, `SELECT totp_secret FROM users WHERE id = $1`, userID)
	return s
}

// SetPendingTOTP stores a secret that is not yet in force.
//
// The PENDING column, not the live one: nothing about the account changes
// until a code proves the authenticator holds the same secret.
func (st *Store) SetPendingTOTP(ctx context.Context, userID int64, secret string) error {
	_, err := st.db.ExecContext(ctx,
		`UPDATE users SET totp_pending = $1 WHERE id = $2`, secret, userID)
	return err
}

// EnableTOTP promotes the pending secret to the live one.
//
// Called only after the recovery codes are written, so there is no moment when
// the factor is on and the member has no way past it.
func (st *Store) EnableTOTP(ctx context.Context, userID int64) error {
	_, err := st.db.ExecContext(ctx,
		`UPDATE users SET totp_secret = totp_pending, totp_pending = '', totp_enabled_at = now()
		  WHERE id = $1`, userID)
	return err
}

// ClearPendingTOTP abandons a setup in progress.
func (st *Store) ClearPendingTOTP(ctx context.Context, userID int64) error {
	_, err := st.db.ExecContext(ctx,
		`UPDATE users SET totp_pending = '' WHERE id = $1`, userID)
	return err
}

// DisableTOTP removes the factor and every recovery code with it.
//
// The codes go too: leaving them would let a saved code re-open an account
// whose owner has deliberately turned the factor off.
func (st *Store) DisableTOTP(ctx context.Context, userID int64) error {
	if _, err := st.db.ExecContext(ctx,
		`UPDATE users SET totp_secret = '', totp_pending = '', totp_enabled_at = NULL WHERE id = $1`,
		userID); err != nil {
		return err
	}
	_, err := st.db.ExecContext(ctx,
		`DELETE FROM totp_recovery_codes WHERE user_id = $1`, userID)
	return err
}

// RecoveryCode is one unspent code, hashed.
type RecoveryCode struct {
	ID   int64  `db:"id"`
	Hash string `db:"code_hash"`
}

// UnusedRecoveryCodes lists the codes still available to a member.
//
// Hashes, not codes: the site cannot show them again and does not pretend to.
// The caller compares a submitted code against each with the site's hasher.
func (st *Store) UnusedRecoveryCodes(ctx context.Context, userID int64) ([]RecoveryCode, error) {
	var rows []RecoveryCode
	err := st.db.SelectContext(ctx, &rows,
		`SELECT id, code_hash FROM totp_recovery_codes WHERE user_id = $1 AND used_at IS NULL`, userID)
	return rows, err
}

// SpendRecoveryCode marks one code used and reports whether THIS call was the
// one that spent it.
//
// The used_at IS NULL in the WHERE is the whole mechanism: two simultaneous
// logins both matching the same code will both reach this statement, and only
// one of them updates a row. Returning the row count rather than a nil error is
// what makes that difference visible to the caller.
func (st *Store) SpendRecoveryCode(ctx context.Context, id int64) bool {
	res, err := st.db.ExecContext(ctx,
		`UPDATE totp_recovery_codes SET used_at = now() WHERE id = $1 AND used_at IS NULL`, id)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

// ReplaceRecoveryCodes swaps a member's whole set for the hashes given.
//
// One transaction: a half-replaced set would leave the member holding codes
// that no longer work alongside ones they have never seen.
func (st *Store) ReplaceRecoveryCodes(ctx context.Context, userID int64, hashes []string) error {
	tx, err := st.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM totp_recovery_codes WHERE user_id = $1`, userID); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO totp_recovery_codes (user_id, code_hash) VALUES ($1,$2)`, userID, h); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ClaimEmailChange consumes a change token and returns what it authorises.
//
// Claimed and read in ONE statement. A separate "look it up, then mark it
// used" leaves a window in which a link opened twice — a mail client
// prefetching it, an impatient double click — applies twice.
func (st *Store) ClaimEmailChange(ctx context.Context, token string) (userID int64, email string, ok bool) {
	var row struct {
		UserID int64  `db:"user_id"`
		Email  string `db:"new_email"`
	}
	if err := st.db.GetContext(ctx, &row, `
		UPDATE email_changes SET used_at = now()
		 WHERE token = $1 AND used_at IS NULL AND expires_at > now()
		 RETURNING user_id, new_email`, token); err != nil {
		return 0, "", false
	}
	return row.UserID, row.Email, true
}

// ApplyEmailChange writes the new address onto the account.
func (st *Store) ApplyEmailChange(ctx context.Context, userID int64, email string) error {
	_, err := st.db.ExecContext(ctx,
		`UPDATE users SET email = $1 WHERE id = $2`, email, userID)
	return err
}

// DropPendingEmailChanges cancels every other outstanding request for an account.
//
// They were all requested from the OLD address, and one of them just stopped
// being the account's address.
func (st *Store) DropPendingEmailChanges(ctx context.Context, userID int64) error {
	_, err := st.db.ExecContext(ctx,
		`UPDATE email_changes SET used_at = now() WHERE user_id = $1 AND used_at IS NULL`, userID)
	return err
}

// EmailOwner returns the account currently using an address, if any.
//
// The ID matters, not just whether it is taken: an address held by SOMEBODY
// ELSE is a refusal, while one already held by the requester is a no-op worth
// naming — "nothing happened" and "we sent you a link" look identical from the
// outside, and only one of them is true.
func (st *Store) EmailOwner(ctx context.Context, email string) (int64, bool) {
	var id int64
	if err := st.db.GetContext(ctx, &id, `SELECT id FROM users WHERE lower(email) = $1`, email); err != nil {
		return 0, false
	}
	return id, true
}

// StartEmailChange records a pending change and its token.
//
// ttl is passed as a string interval so the expiry is computed by the
// database: a client clock that is wrong would otherwise mint a link that is
// already expired, or one that outlives its window.
func (st *Store) StartEmailChange(ctx context.Context, token string, userID int64, newEmail, ttl string) error {
	_, err := st.db.ExecContext(ctx, `
		INSERT INTO email_changes (token, user_id, new_email, expires_at)
		VALUES ($1,$2,$3, now() + $4::interval)`, token, userID, newEmail, ttl)
	return err
}
