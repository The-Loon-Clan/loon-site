package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-baseline/authtoken"
	"github.com/the-loon-clan/loon-baseline/captcha"
	"github.com/the-loon-clan/loon-baseline/loginlog"
	"github.com/the-loon-clan/loon-baseline/session"

	"github.com/the-loon-clan/loon-site/internal/request"
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
	in := readLoginInput(c, captcha.FormField)

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
	// SECOND FACTOR, between a correct password and a session. Deliberately
	// after the login-attempt audit above: an attempt that got the password
	// right is worth recording as a success whether or not the second step
	// follows, because "the password is known" is the fact a reader of that log
	// needs. See security_web.go.
	if w.data.TOTPSecret(c.Request.Context(), u.ID) != "" {
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
	w.render(c, "register.html", map[string]any{
		"Title":   "Register",
		"RegMode": registrationMode(),
	})
}

func (w *web) registerPost(c *gin.Context) {
	in := readRegisterInput(c, captcha.FormField)

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
	switch in.RegMode {
	case RegClosed:
		c.Status(http.StatusForbidden)
		w.render(c, "register.html", map[string]any{
			"Title": "Register", "RegMode": RegClosed,
		})
		return
	case RegInvite:
		if !w.data.InviteCodeValid(c.Request.Context(), in.Invite) {
			again(http.StatusForbidden, "That invite code is not valid.")
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
	if registrationMode() == RegInvite && !w.data.RedeemInviteCode(c.Request.Context(), invite, u.ID) {
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
	_ = session.Clear(c)
	c.Redirect(http.StatusSeeOther, "/")
}

// ── password reset + email verification (loon-baseline authtoken) ────

func (w *web) forgotPage(c *gin.Context) {
	w.render(c, "forgot.html", map[string]any{"Title": "Reset password"})
}

func (w *web) forgotPost(c *gin.Context) {
	if err := w.captcha.Verify(c.Request.Context(), c.PostForm(captcha.FormField), c.ClientIP()); err != nil {
		c.Status(http.StatusBadRequest)
		w.render(c, "forgot.html", map[string]any{"Title": "Reset password", "Error": "Please complete the captcha."})
		return
	}
	// RequestReset is deliberately silent about whether the email exists, so we
	// always show the same confirmation.
	if err := w.resetFlow.RequestReset(c.Request.Context(), strings.TrimSpace(c.PostForm("email"))); err != nil {
		w.log.Error("request reset", "err", err)
	}
	w.render(c, "forgot.html", map[string]any{"Title": "Reset password", "Sent": true})
}

func (w *web) resetPage(c *gin.Context) {
	w.render(c, "reset.html", map[string]any{"Title": "Set a new password", "Token": c.Query("token")})
}

func (w *web) resetPost(c *gin.Context) {
	token := c.PostForm("token")
	err := w.resetFlow.PerformReset(c.Request.Context(), token, c.PostForm("password"))
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
	c.Redirect(http.StatusSeeOther, "/")
}
