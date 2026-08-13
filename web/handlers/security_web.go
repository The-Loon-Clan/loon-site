package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
)

// /settings/security — the second factor, and changing the address that can
// reset your password.
//
// This is the one flow in the codebase where a bug locks somebody out rather
// than showing them a broken page, so the shape is defensive on purpose:
//
//   - ENROLMENT IS NOT COMPLETE UNTIL A CODE IS VERIFIED. The secret is stored
//     as pending and the factor stays off until the member proves their app
//     produces the right numbers. Turning it on first and asking later is how
//     a mistyped secret becomes a support ticket.
//   - RECOVERY CODES EXIST BEFORE THE FACTOR DOES. They are generated and shown
//     in the same request that enables it, never as a later step somebody can
//     skip.
//   - TURNING IT OFF NEEDS A CODE. Otherwise an attacker with a stolen session
//     removes the factor in one click, and the factor protected nothing.
//
// The login half lives in the pending-session dance at the bottom. The one
// thing to understand there: a real session is stamped under "user_id", so a
// pending second factor is stamped under a DIFFERENT key and is invisible to
// every authentication check on the site.

// pendingTOTPKey holds the user id between password and second factor.
//
// Deliberately not "user_id". session.Read looks for that key and nothing else,
// so a half-finished login cannot be mistaken for a finished one by any code
// that did not go looking for this constant specifically.
const pendingTOTPKey = "pending_totp_user"

// pendingTOTPAtKey stamps when, so a challenge cannot be left open forever.
const pendingTOTPAtKey = "pending_totp_at"

// pendingTOTPTTL is how long the second step stays available. Long enough to
// find a phone, short enough that a shared machine does not keep a half-login
// alive.
const pendingTOTPTTL = 10 * time.Minute

var securityDB *sqlx.DB

