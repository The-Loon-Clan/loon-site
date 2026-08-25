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

// The report endpoint is off until AGENT_TOKEN is set, and rejects a bad token
// when it is on -- an endpoint that WRITES must not be open.
func TestAgentReportIsOptInAndAuthenticated(t *testing.T) {
	// Off: no token configured -> 503 regardless of what is sent.
	w := &web{}
	rec := httptest.NewRecorder()
	c := agentCtx(rec, agentPostReq("Bearer anything", `{"agent":"x"}`))
	w.agentReport(c)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no token -> want 503, got %d", rec.Code)
	}

	// On but wrong token -> 401.
	w2 := &web{agentToken: "secret"}
	rec = httptest.NewRecorder()
	c = agentCtx(rec, agentPostReq("Bearer wrong", `{"agent":"x"}`))
	w2.agentReport(c)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token -> want 401, got %d", rec.Code)
	}

	// A missing Authorization header is not authorized either.
	rec = httptest.NewRecorder()
	c = agentCtx(rec, agentPostReq("", `{"agent":"x"}`))
	w2.agentReport(c)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no header -> want 401, got %d", rec.Code)
	}
}

// agentAuthorized tolerates a bare token or the Bearer prefix, and never
// accepts an empty token as a match for an empty want.
func TestAgentAuthorizedTolerantButStrict(t *testing.T) {
	rec := httptest.NewRecorder()
	if !agentAuthorized(agentCtx(rec, agentPostReq("Bearer secret", "")), "secret") {
		t.Fatal("Bearer <token> should authorize")
	}
	if !agentAuthorized(agentCtx(rec, agentPostReq("secret", "")), "secret") {
		t.Fatal("a bare token should authorize")
	}
	if agentAuthorized(agentCtx(rec, agentPostReq("Bearer ", "")), "") {
		t.Fatal("an empty token must never match an empty want")
	}
}

func agentPostReq(auth, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/agent/report", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	return r
}
