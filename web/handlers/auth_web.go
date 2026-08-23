package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-baseline/authtoken"
	"github.com/the-loon-clan/loon-baseline/loginlog"
	"github.com/the-loon-clan/loon-baseline/session"

	"github.com/the-loon-clan/loon-site/internal/request"

	"github.com/the-loon-clan/loon-site/internal/storage"
	"strings"
)

// The doors: sign in, register, sign out, and the two token flows that let
// somebody back in when they cannot.
//
// Lifted out of views.go. These belong together because they are the only
// pages reachable WITHOUT a session — every one of them is on the
// always-public list in access_web.go, and a change here is a change to who
// can get in.
func (w *web) loginPage(c *gin.Context) {
	w.render(c, "login.html", map[string]any{"Title": "Log in"})
}

func (w *web) loginPost(c *gin.Context) {
	// Captcha first — a bot shouldn't get to probe credentials. No-op when the
	// Turnstile hook is unconfigured (demo default).
	in, err := readLoginInput(c)
	if err != nil {
		w.log.Error("bind login", "err", err)
	}

	// Presence only, and ahead of the captcha because an empty box costs
	// nothing to notice — see inputs.go for why nothing STRICTER belongs on a
	// login form.
	if errs := request.Validate(in); errs.Any() {
		c.Status(http.StatusBadRequest)
		w.render(c, "login.html", map[string]any{
			"Title": "Log in", "Error": errs.First(in.fieldOrder()...), "Username": in.Username,
		})
		return
	}
	if err := w.captcha.Verify(c.Request.Context(), in.Captcha, c.ClientIP()); err != nil {
		c.Status(http.StatusBadRequest)
		w.render(c, "login.html", map[string]any{"Title": "Log in", "Error": "Please complete the captcha and try again."})
		return
	}
	name := in.Username
	u, err := w.flow.Authenticate(c.Request.Context(), name, in.Password)
	// Audit the attempt via loon-baseline's standard policy (hash the IP,
	// attribute a failed attempt to the targeted account). One call — the
	// policy lives in loginlog, not here.
	if w.loginLog != nil {
		var uid int64
		if u != nil {
			uid = u.ID
		}
		if e := loginlog.Attempt(c.Request.Context(), w.loginLog, w.store.IDByName,
			w.ipSalt, c.ClientIP(), name, uid, err == nil); e != nil {
			w.log.Error("login log", "err", e)
		}
	}
	if err != nil {
		c.Status(http.StatusUnauthorized)
		w.render(c, "login.html", map[string]any{"Title": "Log in", "Error": "Invalid username or password."})
		return
	}
	// The password was right, so hand the rate-limit token back: the auth tier
	// is a brute-force budget and a correct password is not an attempt at brute
	// force. Charging successes too means eight logins in a row lock out the
	// ninth, which is a shared address or an operator's tooling, not an attack.
	// Deliberately here rather than after the second factor -- see the audit
	// note below for why "the password is known" is the fact that matters.
	w.tAuth.Refund(c)

	// SECOND FACTOR, between a correct password and a session. Deliberately
	// after the login-attempt audit above: an attempt that got the password
	// right is worth recording as a success whether or not the second step
	// follows, because "the password is known" is the fact a reader of that log
	// needs. See security_web.go.
	hasTOTP, err := w.data.HasTOTP(c.Request.Context(), u.ID)
	if err != nil {
		// Fail CLOSED. This used to read TOTPSecret() != "", where a failed
		// database read returns "" and the account was let through on a
		// password alone — an authentication downgrade that healed itself
		// before anyone could see it. Refusing the login is the safe side of a
		// question we could not answer.
		w.log.Error("check second factor", "user", u.ID, "err", err)
		c.Status(http.StatusServiceUnavailable)
		w.render(c, "login.html", map[string]any{
			"Title": "Log in",
			"Error": "Could not complete sign-in just now. Please try again.",
		})
		return
	}
	if hasTOTP {
		beginTOTPChallenge(c, u.ID)
		return
	}
	if err := w.flow.Issue(c, u); err != nil {
		w.log.Error("session issue", "err", err)
	}
	c.Redirect(http.StatusSeeOther, "/")
}

