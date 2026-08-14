package handlers

// The async layer: htmx, and the two helpers every partial-updating endpoint
// needs.
//
// # Why htmx rather than fetch()
//
// The prod indexer answered this question by doing it the other way first, and
// its tree is the argument. Eighty-five hand-written fetch() calls across
// thirty-one templates, one shared .js file between them, and the shape that
// falls out of that:
//
//	69  .json()            the server sends data
//	65  innerHTML = ...    the client rebuilds the markup from it
//	30  location.reload()  a third of them give up and refresh anyway
//	57  _csrf in the body vs 12 X-CSRF-Token headers, and at least one
//	    endpoint (/admin/duplicates/purge-all) that sends NEITHER and is
//	    therefore rejected 403 every time it is pressed
//
// Every one of those numbers is a consequence of the same decision: if the
// server returns JSON, the client must own a second copy of the view. That copy
// is written in a different language, is never type-checked against the first,
// and drifts. The 30 reloads are what it looks like when somebody gives up
// maintaining the copy — the request is async and the page still refreshes,
// which is the worst of both.
//
// htmx inverts it. The server returns the SAME HTML it already knows how to
// render, and the client swaps it in. There is one view layer, in one language,
// and the fragment and the full page are the same template — so they cannot
// disagree.
//
// # The rules
//
// Written out in docs/ASYNC.md; the two that this file exists to enforce:
//
//  1. Every control is a real form or link that works with JavaScript off.
//     htmx attributes ADD behaviour to a working control, they never replace
//     the mechanism. Delete the script tag and the site still works.
//
//  2. The handler serves a fragment to htmx and does exactly what it did before
//     to everyone else. isHTMX is the only branch; the redirect path is
//     untouched, which is what keeps rule 1 true.

import (
	"html/template"
	"net/http"

	"github.com/gin-gonic/gin"

	site "github.com/the-loon-clan/loon-site"
	"github.com/the-loon-clan/loon-site/internal/middleware"
)

// hxRequestHeader is set by htmx on every request it makes.
//
// Checked for the literal "true" rather than mere presence: htmx sends
// HX-Request: true, and a header that is present but empty is not htmx. A
// caller can of course forge it — which is harmless, because the fragment is
// the same HTML the full page would have shown to that same viewer, rendered
// by the same template with the same authorisation already done above.
const hxRequestHeader = "HX-Request"

// hxHistoryRestoreHeader marks a request htmx makes to REBUILD a page it is
// navigating back to, which is the one htmx request that must be answered with
// a whole page.
//
// htmx sends HX-Request: true on these too (config historyRestoreAsHxRequest,
// default true), so a handler that only checks that header answers the back
// button with a fragment — and the browser then displays a bare table where the
// site used to be. The config is set to false in site_chrome.html as well; this
// is the half that does not depend on the client agreeing.
const hxHistoryRestoreHeader = "HX-History-Restore-Request"

// isHTMX reports whether this request wants a fragment rather than a page.
func isHTMX(c *gin.Context) bool {
	if c.GetHeader(hxHistoryRestoreHeader) == "true" {
		return false
	}
	return c.GetHeader(hxRequestHeader) == "true"
}

// notice is a message an htmx response carries alongside its fragment.
//
// The problem it solves: a swap targets ONE card, and "you cannot vote on your
// own avatar" does not belong inside that card. Before htmx these messages
// travelled as ?err= on a redirect and the reloaded page rendered them; a
// fragment response has no reload to carry them.
//
// Constructed only through noticeOK/noticeErr/noticeInfo so the kind and the
// icon cannot disagree — a red notice with a tick in it is the kind of thing
// that survives review because each half looks right on its own.
type notice struct {
	Kind string // maps to .notice--<kind>
	Icon string // sprite id
	Text string
}

func noticeOK(text string) notice   { return notice{Kind: "success", Icon: "check", Text: text} }
func noticeErr(text string) notice  { return notice{Kind: "danger", Icon: "info", Text: text} }
func noticeInfo(text string) notice { return notice{Kind: "info", Icon: "info", Text: text} }

