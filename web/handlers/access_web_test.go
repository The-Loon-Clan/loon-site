package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// ginTestContext builds the minimum a handler needs to write a response.
//
// robotsTxt touches no store and no session — it reads one atomic and writes a
// body — so it needs a writer and a request and nothing else.
func ginTestContext(rec *httptest.ResponseRecorder, req *http.Request) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	return c
}

// Who may read this site, and who may join it.
//
// This file was at 0%. It decides whether a site an operator set to
// members-only is actually members-only, and the failure mode is silent in the
// worst direction: nothing errors, no page looks broken, the catalogue is
// simply readable by anyone — and by every crawler, which is how it stops being
// recoverable.

// withBrowsing sets the live mirror for one test and puts it back afterwards.
// These are process-wide atomics read on every request, so a test that leaves
// one flipped changes the answer for every test that runs after it.
func withBrowsing(t *testing.T, mode string) {
	t.Helper()
	prev := browsingMode()
	browseMode.Store(mode)
	t.Cleanup(func() { browseMode.Store(prev) })
}

func TestAPrivateSiteTurnsAnonymousVisitorsAway(t *testing.T) {
	for _, path := range []string{"/", "/browse", "/release/12", "/u/alice", "/search"} {
		allow, to := browsingGate(BrowseMembers, path, path, false)
		if allow {
			t.Errorf("members-only: %s was allowed to an anonymous visitor", path)
		}
		if !strings.HasPrefix(to, "/login?next=") {
			t.Errorf("members-only: %s redirected to %q, want the door with a next", path, to)
		}
	}
}

func TestTheDoorRemembersWhereYouWereGoingIncludingTheQuery(t *testing.T) {
	// A deep link is the case that matters: turning somebody away and then
	// dropping them on the home page has lost the thing they came for, and a
	// path-only next silently loses the filters — which reads as the site
	// forgetting rather than the redirect being wrong.
	_, to := browsingGate(BrowseMembers, "/browse", "/browse?cat=2&q=blade+runner", false)

	// %2B, not a bare +. The plus in the incoming URI is a literal character
	// and has to survive as one: escaping it to + would have it decoded back as
	// a SPACE at the door, and the member would arrive at a search for a
	// different phrase than the one they clicked. This expectation was wrong
	// the first time it was written, in exactly that way.
	const want = "/login?next=%2Fbrowse%3Fcat%3D2%26q%3Dblade%2Brunner"
	if to != want {
		t.Errorf("redirect = %q, want %q", to, want)
	}

	// The property behind the literal: unescaping returns precisely what came in.
	got, err := url.QueryUnescape(strings.TrimPrefix(to, "/login?next="))
	if err != nil {
		t.Fatalf("next= does not decode: %v", err)
	}
	if got != "/browse?cat=2&q=blade+runner" {
		t.Errorf("next= round-tripped to %q, want the original URI", got)
	}
}

func TestASignedInMemberPassesOnAPrivateSite(t *testing.T) {
	if allow, to := browsingGate(BrowseMembers, "/browse", "/browse", true); !allow {
		t.Errorf("a signed-in member was turned away, to %q", to)
	}
}

func TestAPublicSiteLetsAnonymousVisitorsRead(t *testing.T) {
	for _, path := range []string{"/", "/browse", "/release/12"} {
		if allow, _ := browsingGate(BrowsePublic, path, path, false); !allow {
			t.Errorf("public: %s was refused to an anonymous visitor", path)
		}
	}
}

func TestTheDoorsStayOpenOnAPrivateSite(t *testing.T) {
	// You cannot log in through a gate that requires you to be logged in. This
	// is the list that keeps a private site from locking everybody out
	// permanently, including its own operator.
	for _, path := range []string{
		"/login", "/logout", "/register", "/forgot", "/reset",
		"/healthz", "/robots.txt", "/favicon.ico",
		"/static/css/theme.css", "/verify/abc123",
	} {
		if allow, _ := browsingGate(BrowseMembers, path, path, false); !allow {
			t.Errorf("members-only: %s was gated, and it must not be", path)
		}
	}
}