func (w *web) registerPage(c *gin.Context) {
	// The mode reaches the template rather than the handler refusing outright:
	// a closed site should SAY it is closed, not 404 the page a visitor was
	// invited to by a link. "Registration is closed" is information; a dead
	// link is a puzzle.
	info := registrationModeInfo()
	data := map[string]any{
		"Title": "Register",
		// RegMode drives the template's existing branches, and a plugin mode
		// that forbids direct signup renders as CLOSED — the same page, since
		// "you cannot sign up here" is the same message however it was decided.
		// The widget below is what says where to go instead.
		"RegMode": registrationMode(),
		"Mode":    info,
	}
	// A visitor arriving WITH an invite gets the form, whatever the mode says
	// about strangers: the invite email links here with ?invite=CODE, and the
	// person who follows it needs somewhere to type their username. Everybody
	// else gets the mode's call to action.
	//
	// So AllowsSignup hides the form from people who have nothing, rather than
	// from people who were invited — which is the same distinction the POST
	// makes, kept in step deliberately.
	invited := info.RequiresInvite && strings.TrimSpace(c.Query("invite")) != ""
	switch {
	case invited:
		// Rendered as the built-in invite mode, which is exactly what it is
		// from here: a form with an invite box. Reusing that branch rather
		// than adding a third means the invite field, its help text and its
		// required flag cannot drift between the two ways of reaching it.
		data["RegMode"] = RegInvite
	case !info.AllowsSignup && info.Key != RegClosed:
		data["RegMode"] = RegClosed
	}
	data["Invite"] = strings.TrimSpace(c.Query("invite"))
	// Operator-placed widgets for the sign-up page. This is where a plugin that
	// owns how people join — an application queue, a waiting list — puts its
	// call to action, so the host never learns what those are.
	if ws := w.renderRegion(c, "register"); len(ws) > 0 {
		data["RegionWidgets"] = ws
	}
	w.render(c, "register.html", data)
}