// statusRefused is the status a REFUSED action answers with.
//
// Not 200, which would tell every cache and scripted client that a rejected
// vote was accepted, and not 400: the request was well-formed and the server
// understood it, it was simply not allowed to happen. 422 also has a specific
// meaning to htmx here — site_chrome.html configures it as a code that still
// SWAPS, because the response body is the explanation and refusing to render it
// would leave the member with a dead button and no reason.
const statusRefused = http.StatusUnprocessableEntity

// shellPage names a page whose template set is used to render a fragment that
// is defined in the SHELL — base.html or site_chrome.html — rather than in a
// page of its own.
//
// Every set parses the shell, so literally any page name would work, and that
// is the awkwardness this constant exists to make visible rather than hide at a
// call site. home.html is named because it is the one page that cannot be
// deleted without the site losing its root, so the arbitrary choice is at least
// the most stable one available.
const shellPage = "home.html"

// renderFragment writes ONE named template from a page's set.
//
// Not renderStatus with a different template name: that executes "base.html"
// and would wrap the fragment in the entire site chrome, so the swap would
// paste a second navbar into the middle of the page. This executes the named
// define and nothing else.
//
// The data map is the caller's, plus CSRFToken — a fragment containing a form
// needs one, and the whole point of a shared partial is that it renders the
// same from the page and from here. Forgetting it would produce a button that
// works once and then 403s, which is exactly the class of bug the prod tree
// has and the reason this helper exists at all.
func (w *web) renderFragment(c *gin.Context, page, fragment string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	if _, ok := data["CSRFToken"]; !ok {
		data["CSRFToken"] = middleware.Token(c)
	}

	t, ok := w.fragmentSet(c, page)
	if !ok {
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(http.StatusOK)
	if err := t.ExecuteTemplate(c.Writer, fragment, data); err != nil {
		w.log.Error("render fragment", "page", page, "fragment", fragment, "err", err)
	}
}

// renderFragmentWithNotice writes a fragment AND a message, in one response.
//
// The notice goes out FIRST and out-of-band: htmx applies an hx-swap-oob
// element by its own id rather than into the request's target, so one response
// updates the row that was clicked and the notice region above it.
//
// Ordering matters and is not arbitrary — htmx processes out-of-band elements
// before swapping the remainder into the target, and putting the notice last
// makes it part of the fragment on some swap styles, which would paste a site
// notice inside a table row.
func (w *web) renderFragmentWithNotice(c *gin.Context, status int, page, fragment string, data map[string]any, n notice) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(status)
	t, ok := w.fragmentSet(c, page)
	if !ok {
		return
	}
	if err := t.ExecuteTemplate(c.Writer, "oob-notice", n); err != nil {
		w.log.Error("render notice", "page", page, "err", err)
		return
	}
	if fragment == "" {
		return // notice only: nothing about the page changed
	}
	if data == nil {
		data = map[string]any{}
	}
	if _, has := data["CSRFToken"]; !has {
		data["CSRFToken"] = middleware.Token(c)
	}
	if err := t.ExecuteTemplate(c.Writer, fragment, data); err != nil {
		w.log.Error("render fragment", "page", page, "fragment", fragment, "err", err)
	}
}

// renderRefusal is the common case: nothing changed, and the member needs to be
// told why.
//
// It answers 422 rather than 200 — see statusRefused. Kept as its own function
// because "refused" is the outcome a handler is most likely to render with the
// wrong status, there being no visible difference in a browser either way.
func (w *web) renderRefusal(c *gin.Context, page, text string) {
	w.renderFragmentWithNotice(c, statusRefused, page, "", nil, noticeErr(text))
}

// fragmentSet resolves a page's template set, re-reading from disk in dev.
// Shared by renderFragment and renderFragmentWithNotice so the two cannot
// diverge on which set they execute against.
func (w *web) fragmentSet(c *gin.Context, page string) (*template.Template, bool) {
	if site.DevReload {
		fresh, err := template.New(page).Funcs(w.tmplFuncs()).ParseFS(site.FS, pageFiles(page)...)
		if err != nil {
			w.log.Error("fragment parse", "page", page, "err", err)
			c.String(http.StatusInternalServerError, "template %s: %v", page, err)
			return nil, false
		}
		return fresh, true
	}
	t := w.tmpls[page]
	if t == nil {
		c.String(http.StatusInternalServerError, "unknown page %q", page)
		return nil, false
	}
	return t, true
}