// securityMigrate adds the columns and the recovery-code table.
func securityMigrate(db *sqlx.DB) error {
	stmts := []string{
		// Pending and active are separate columns so an abandoned setup cannot
		// half-enable anything: totp_secret is authoritative and is only
		// written when a code has been verified.
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_secret TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_pending TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_enabled_at TIMESTAMPTZ`,
		`CREATE TABLE IF NOT EXISTS totp_recovery_codes (
		    id       BIGSERIAL PRIMARY KEY,
		    user_id  BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    -- The HASH, never the code. See hashRecoveryCode.
		    code_hash TEXT  NOT NULL,
		    used_at  TIMESTAMPTZ
		)`,
		`CREATE INDEX IF NOT EXISTS totp_recovery_user ON totp_recovery_codes (user_id) WHERE used_at IS NULL`,
		// Email changes are confirmed at the NEW address before they take
		// effect, so a typo cannot move the reset destination somewhere the
		// member cannot read.
		`CREATE TABLE IF NOT EXISTS email_changes (
		    token      TEXT PRIMARY KEY,
		    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    new_email  TEXT   NOT NULL,
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    expires_at TIMESTAMPTZ NOT NULL,
		    used_at    TIMESTAMPTZ
		)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// totpStatus is what the settings page needs to know.
type totpStatus struct {
	Enabled      bool
	Pending      string // the un-confirmed secret, empty unless setup is underway
	RecoveryLeft int
}

func readTOTPStatus(ctx context.Context, userID int64) totpStatus {
	var st struct {
		Secret  string `db:"totp_secret"`
		Pending string `db:"totp_pending"`
	}
	if securityDB == nil {
		return totpStatus{}
	}
	if err := securityDB.GetContext(ctx, &st,
		`SELECT totp_secret, totp_pending FROM users WHERE id = $1`, userID); err != nil {
		return totpStatus{}
	}
	out := totpStatus{Enabled: st.Secret != "", Pending: st.Pending}
	if out.Enabled {
		_ = securityDB.GetContext(ctx, &out.RecoveryLeft,
			`SELECT count(*) FROM totp_recovery_codes WHERE user_id = $1 AND used_at IS NULL`, userID)
	}
	return out
}

// issueRecoveryCodes replaces a member's codes and returns the plaintext ONCE.
//
// Replaces rather than appends: regenerating exists precisely because the old
// list is compromised or lost, and leaving the old ones valid would defeat both
// reasons.
func issueRecoveryCodes(ctx context.Context, w *web, userID int64) ([]string, error) {
	tx, err := securityDB.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.ExecContext(ctx, `DELETE FROM totp_recovery_codes WHERE user_id = $1`, userID); err != nil {
		return nil, err
	}
	out := make([]string, 0, recoveryCodeCount)
	for i := 0; i < recoveryCodeCount; i++ {
		code, err := newRecoveryCode()
		if err != nil {
			return nil, err
		}
		hash, err := hashRecoveryCode(w.flow.Hasher, code)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO totp_recovery_codes (user_id, code_hash) VALUES ($1,$2)`, userID, hash); err != nil {
			return nil, err
		}
		out = append(out, code)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// spendRecoveryCode consumes one code, returning whether it matched.
//
// Compares against every unused hash rather than looking one up: the codes are
// hashed, so there is nothing to look up BY. That is the cost of storing them
// safely and it is ten bcrypt comparisons on a path somebody reaches when they
// have lost their phone.
func spendRecoveryCode(ctx context.Context, w *web, userID int64, code string) bool {
	if strings.TrimSpace(code) == "" {
		return false
	}
	var rows []struct {
		ID   int64  `db:"id"`
		Hash string `db:"code_hash"`
	}
	if err := securityDB.SelectContext(ctx, &rows,
		`SELECT id, code_hash FROM totp_recovery_codes WHERE user_id = $1 AND used_at IS NULL`,
		userID); err != nil {
		return false
	}
	for _, r := range rows {
		if !recoveryCodeMatches(w.flow.Hasher, r.Hash, code) {
			continue
		}
		// Marked used inside the same statement that claims it, so two
		// simultaneous logins cannot both spend one code.
		res, err := securityDB.ExecContext(ctx,
			`UPDATE totp_recovery_codes SET used_at = now() WHERE id = $1 AND used_at IS NULL`, r.ID)
		if err != nil {
			return false
		}
		n, _ := res.RowsAffected()
		return n == 1
	}
	return false
}

// securityPage serves GET /settings/security.
func (w *web) securityPage(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	st := readTOTPStatus(ctx, u.ID)

	data := map[string]any{
		"Title":    "Security",
		"Enabled":  st.Enabled,
		"Recovery": st.RecoveryLeft,
		"Email":    u.Email,
		"Err":      c.Query("err"),
		"Done":     c.Query("done"),
		// Shown once, immediately after enabling or regenerating. Held in the
		// session rather than the query string: a URL with ten working bypass
		// codes in it lands in history, logs and anything watching referrers.
		"Codes": takeFlashCodes(c),
	}
	if st.Pending != "" {
		data["Setup"] = true
		data["Secret"] = totpFormatSecret(st.Pending)
		data["URI"] = totpURI(siteName(), u.Username, st.Pending)
	}
	w.render(c, "settings_security.html", data)
}

// flashCodesKey carries freshly issued recovery codes across one redirect.
const flashCodesKey = "flash_recovery_codes"

func flashCodes(c *gin.Context, codes []string) {
	s := sessions.Default(c)
	s.Set(flashCodesKey, strings.Join(codes, ","))
	_ = s.Save()
}

func takeFlashCodes(c *gin.Context) []string {
	s := sessions.Default(c)
	v, _ := s.Get(flashCodesKey).(string)
	if v == "" {
		return nil
	}
	s.Delete(flashCodesKey)
	_ = s.Save()
	return strings.Split(v, ",")
}

// siteName is the issuer an authenticator app shows beside the code.
func siteName() string { return getenvDefault("LOON_SITE_NAME", "loon indexer") }

// securityAction serves POST /settings/security.
func (w *web) securityAction(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	fail := func(msg string) {
		c.Redirect(http.StatusSeeOther, "/settings/security?err="+url.QueryEscape(msg))
	}

	switch c.PostForm("action") {
	case "begin":
		secret, err := totpSecret()
		if err != nil {
			fail("could not start setup")
			return
		}
		// Written to the PENDING column. Nothing about the account changes
		// until a code proves the app has the same secret.
		if _, err := securityDB.ExecContext(ctx,
			`UPDATE users SET totp_pending = $1 WHERE id = $2`, secret, u.ID); err != nil {
			fail("could not start setup")
			return
		}
		c.Redirect(http.StatusSeeOther, "/settings/security")

	case "confirm":
		st := readTOTPStatus(ctx, u.ID)
		if st.Pending == "" {
			fail("start the setup again")
			return
		}
		if !totpVerify(st.Pending, c.PostForm("code"), time.Now()) {
			// The pending secret is KEPT so the member can retype rather than
			// rescan. A wrong code is usually a typo or a clock, not a
			// different secret.
			fail("that code did not match — check your phone's clock and try the next one")
			return
		}
		codes, err := issueRecoveryCodes(ctx, w, u.ID)
		if err != nil {
			w.log.Error("issue recovery codes", "user", u.ID, "err", err)
			fail("could not finish setup")
			return
		}
		// Only now does the factor exist, and the codes are already written.
		if _, err := securityDB.ExecContext(ctx,
			`UPDATE users SET totp_secret = totp_pending, totp_pending = '', totp_enabled_at = now()
			  WHERE id = $1`, u.ID); err != nil {
			fail("could not finish setup")
			return
		}
		w.log.Info("two-factor enabled", "user", u.ID)
		flashCodes(c, codes)
		c.Redirect(http.StatusSeeOther, "/settings/security?done=enabled")

	case "cancel":
		_, _ = securityDB.ExecContext(ctx, `UPDATE users SET totp_pending = '' WHERE id = $1`, u.ID)
		c.Redirect(http.StatusSeeOther, "/settings/security")

	case "regenerate":
		st := readTOTPStatus(ctx, u.ID)
		if !st.Enabled || !totpVerify(secretOf(ctx, u.ID), c.PostForm("code"), time.Now()) {
			fail("that code did not match")
			return
		}
		codes, err := issueRecoveryCodes(ctx, w, u.ID)
		if err != nil {
			fail("could not regenerate")
			return
		}
		w.log.Info("recovery codes regenerated", "user", u.ID)
		flashCodes(c, codes)
		c.Redirect(http.StatusSeeOther, "/settings/security?done=codes")

	case "disable":
		// A CODE, not just a session. Otherwise a stolen session removes the
		// factor in one click and it protected nothing.
		if !totpVerify(secretOf(ctx, u.ID), c.PostForm("code"), time.Now()) &&
			!spendRecoveryCode(ctx, w, u.ID, c.PostForm("code")) {
			fail("that code did not match")
			return
		}
		if _, err := securityDB.ExecContext(ctx,
			`UPDATE users SET totp_secret = '', totp_pending = '', totp_enabled_at = NULL WHERE id = $1`,
			u.ID); err != nil {
			fail("could not turn it off")
			return
		}
		_, _ = securityDB.ExecContext(ctx, `DELETE FROM totp_recovery_codes WHERE user_id = $1`, u.ID)
		w.log.Info("two-factor disabled", "user", u.ID)
		c.Redirect(http.StatusSeeOther, "/settings/security?done=disabled")

	case "email":
		link, err := requestEmailChange(ctx, u.ID, c.PostForm("email"), baseURL())
		if err != nil {
			fail(err.Error())
			return
		}
		// The demo mailer logs the message rather than sending it, exactly as
		// the password-reset flow does -- so the link is followable from the
		// container log without an SMTP server.
		w.log.Info("email change requested (demo mailer)", "user", u.ID, "link", link)
		c.Redirect(http.StatusSeeOther, "/settings/security?done=email-sent")

	default:
		c.Redirect(http.StatusSeeOther, "/settings/security")
	}
}

// baseURL is the origin confirmation links are built against.
func baseURL() string { return getenvDefault("LOON_BASE_URL", "http://localhost:8090") }

// secretOf reads the ACTIVE secret. Empty when the factor is off, which makes
// totpVerify refuse — so a caller that forgets to check Enabled still fails
// closed.
func secretOf(ctx context.Context, userID int64) string {
	var s string
	if securityDB == nil {
		return ""
	}
	_ = securityDB.GetContext(ctx, &s, `SELECT totp_secret FROM users WHERE id = $1`, userID)
	return s
}

// ── the login half ──────────────────────────────────────────────────────────

// errTOTPRequired tells the login handler to stop short of issuing a session.
var errTOTPRequired = errors.New("second factor required")

// beginTOTPChallenge stamps the pending user and sends them to the second step.
func beginTOTPChallenge(c *gin.Context, userID int64) {
	s := sessions.Default(c)
	// Clear first: whatever was in this session, a half-authenticated state
	// must not inherit it.
	s.Clear()
	s.Set(pendingTOTPKey, userID)
	s.Set(pendingTOTPAtKey, time.Now().Unix())
	_ = s.Save()
	c.Redirect(http.StatusSeeOther, "/login/2fa")
}

// pendingTOTPUser returns who is mid-login, or 0.
func pendingTOTPUser(c *gin.Context) int64 {
	s := sessions.Default(c)
	var id int64
	switch v := s.Get(pendingTOTPKey).(type) {
	case int64:
		id = v
	case int:
		id = int64(v)
	}
	if id == 0 {
		return 0
	}
	at, _ := s.Get(pendingTOTPAtKey).(int64)
	if at == 0 || time.Since(time.Unix(at, 0)) > pendingTOTPTTL {
		s.Clear()
		_ = s.Save()
		return 0
	}
	return id
}

// twoFactorPage serves GET /login/2fa.
func (w *web) twoFactorPage(c *gin.Context) {
	if pendingTOTPUser(c) == 0 {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}
	w.render(c, "login_2fa.html", map[string]any{
		"Title": "Two-factor",
		"Err":   c.Query("err"),
	})
}

// twoFactorPost serves POST /login/2fa.
func (w *web) twoFactorPost(c *gin.Context) {
	id := pendingTOTPUser(c)
	if id == 0 {
		c.Redirect(http.StatusSeeOther, "/login?err="+url.QueryEscape("that took too long — sign in again"))
		return
	}
	ctx := c.Request.Context()
	code := c.PostForm("code")

	// A recovery code is accepted here too, because "I have lost my phone" is
	// exactly when somebody is looking at this form.
	if !totpVerify(secretOf(ctx, id), code, time.Now()) && !spendRecoveryCode(ctx, w, id, code) {
		w.log.Info("second factor rejected", "user", id)
		c.Redirect(http.StatusSeeOther, "/login/2fa?err="+url.QueryEscape("that code did not match"))
		return
	}
	u, err := w.store.ByID(ctx, id)
	if err != nil || u == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}
	// Issue clears the session itself, which takes the pending keys with it --
	// so the half-authenticated state cannot outlive the login it belonged to.
	if err := w.flow.Issue(c, u); err != nil {
		w.log.Error("session issue after 2fa", "err", err)
	}
	w.log.Info("second factor accepted", "user", id)
	c.Redirect(http.StatusSeeOther, "/")
}

// ── email change ────────────────────────────────────────────────────────────

// emailChangeTTL is how long a confirmation link lives. A day: long enough to
// find the message in a spam folder, short enough that an old link in an
// abandoned mailbox is not a standing key to the account.
const emailChangeTTL = 24 * time.Hour

// requestEmailChange stores a pending change and returns the confirmation link.
//
// CONFIRMED AT THE NEW ADDRESS, never applied on submit. The email is what a
// password reset is sent to, so writing it straight to the row means one typo
// moves the reset destination somewhere the member cannot read — and the way
// they find out is the next time they are locked out.
//
// The old address is not changed and not freed until the new one answers.
func requestEmailChange(ctx context.Context, userID int64, newEmail, baseURL string) (string, error) {
	newEmail = strings.TrimSpace(strings.ToLower(newEmail))
	if newEmail == "" || !strings.Contains(newEmail, "@") || strings.Contains(newEmail, " ") {
		return "", errors.New("that does not look like an email address")
	}
	// Taken by somebody else is a refusal; taken by YOU is a no-op worth
	// naming, because "nothing happened" and "we sent you a link" look the same
	// from the outside.
	var owner int64
	err := securityDB.GetContext(ctx, &owner,
		`SELECT id FROM users WHERE lower(email) = $1`, newEmail)
	switch {
	case err == nil && owner == userID:
		return "", errors.New("that is already your address")
	case err == nil:
		return "", errors.New("that address is already in use")
	}

	token, err := newUndoToken() // same generator: 128 bits, URL-safe
	if err != nil {
		return "", errors.New("could not start that change")
	}
	if _, err := securityDB.ExecContext(ctx, `
		INSERT INTO email_changes (token, user_id, new_email, expires_at)
		VALUES ($1,$2,$3, now() + $4::interval)`,
		token, userID, newEmail, emailChangeTTL.String()); err != nil {
		return "", errors.New("could not start that change")
	}
	return baseURL + "/settings/email/confirm?token=" + url.QueryEscape(token), nil
}

// emailConfirm serves GET /settings/email/confirm.
//
// A GET because it is reached from a link in a message, which is the whole
// point of the flow. That is the one place in this codebase where a GET
// changes state, and it is defensible only because the token is single-use,
// expiring, and unguessable — the three properties that make the link itself
// the authorisation.
func (w *web) emailConfirm(c *gin.Context) {
	token := c.Query("token")
	ctx := c.Request.Context()
	var row struct {
		UserID int64  `db:"user_id"`
		Email  string `db:"new_email"`
	}
	// Claimed and read in one statement, so a link opened twice (a mail client
	// prefetching it, say) cannot apply twice.
	if err := securityDB.GetContext(ctx, &row, `
		UPDATE email_changes SET used_at = now()
		 WHERE token = $1 AND used_at IS NULL AND expires_at > now()
		 RETURNING user_id, new_email`, token); err != nil {
		c.Redirect(http.StatusSeeOther, "/settings/security?err="+
			url.QueryEscape("that link has expired or has already been used"))
		return
	}
	if _, err := securityDB.ExecContext(ctx,
		`UPDATE users SET email = $1 WHERE id = $2`, row.Email, row.UserID); err != nil {
		w.log.Error("apply email change", "user", row.UserID, "err", err)
		c.Redirect(http.StatusSeeOther, "/settings/security?err="+url.QueryEscape("could not apply that change"))
		return
	}
	// Every other pending change for this account is dropped: they were all
	// requested from the old address, and one of them just stopped being the
	// account's address.
	_, _ = securityDB.ExecContext(ctx,
		`UPDATE email_changes SET used_at = now() WHERE user_id = $1 AND used_at IS NULL`, row.UserID)
	w.log.Info("email changed", "user", row.UserID)
	c.Redirect(http.StatusSeeOther, "/settings/security?done=email")
}
