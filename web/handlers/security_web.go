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
	"github.com/the-loon-clan/loon/core"
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

// issueRecoveryCodes replaces a member's codes and returns the plaintext ONCE.
//
// Replaces rather than appends: regenerating exists precisely because the old
// list is compromised or lost, and leaving the old ones valid would defeat both
// reasons.
//
// The MINTING and HASHING stay here — they need the site's password hasher,
// and which hasher a deployment uses is not a storage decision. Only the write
// crosses, as one transaction (see ReplaceRecoveryCodes).
func (w *web) issueRecoveryCodes(ctx context.Context, userID int64) ([]string, error) {
	codes := make([]string, 0, recoveryCodeCount)
	hashes := make([]string, 0, recoveryCodeCount)
	for i := 0; i < recoveryCodeCount; i++ {
		code, err := newRecoveryCode()
		if err != nil {
			return nil, err
		}
		hash, err := hashRecoveryCode(w.flow.Hasher, code)
		if err != nil {
			return nil, err
		}
		codes = append(codes, code)
		hashes = append(hashes, hash)
	}
	if err := w.data.ReplaceRecoveryCodes(ctx, userID, hashes); err != nil {
		return nil, err
	}
	return codes, nil
}

// spendRecoveryCode consumes one code, returning whether it matched.
//
// Compares against every unused hash rather than looking one up: the codes are
// hashed, so there is nothing to look up BY. That is the cost of storing them
// safely and it is ten bcrypt comparisons on a path somebody reaches when they
// have lost their phone.
func (w *web) spendRecoveryCode(ctx context.Context, userID int64, code string) bool {
	if strings.TrimSpace(code) == "" {
		return false
	}
	rows, err := w.data.UnusedRecoveryCodes(ctx, userID)
	if err != nil {
		return false
	}
	for _, r := range rows {
		if !recoveryCodeMatches(w.flow.Hasher, r.Hash, code) {
			continue
		}
		// SpendRecoveryCode claims and marks in one statement and reports
		// whether THIS call was the one that spent it, so two simultaneous
		// logins matching the same code cannot both succeed.
		return w.data.SpendRecoveryCode(ctx, r.ID)
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
	st := w.data.ReadTOTPStatus(ctx, u.ID)

	data := map[string]any{
		"Title":    "Security",
		"Enabled":  st.Enabled,
		"Recovery": st.RecoveryLeft,
		"Email":    u.Email,
		"Err":      c.Query(queryErr),
		"Done":     c.Query(queryDone),
		// Shown once, immediately after enabling or regenerating. Held in the
		// session rather than the query string: a URL with ten working bypass
		// codes in it lands in history, logs and anything watching referrers.
		"Codes": takeFlashCodes(c),
	}
	if st.Pending != "" {
		data["Setup"] = true
		data["Secret"] = totpFormatSecret(st.Pending)
		data["URI"] = totpURI(w.siteName(), u.Username, st.Pending)
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
func (w *web) siteName() string { return getenvDefault("LOON_SITE_NAME", "loon indexer") }

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

	switch c.PostForm(fieldAction) {
	case "begin":
		w.totpBegin(c, ctx, u, fail)
	case "confirm":
		w.totpConfirm(c, ctx, u, fail)
	case "cancel":
		w.totpCancel(c, ctx, u, fail)
	case "regenerate":
		w.totpRegenerate(c, ctx, u, fail)
	case "disable":
		w.totpDisable(c, ctx, u, fail)
	case "email":
		w.emailChangeRequest(c, ctx, u, fail)
	default:
		c.Redirect(http.StatusSeeOther, "/settings/security")
	}
}

// totpBegin mints a secret and shows the QR code.
//
// The secret goes to the PENDING column: nothing about the account changes
// until a code proves the authenticator holds the same secret.
func (w *web) totpBegin(c *gin.Context, ctx context.Context, u *core.User, fail func(string)) {
	secret, err := totpSecret()
	if err != nil {
		fail("could not start setup")
		return
	}
	// Written to the PENDING column. Nothing about the account changes
	// until a code proves the app has the same secret.
	if err := w.data.SetPendingTOTP(ctx, u.ID, secret); err != nil {
		fail("could not start setup")
		return
	}
	c.Redirect(http.StatusSeeOther, "/settings/security")
}

// totpConfirm turns a pending secret into a live second factor.
//
// A wrong code KEEPS the pending secret so the member can retype rather than
// rescan — a mismatch is usually a typo or a clock, not a different secret.
// Recovery codes are written before the factor goes live, so there is no
// window where the account is behind an app with no way back.
func (w *web) totpConfirm(c *gin.Context, ctx context.Context, u *core.User, fail func(string)) {
	st := w.data.ReadTOTPStatus(ctx, u.ID)
	if st.Pending == "" {
		fail("start the setup again")
		return
	}
	if !totpVerify(st.Pending, c.PostForm(fieldCode), time.Now()) {
		// The pending secret is KEPT so the member can retype rather than
		// rescan. A wrong code is usually a typo or a clock, not a
		// different secret.
		fail("that code did not match — check your phone's clock and try the next one")
		return
	}
	codes, err := w.issueRecoveryCodes(ctx, u.ID)
	if err != nil {
		w.log.Error("issue recovery codes", "user", u.ID, "err", err)
		fail("could not finish setup")
		return
	}
	// Only now does the factor exist, and the codes are already written.
	if err := w.data.EnableTOTP(ctx, u.ID); err != nil {
		fail("could not finish setup")
		return
	}
	w.log.Info("two-factor enabled", "user", u.ID)
	flashCodes(c, codes)
	c.Redirect(http.StatusSeeOther, "/settings/security?done=enabled")
}

// totpCancel abandons a setup in progress, clearing the pending secret.
func (w *web) totpCancel(c *gin.Context, ctx context.Context, u *core.User, fail func(string)) {
	_ = w.data.ClearPendingTOTP(ctx, u.ID)
	c.Redirect(http.StatusSeeOther, "/settings/security")
}

// totpRegenerate issues fresh recovery codes and invalidates the old ones.
//
// Shown once: they are stored hashed, so the site cannot show them again and
// does not pretend it can.
func (w *web) totpRegenerate(c *gin.Context, ctx context.Context, u *core.User, fail func(string)) {
	st := w.data.ReadTOTPStatus(ctx, u.ID)
	if !st.Enabled || !totpVerify(w.data.TOTPSecret(ctx, u.ID), c.PostForm(fieldCode), time.Now()) {
		fail("that code did not match")
		return
	}
	codes, err := w.issueRecoveryCodes(ctx, u.ID)
	if err != nil {
		fail("could not regenerate")
		return
	}
	w.log.Info("recovery codes regenerated", "user", u.ID)
	flashCodes(c, codes)
	c.Redirect(http.StatusSeeOther, "/settings/security?done=codes")
}

// totpDisable removes the second factor.
//
// Requires a CODE, not merely a session — otherwise a stolen session removes
// the factor in one click and it protected nothing.
func (w *web) totpDisable(c *gin.Context, ctx context.Context, u *core.User, fail func(string)) {
	// A CODE, not just a session. Otherwise a stolen session removes the
	// factor in one click and it protected nothing.
	if !totpVerify(w.data.TOTPSecret(ctx, u.ID), c.PostForm(fieldCode), time.Now()) &&
		!w.spendRecoveryCode(ctx, u.ID, c.PostForm(fieldCode)) {
		fail("that code did not match")
		return
	}
	// The recovery codes go with it — see DisableTOTP. Leaving them would let
	// a saved code re-open an account whose owner just turned the factor off.
	if err := w.data.DisableTOTP(ctx, u.ID); err != nil {
		fail("could not turn it off")
		return
	}
	w.log.Info("two-factor disabled", "user", u.ID)
	c.Redirect(http.StatusSeeOther, "/settings/security?done=disabled")
}

// emailChangeRequest starts a verified email change.
//
// The demo's mailer logs the message rather than sending it, as the
// password-reset flow does, so the link is followable from the container log
// without an SMTP server.
func (w *web) emailChangeRequest(c *gin.Context, ctx context.Context, u *core.User, fail func(string)) {
	link, err := w.requestEmailChange(ctx, u.ID, c.PostForm("email"), w.baseURL())
	if err != nil {
		fail(err.Error())
		return
	}
	// The demo mailer logs the message rather than sending it, exactly as
	// the password-reset flow does -- so the link is followable from the
	// container log without an SMTP server.
	w.log.Info("email change requested (demo mailer)", "user", u.ID, "link", link)
	c.Redirect(http.StatusSeeOther, "/settings/security?done=email-sent")
}

// baseURL is the origin confirmation links are built against.
func (w *web) baseURL() string { return getenvDefault("LOON_BASE_URL", "http://localhost:8090") }

// ── the login half ──────────────────────────────────────────────────────────

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
		"Err":   c.Query(queryErr),
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
	code := c.PostForm(fieldCode)

	// A recovery code is accepted here too, because "I have lost my phone" is
	// exactly when somebody is looking at this form.
	if !totpVerify(w.data.TOTPSecret(ctx, id), code, time.Now()) && !w.spendRecoveryCode(ctx, id, code) {
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
func (w *web) requestEmailChange(ctx context.Context, userID int64, newEmail, baseURL string) (string, error) {
	newEmail = strings.TrimSpace(strings.ToLower(newEmail))
	if newEmail == "" || !strings.Contains(newEmail, "@") || strings.Contains(newEmail, " ") {
		return "", errors.New("that does not look like an email address")
	}
	// Taken by somebody else is a refusal; taken by YOU is a no-op worth
	// naming, because "nothing happened" and "we sent you a link" look the same
	// from the outside.
	switch owner, taken := w.data.EmailOwner(ctx, newEmail); {
	case taken && owner == userID:
		return "", errors.New("that is already your address")
	case taken:
		return "", errors.New("that address is already in use")
	}

	token, err := newUndoToken() // same generator: 128 bits, URL-safe
	if err != nil {
		return "", errors.New("could not start that change")
	}
	if err := w.data.StartEmailChange(ctx, token, userID, newEmail, emailChangeTTL.String()); err != nil {
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
	// One statement claims and reads the token — see ClaimEmailChange.
	userID, newEmail, ok := w.data.ClaimEmailChange(ctx, token)
	if !ok {
		c.Redirect(http.StatusSeeOther, "/settings/security?err="+
			url.QueryEscape("that link has expired or has already been used"))
		return
	}
	if err := w.data.ApplyEmailChange(ctx, userID, newEmail); err != nil {
		w.log.Error("apply email change", "user", userID, "err", err)
		c.Redirect(http.StatusSeeOther, "/settings/security?err="+url.QueryEscape("could not apply that change"))
		return
	}
	// Every other pending change for this account is dropped: they were all
	// requested from the old address, and one of them just stopped being the
	// account's address.
	_ = w.data.DropPendingEmailChanges(ctx, userID)
	w.log.Info("email changed", "user", userID)
	c.Redirect(http.StatusSeeOther, "/settings/security?done=email")
}