func (w *web) registerPost(c *gin.Context) {
	in, err := readRegisterInput(c)
	if err != nil {
		w.log.Error("bind register", "err", err)
	}

	// again re-renders the form with what was typed still in it. Losing a
	// half-filled form to a validation message is its own small insult, and
	// four call sites rebuilding the same map by hand is how one of them ends
	// up dropping a field.
	again := func(status int, msg string) {
		c.Status(status)
		w.render(c, "register.html", map[string]any{
			"Title": "Register", "Error": msg, "RegMode": in.RegMode,
			"Username": in.Username, "Email": in.Email, "Invite": in.Invite,
		})
	}

	// Enforced HERE as well as in the template, because the template only
	// hides the form. A closed site whose POST still works is open to anyone
	// who kept the page open, or who has ever used curl.
	// A mode a PLUGIN added behaves by its two flags rather than by name — see
	// pluginapi.RegistrationModeInfo. Checked before the built-in switch so a
	// registered mode never falls through to the open-signup default.
	if info := registrationModeInfo(); info.Key != RegOpen && info.Key != RegInvite && info.Key != RegClosed {
		switch {
		case info.RequiresInvite:
			// Checked FIRST, and that order is the whole correctness of the
			// application flow. AllowsSignup governs whether the PAGE offers a
			// form; it does not govern who may submit one. An approved
			// applicant arrives from the invite email holding a code, and
			// reading AllowsSignup as "nobody may register" refused exactly
			// the people the mode exists to admit.
			//
			// Handled as the built-in invite mode from here, so the plugin
			// gets redemption, the email lock and the race guard without
			// reimplementing any of them.
			in.RegMode = RegInvite
		case !info.AllowsSignup:
			// No form and no invite path: the mode is closed by another name.
			c.Status(http.StatusForbidden)
			w.render(c, "register.html", map[string]any{
				"Title": "Register", "RegMode": RegClosed, "Mode": info,
			})
			return
		}
	}
	switch in.RegMode {
	case RegClosed:
		c.Status(http.StatusForbidden)
		w.render(c, "register.html", map[string]any{
			"Title": "Register", "RegMode": RegClosed,
		})
		return
	case RegInvite:
		// One read, then three different answers, because "invalid" is a bad
		// thing to tell somebody holding a real invite. A visitor whose code
		// expired needs to ask for a new one; a visitor who typed the wrong
		// address needs to fix the form; and only a visitor with a code that
		// was never issued should be told it does not exist. Collapsing those
		// into one refusal sends two of the three to the wrong remedy.
		l := w.data.LookupInviteCode(c.Request.Context(), in.Invite)
		switch {
		case !l.Found:
			again(http.StatusForbidden, "That invite code is not valid.")
			return
		case l.Reason == "used":
			again(http.StatusForbidden, "That invite has already been used.")
			return
		case l.Reason == "revoked":
			again(http.StatusForbidden, "That invite was withdrawn. Ask whoever sent it for another.")
			return
		case l.Reason == "expired":
			again(http.StatusForbidden, "That invite has expired. Ask whoever sent it for another.")
			return
		}
		// The email lock. Enforced only when the invite CARRIES an address —
		// codes issued before the lock existed, and codes from a site running
		// with it off, have none and stay usable by anybody holding them.
		//
		// Compared on the normalised form so capitals and stray whitespace are
		// not a rejection, and gated on the strict setting so an operator can
		// treat the address as "where it was sent" rather than "who may use
		// it".
		if l.Email != "" && currentInviteOptions().EmailStrict &&
			storage.NormaliseEmail(in.Email) != l.Email {
			again(http.StatusForbidden, "That invite was sent to a different email address. "+
				"Sign up with the address it was sent to.")
			return
		}
	}
	// The endpoint's own rules, in one place — see inputs.go. Ahead of the
	// captcha because these cost nothing to check and the captcha costs a round
	// trip to Cloudflare.
	if errs := request.Validate(in); errs.Any() {
		again(http.StatusBadRequest, errs.First(in.fieldOrder()...))
		return
	}
	if err := w.captcha.Verify(c.Request.Context(), in.Captcha, c.ClientIP()); err != nil {
		again(http.StatusBadRequest, "Please complete the captcha and try again.")
		return
	}
	invite := in.Invite
	u, err := w.flow.Register(c.Request.Context(), in.Username, in.Email, in.Password)
	if err != nil {
		again(http.StatusBadRequest, err.Error())
		return
	}
	// Consume the code now the account exists. Redeem, not just validate: a
	// gate that checks without consuming lets one code make any number of
	// accounts, which is the whole thing invite-only is trying to stop.
	//
	// After Register on purpose. Redeeming first would burn the code when
	// registration then fails on a taken username — the visitor loses an invite
	// they were given and has nothing to show for it.
	// in.RegMode, NOT registrationMode(). A plugin mode that requires an invite
	// is normalised to RegInvite above, and reading the raw setting here missed
	// that — the code was VALIDATED and never consumed, so one invite could
	// create any number of accounts. That is precisely what the comment below
	// says this line exists to prevent, defeated by asking the wrong question.
	if in.RegMode == RegInvite && !w.data.RedeemInviteCode(c.Request.Context(), invite, u.ID) {
		// The account exists and the code did not stick — a race with another
		// registration on the same code. Say so rather than leaving them signed
		// in via a gate that did not open.
		w.log.Warn("invite not redeemed after register", "user", u.ID)
	}
	if err := w.flow.Issue(c, u); err != nil {
		w.log.Error("session issue", "err", err)
	}
	// Send the email-verification link (no-op if they left email blank).
	if err := w.resetFlow.SendVerify(c.Request.Context(), u.ID, u.Email); err != nil {
		w.log.Error("send verify", "err", err)
	}
	c.Redirect(http.StatusSeeOther, "/")
}

func (w *web) logout(c *gin.Context) {
	// A logout that fails silently is the one failure here worth a log line:
	// the session survives, the redirect reports success, and somebody on a
	// shared machine walks away from an account that is still signed in. The
	// nav rendering as signed-in is the only other signal, and it is one people
	// do not look for after clicking Log out.
	if err := session.Clear(c); err != nil {
		w.log.Error("logout", "err", err)
	}
	c.Redirect(http.StatusSeeOther, "/")
}