func TestTheKeyedEndpointsStayReachableOnAPrivateSite(t *testing.T) {
	// /api and /rss authenticate with an api key, not a session. Gating them on
	// a session would break every downloader the moment the site went private —
	// the opposite of what "members only" means for a tool a member already
	// authorised, and it would look like the client's fault.
	for _, path := range []string{"/api", "/api/v1/caps", "/rss", "/rss/tv"} {
		if allow, _ := browsingGate(BrowseMembers, path, path, false); !allow {
			t.Errorf("members-only: %s was gated; keyed clients would all break", path)
		}
	}
}

func TestTheExemptionListIsNotWiderThanItLooks(t *testing.T) {
	// The exemption must be granted by a path segment, not by spelling.
	//
	// alwaysPublic used to match /api and /rss as bare prefixes, so /apikeys
	// and /rssfeed were exempt as well. No route was called either of those, so
	// nothing was open — but the exemption was being handed out by string
	// coincidence rather than by anybody deciding, and a route landing on the
	// wrong side of that line would be readable on a members-only site with no
	// symptom at all. This test found it, which is what it was written for.
	for _, path := range []string{"/apikeys", "/api-docs", "/rssfeed", "/apidocs", "/staticfiles"} {
		if alwaysPublic(path) {
			t.Errorf("%s is exempt from the members-only gate by prefix alone — "+
				"alwaysPublic must match a whole segment", path)
		}
	}
	// …while everything that really is under those prefixes still passes.
	for _, path := range []string{
		"/api", "/api/", "/api/chat/recent", "/api/btcpay/webhook",
		"/rss", "/rss/tv", "/static/css/theme.css", "/verify/abc",
	} {
		if !alwaysPublic(path) {
			t.Errorf("%s must stay reachable: tightening the match broke a real endpoint", path)
		}
	}
}

func TestNothingElseIsExempt(t *testing.T) {
	// The other direction: the pages that carry a member's own data must never
	// appear in the exemption list, whatever else is added to it.
	for _, path := range []string{
		"/", "/browse", "/settings/security", "/bookmarks", "/admin",
		"/moderation", "/invites", "/wishlist", "/u/alice",
	} {
		if alwaysPublic(path) {
			t.Errorf("%s is exempt from the members-only gate and must not be", path)
		}
	}
}

func TestAPrivateSiteTellsCrawlersToLeave(t *testing.T) {
	// A members-only site that still invites crawlers is not members-only. This
	// is the difference between a private catalogue and a private catalogue
	// with a public index of everything in it.
	withBrowsing(t, BrowseMembers)

	body := robotsBody(t)
	if !strings.Contains(body, "Disallow: /") {
		t.Errorf("members-only robots.txt does not disallow everything:\n%s", body)
	}
	if strings.Contains(body, "Allow: /") {
		t.Errorf("members-only robots.txt still contains an Allow rule:\n%s", body)
	}
	if strings.Contains(body, "Sitemap:") {
		t.Errorf("members-only robots.txt still advertises a sitemap:\n%s", body)
	}
}

func TestAPublicSiteKeepsTheAccountAreaOutOfTheIndex(t *testing.T) {
	// Public does not mean every page is worth crawling: these answer
	// differently for every reader or need a credential, so indexing them
	// spends a crawl budget on pages no two visitors see the same way.
	withBrowsing(t, BrowsePublic)

	body := robotsBody(t)
	for _, want := range []string{
		"Disallow: /admin/", "Disallow: /settings/", "Disallow: /bookmarks",
		"Disallow: /api", "Disallow: /rss", "Allow: /", "Sitemap:",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("public robots.txt is missing %q:\n%s", want, body)
		}
	}
}

