package handlers

import (
	"bytes"
	"compress/gzip"
	"mime/multipart"
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

// The real agent GZIPS its completion. loon-agent's postGzippedWith compresses
// the multipart body and sets Content-Encoding: gzip beside the multipart
// Content-Type, and Go's net/http never decodes a REQUEST body — so before
// gunzipAgentBody the parser read gzip bytes, found no fields, and every
// completion arrived as lock_id 0: the task was never closed, its lease
// expired, and the same release was re-dispatched and re-posted to Usenet on a
// loop while the agent's completed counter climbed.
//
// Pinned here because the MOCK CANNOT CATCH IT — it posts JSON, the one dialect
// that always worked — so this bug is invisible to the rig and only a test
// written against the real client's encoding keeps it fixed.
func TestAgentCompleteReadsGzippedMultipart(t *testing.T) {
	body, ctype := gzippedCompleteForm(t, "42", "completed")
	r := httptest.NewRequest(http.MethodPost, "/api/agent/complete", bytes.NewReader(body))
	r.Header.Set("Content-Type", ctype)
	r.Header.Set("Content-Encoding", "gzip")

	c := agentCtx(httptest.NewRecorder(), r)
	gunzipAgentBody()(c)

	lockID, status, _ := agentCompleteFields(c)
	if lockID != 42 {
		t.Errorf("lock_id = %d, want 42 — a gzipped completion closes no task, so the "+
			"release is handed out and re-uploaded forever", lockID)
	}
	if status != "completed" {
		t.Errorf("status = %q, want \"completed\" — an unread status reads as success, "+
			"so even a FAILED grab would count as an upload", status)
	}
}

// The control that gives the test above its meaning: the same gzip bytes with
// the header absent must still parse to nothing. Without this, a test that
// passed because gin had somehow started decoding on its own would look like
// proof that the middleware works.
func TestAgentCompleteUndeclaredGzipStillParsesToNothing(t *testing.T) {
	body, ctype := gzippedCompleteForm(t, "42", "completed")
	r := httptest.NewRequest(http.MethodPost, "/api/agent/complete", bytes.NewReader(body))
	r.Header.Set("Content-Type", ctype) // no Content-Encoding

	c := agentCtx(httptest.NewRecorder(), r)
	gunzipAgentBody()(c)

	if lockID, _, _ := agentCompleteFields(c); lockID != 0 {
		t.Errorf("lock_id = %d from undeclared gzip bytes, want 0 — this control is what "+
			"proves the decode in the test above is load-bearing", lockID)
	}
}

// The JSON dialect the mock speaks must keep working alongside the gzipped
// multipart one. Reading only one of the two would either break the rig or
// fail the real-client test the runtime exists to pass.
func TestAgentCompleteStillReadsJSON(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/agent/complete",
		strings.NewReader(`{"lock_id":7,"status":"completed"}`))
	r.Header.Set("Content-Type", "application/json")

	c := agentCtx(httptest.NewRecorder(), r)
	gunzipAgentBody()(c)

	lockID, status, _ := agentCompleteFields(c)
	if lockID != 7 || status != "completed" {
		t.Errorf("JSON completion parsed as lock_id=%d status=%q, want 7/completed", lockID, status)
	}
}

// The 426 body's KEYS are the contract: the client reads min_protocol, and a
// body that says "min" unmarshals into nothing, so the operator is told the
// site needs "protocol v0" — a version that has never existed.
func TestUpgradeRequiredUsesTheKeysTheClientReads(t *testing.T) {
	b := upgradeRequiredBody()
	if _, ok := b["min_protocol"]; !ok {
		t.Error("no min_protocol key — loon-agent's parseUpgradeRequired reads that " +
			"exact name and would report the floor as 0")
	}
	if got := b["min_protocol"]; got != minAgentProtocol {
		t.Errorf("min_protocol = %v, want %d", got, minAgentProtocol)
	}
	if _, ok := b["message"]; !ok {
		t.Error("no message key — the client falls back to error, but message is what " +
			"it prefers to show the operator")
	}
}

// gzippedCompleteForm builds a completion exactly as the real client sends it:
// a multipart form, gzipped whole.
func gzippedCompleteForm(t *testing.T, lockID, status string) (body []byte, contentType string) {
	t.Helper()
	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	for k, v := range map[string]string{"lock_id": lockID, "status": status} {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatalf("write field %s: %v", k, err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(form.Bytes()); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return gz.Bytes(), mw.FormDataContentType()
}
