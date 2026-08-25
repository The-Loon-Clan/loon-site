package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-site/internal/storage"
)

// The fleet agent runtime's API, aligned to production's split-verb contract
// (loon-agent, X-Agent-Protocol v3): an agent POLLs for work, POSTs a rich
// live status and lightweight progress, and COMPLETEs an upload -- each
// authenticated by a PER-AGENT bearer token so one worker can be attributed
// and revoked. This is the debug surface of that contract; a real agent binary
// pointed here speaks the same verbs and shapes.
//
// TWO kinds of token. The per-agent token (minted per worker) authenticates
// the protocol verbs. A separate MASTER token, AGENT_TOKEN, gates ONE endpoint
// -- /register -- which mints a per-agent token for a named worker. Production
// provisions agents through its admin UI; the demo also offers this register
// bootstrap so the mock client (and a real client under test) can self-provision
// without a human minting a token first. Unset AGENT_TOKEN disables register
// AND is the signal the whole runtime is opt-out; the protocol verbs still work
// for any agent an admin created.

// minAgentProtocol is the oldest protocol version this runtime accepts. Prod's
// rule is additive-with-omitempty, so an older agent is refused rather than
// mis-parsed.
const minAgentProtocol = 3

// agentRegisterBody bootstraps a worker: name + owner, authenticated by the
// master token, returns a per-agent token.
type agentRegisterBody struct {
	Agent string `json:"agent"`
	User  string `json:"user"`
}

// agentRegister mints (or returns) a per-agent token. Master-token gated.
func (w *web) agentRegister(c *gin.Context) {
	if w.agentToken == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent provisioning off (set AGENT_TOKEN)"})
		return
	}
	if !bearerEquals(c, w.agentToken) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "bad master token"})
		return
	}
	var body agentRegisterBody
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Agent) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "agent name is required"})
		return
	}
	ctx := c.Request.Context()
	userID := w.data.UserIDByUsername(ctx, strings.TrimSpace(body.User))
	// tok is the plaintext, alive only in this response: the store keeps its
	// hash. Re-registering a known name re-mints (see EnsureAgent), so a
	// client that registers every boot always holds the one live credential.
	a, tok, err := w.data.EnsureAgent(ctx, strings.TrimSpace(body.Agent), userID)
	if err != nil {
		w.log.Error("register agent", "agent", body.Agent, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not register"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"agent": a.Name, "token": tok})
}

// authAgent resolves the per-agent token and checks the protocol version. It
// returns the agent and writes the error response itself on failure.
func (w *web) authAgent(c *gin.Context) (storage.Agent, bool) {
	tok := bearerToken(c)
	if tok == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing agent token"})
		return storage.Agent{}, false
	}
	a, ok, err := w.data.AgentByToken(c.Request.Context(), tok)
	if err != nil || !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unknown agent token"})
		return storage.Agent{}, false
	}
	// X-Agent-Protocol is how prod refuses an agent too old to trust; below the
	// floor is a 426, the code prod uses for "upgrade required".
	if p, _ := strconv.Atoi(c.GetHeader("X-Agent-Protocol")); p != 0 && p < minAgentProtocol {
		c.JSON(http.StatusUpgradeRequired, gin.H{"error": "agent protocol too old", "min": minAgentProtocol})
		return storage.Agent{}, false
	}
	return a, true
}

// agentPoll answers "is there work for me". The demo dispatches none -- the
// mock invents its own tasks -- so this is an empty queue, which is a valid,
// common answer a real agent handles (it just sleeps and polls again).
func (w *web) agentPoll(c *gin.Context) {
	if _, ok := w.authAgent(c); !ok {
		return
	}
	// 204: no work. A real client reads this as "nothing queued".
	c.Status(http.StatusNoContent)
}

// agentProgressBody is the lightweight per-lock ping between statuses.
type agentProgressBody struct {
	LockID   int    `json:"lock_id"`
	Progress string `json:"progress"`
	Speed    string `json:"speed"`
	Warnings string `json:"warnings"`
}

// agentProgress records a fast progress ping (keeps the agent marked online).
func (w *web) agentProgress(c *gin.Context) {
	a, ok := w.authAgent(c)
	if !ok {
		return
	}
	var body agentProgressBody
	_ = c.ShouldBindJSON(&body) // fields optional; the ping itself is the signal
	if err := w.data.TouchAgent(c.Request.Context(), a.ID); err != nil {
		w.log.Error("agent progress", "agent", a.ID, "err", err)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// agentStatus records the rich live snapshot the dashboard renders.
func (w *web) agentStatus(c *gin.Context) {
	a, ok := w.authAgent(c)
	if !ok {
		return
	}
	var s storage.AgentLiveStatus
	if err := c.ShouldBindJSON(&s); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "malformed status"})
		return
	}
	protocol, _ := strconv.Atoi(c.GetHeader("X-Agent-Protocol"))
	version := c.GetHeader("X-Agent-Version")
	if err := w.data.RecordStatus(c.Request.Context(), a.ID, protocol, version, s); err != nil {
		w.log.Error("agent status", "agent", a.ID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not record status"})
		return
	}
	// The status response can carry a cancel command; the demo never cancels.
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// agentComplete records a finished upload. Production's /complete is a
// multipart bundle (NZB + screenshots + metadata + subtitles); the demo does
// not ingest, so it accepts the call, bumps the counter, and returns the
// completed verdict a client expects.
func (w *web) agentComplete(c *gin.Context) {
	a, ok := w.authAgent(c)
	if !ok {
		return
	}
	if err := w.data.CompleteTask(c.Request.Context(), a.ID); err != nil {
		w.log.Error("agent complete", "agent", a.ID, "err", err)
	}
	c.JSON(http.StatusOK, gin.H{"status": "completed"})
}

// bearerToken returns the token from an Authorization: Bearer header, or "".
func bearerToken(c *gin.Context) string {
	h := strings.TrimSpace(c.GetHeader("Authorization"))
	return strings.TrimSpace(strings.TrimPrefix(h, "Bearer"))
}

// bearerEquals reports whether the request's bearer token equals want, never
// treating an empty token as a match for an empty want.
func bearerEquals(c *gin.Context, want string) bool {
	t := bearerToken(c)
	return t != "" && t == want
}
