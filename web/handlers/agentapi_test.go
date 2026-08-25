package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// agentCtx builds a gin context around a request, for the endpoint tests.
func agentCtx(rec *httptest.ResponseRecorder, r *http.Request) *gin.Context {
	c, _ := gin.CreateTestContext(rec)
	c.Request = r
	return c
}

// Registration is off until the master AGENT_TOKEN is set, and rejects a wrong
// master token when it is on -- the endpoint that MINTS credentials must not be
// open.
func TestAgentRegisterIsOptInAndAuthenticated(t *testing.T) {
	// Off: no master token configured -> 503 regardless of what is sent.
	w := &web{}
	rec := httptest.NewRecorder()
	c := agentCtx(rec, agentPostReq("/api/agent/register", "Bearer anything", `{"agent":"x"}`))
	w.agentRegister(c)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no master token -> want 503, got %d", rec.Code)
	}

	// On but wrong master token -> 401.
	w2 := &web{agentToken: "secret"}
	rec = httptest.NewRecorder()
	c = agentCtx(rec, agentPostReq("/api/agent/register", "Bearer wrong", `{"agent":"x"}`))
	w2.agentRegister(c)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong master token -> want 401, got %d", rec.Code)
	}

	// A missing Authorization header is not authorized either.
	rec = httptest.NewRecorder()
	c = agentCtx(rec, agentPostReq("/api/agent/register", "", `{"agent":"x"}`))
	w2.agentRegister(c)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no header -> want 401, got %d", rec.Code)
	}
}

// A protocol verb with no bearer token is rejected before any store lookup --
// the token IS the identity, so its absence is an immediate 401.
func TestAgentVerbRejectsMissingToken(t *testing.T) {
	w := &web{}
	for _, verb := range []struct {
		name string
		h    func(*gin.Context)
	}{
		{"poll", w.agentPoll},
		{"progress", w.agentProgress},
		{"status", w.agentStatus},
		{"complete", w.agentComplete},
	} {
		rec := httptest.NewRecorder()
		c := agentCtx(rec, agentPostReq("/api/agent/"+verb.name, "", `{}`))
		verb.h(c)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s with no token -> want 401, got %d", verb.name, rec.Code)
		}
	}
}

// bearerToken tolerates a bare token or the Bearer prefix; bearerEquals never
// accepts an empty token as a match for an empty want.
func TestBearerHelpers(t *testing.T) {
	rec := httptest.NewRecorder()
	if got := bearerToken(agentCtx(rec, agentPostReq("/x", "Bearer secret", ""))); got != "secret" {
		t.Fatalf("Bearer <token> -> want secret, got %q", got)
	}
	if got := bearerToken(agentCtx(rec, agentPostReq("/x", "secret", ""))); got != "secret" {
		t.Fatalf("bare token -> want secret, got %q", got)
	}
	if !bearerEquals(agentCtx(rec, agentPostReq("/x", "Bearer secret", "")), "secret") {
		t.Fatal("Bearer <token> should equal the want")
	}
	if bearerEquals(agentCtx(rec, agentPostReq("/x", "Bearer ", "")), "") {
		t.Fatal("an empty token must never match an empty want")
	}
}

func agentPostReq(path, auth, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	return r
}