// ── password reset + email verification (loon-baseline authtoken) ────

func (w *web) forgotPage(c *gin.Context) {
	w.render(c, "forgot.html", map[string]any{"Title": "Reset password"})
}

func (w *web) forgotPost(c *gin.Context) {
	in, _ := readForgotInput(c)
	// The one validation here that is not a duplicate of something downstream.
	// RequestReset is deliberately SILENT about whether an address is known —
	// otherwise this form answers "does this person have an account" for
	// anybody who asks — which also means a typo'd address produces the same
	// cheerful confirmation as a real one. Checking the shape is the only
	// feedback that can be given without giving the other thing away.
	if errs := request.Validate(in); errs.Any() {
		c.Status(http.StatusBadRequest)
		w.render(c, "forgot.html", map[string]any{
			"Title": "Reset password", "Error": errs.First(in.fieldOrder()...),
		})
		return
	}
	if err := w.captcha.Verify(c.Request.Context(), in.Captcha, c.ClientIP()); err != nil {
		c.Status(http.StatusBadRequest)
		w.render(c, "forgot.html", map[string]any{"Title": "Reset password", "Error": "Please complete the captcha."})
		return
	}
	// RequestReset is deliberately silent about whether the email exists, so we
	// always show the same confirmation.
	if err := w.resetFlow.RequestReset(c.Request.Context(), in.Email); err != nil {
		w.log.Error("request reset", "err", err)
	}
	w.render(c, "forgot.html", map[string]any{"Title": "Reset password", "Sent": true})
}

func (w *web) resetPage(c *gin.Context) {
	w.render(c, "reset.html", map[string]any{"Title": "Set a new password", "Token": c.Query("token")})
}

func (w *web) resetPost(c *gin.Context) {
	in, _ := readResetInput(c)
	token := in.Token

	// Before PerformReset, which consumes the token on success OR failure. A
	// mismatch caught afterwards would cost the member their one link.
	if errs := request.Validate(in); errs.Any() {
		c.Status(http.StatusBadRequest)
		w.render(c, "reset.html", map[string]any{
			"Title": "Set a new password", "Token": token,
			"Error": errs.First(in.fieldOrder()...),
		})
		return
	}

	err := w.resetFlow.PerformReset(c.Request.Context(), token, in.Password)
	if err != nil {
		msg := "Could not reset your password."
		switch {
		case errors.Is(err, authtoken.ErrWeakPassword):
			msg = "Password must be at least 8 characters."
		case errors.Is(err, authtoken.ErrInvalidToken):
			msg = "This reset link is invalid or has expired."
		}
		c.Status(http.StatusBadRequest)
		w.render(c, "reset.html", map[string]any{"Title": "Set a new password", "Token": token, "Error": msg})
		return
	}
	w.render(c, "login.html", map[string]any{"Title": "Log in", "Notice": "Password updated. Please log in."})
}

func (w *web) verifyEmail(c *gin.Context) {
	data := map[string]any{"Title": "Log in"}
	if _, err := w.resetFlow.ConfirmVerify(c.Request.Context(), c.Query("token")); err != nil {
		data["Error"] = "This verification link is invalid or has expired."
	} else {
		data["Notice"] = "Your email is verified. Thanks!"
	}
	w.render(c, "login.html", data)
}

func (w *web) resendVerify(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	if full, err := w.store.ByID(c.Request.Context(), u.ID); err == nil && full != nil {
		if err := w.resetFlow.SendVerify(c.Request.Context(), full.ID, full.Email); err != nil {
			w.log.Error("resend verify", "err", err)
		}
	}
	// The banner replaces itself with the confirmation, and the reader stays on
	// the page they were on. The redirect below is what a no-JavaScript client
	// still gets, and it is why this one was worth converting: "/" is nowhere
	// near where the button was pressed.
	if isHTMX(c) {
		w.renderFragment(c, shellPage, "verify-notice", map[string]any{"VerifySent": true})
		return
	}
	c.Redirect(http.StatusSeeOther, "/")
}