func robotsBody(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	c := ginTestContext(rec, httptest.NewRequest(http.MethodGet, "/robots.txt", nil))
	(&web{}).robotsTxt(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("robots.txt = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("robots.txt Content-Type = %q, want text/plain", ct)
	}
	return rec.Body.String()
}

func TestOnlyKnownModesAreValid(t *testing.T) {
	// An unknown mode is a bug in a form, not a state to adopt. Adopting one
	// leaves the site in a mode nothing enforces, and nothing enforcing a mode
	// reads exactly like "public".
	for _, s := range []string{RegOpen, RegInvite, RegClosed} {
		if !validReg(s) {
			t.Errorf("validReg(%q) = false", s)
		}
	}
	for _, s := range []string{"", "OPEN", "Open", "public", "yes", "1", "invite-only"} {
		if validReg(s) {
			t.Errorf("validReg(%q) = true, and nothing enforces that mode", s)
		}
	}
	for _, s := range []string{BrowsePublic, BrowseMembers} {
		if !validBrowse(s) {
			t.Errorf("validBrowse(%q) = false", s)
		}
	}
	for _, s := range []string{"", "PUBLIC", "member", "private", "closed", "open"} {
		if validBrowse(s) {
			t.Errorf("validBrowse(%q) = true, and nothing enforces that mode", s)
		}
	}
}

func TestPrivateIsNotSpeltPrivate(t *testing.T) {
	// The word an operator would reach for first is not the word this uses, and
	// a rejected save is far better than a mode that silently means public. The
	// pairing is deliberate: browsing is public|members, and "private" belongs
	// to neither vocabulary.
	if validBrowse("private") || validReg("private") {
		t.Error(`"private" is accepted somewhere; it is not one of the modes`)
	}
}

func TestTheDefaultsAreTheOpenOnes(t *testing.T) {
	// Deliberately open by default: this is a demo host that has to be readable
	// out of the box. The test exists so the default is a decision somebody
	// made rather than whatever a zero value happened to be — a site that
	// defaulted to members-only would appear broken on first boot, and one that
	// defaulted to a mode with no enforcement would appear fine.
	if registrationMode() != RegOpen {
		t.Errorf("registration default = %q, want %q", registrationMode(), RegOpen)
	}
	if browsingMode() != BrowsePublic {
		t.Errorf("browsing default = %q, want %q", browsingMode(), BrowsePublic)
	}
}

func TestAnEmptyMirrorReadsAsTheDefault(t *testing.T) {
	// The mirror is an atomic.Value holding a string. A boot that read an empty
	// setting must not leave the site in "" — which is neither mode, so nothing
	// matches it and the gate falls open.
	withBrowsing(t, "")
	if browsingMode() != BrowsePublic {
		t.Errorf("empty browsing mirror = %q, want %q", browsingMode(), BrowsePublic)
	}

	prev := registrationMode()
	regMode.Store("")
	t.Cleanup(func() { regMode.Store(prev) })
	if registrationMode() != RegOpen {
		t.Errorf("empty registration mirror = %q, want %q", registrationMode(), RegOpen)
	}
}

func TestAnUnenforceableModeIsNeverAdopted(t *testing.T) {
	// saveAccessSettings validates BEFORE it writes or mirrors. The order is
	// the point: mirroring first and failing the write would leave the running
	// process in a mode the database does not agree with, which survives until
	// a restart silently changes the site back.
	withBrowsing(t, BrowseMembers)

	if err := saveAccessSettings(t.Context(), "nonsense", BrowseMembers); err == nil {
		t.Error("an unknown registration mode was accepted")
	}
	if err := saveAccessSettings(t.Context(), RegOpen, "nonsense"); err == nil {
		t.Error("an unknown browsing mode was accepted")
	}
	if browsingMode() != BrowseMembers {
		t.Errorf("a rejected save still changed the live mode to %q — "+
			"the site is now readable when it was not", browsingMode())
	}
}
