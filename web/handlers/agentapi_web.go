package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-site/internal/storage"
)

// The fleet agent runtime's write surface: where an agent reports its state.
//
// This is the endpoint a real agent will POST to, shaped now so the mock
// client that drives the UI and the eventual real client speak the SAME
// contract -- the point of building it against the real API rather than
// seeding rows is that testing the real client later reveals gaps in THIS,
// not in a throwaway mock. The agent PLUGIN renders read-only surfaces over
// what lands here; see internal/storage/agents.go.
//
// OPT-IN AND AUTHENTICATED. An endpoint that writes must not be open. It is
// live only when the operator sets AGENT_TOKEN; unset, it answers 503, so a
// host that has not chosen to run a fleet exposes no write surface at all. The
// token is a shared bearer the operator gives its agents -- per-agent
// registration is production's concern, not this debug runtime's.

// agentReportBody is one heartbeat, as an agent posts it. Field names are the
// wire contract -- keep them stable for the real client.
type agentReportBody struct {
	Agent      string `json:"agent"`      // required: the worker's name
	User       string `json:"user"`       // owner username, resolved to an id
	Phase      string `json:"phase"`      // downloading|uploading|assembling|idle
	RequestID  int64  `json:"request_id"` // the request being fulfilled
	TaskTitle  string `json:"task_title"` // what is being fetched
	Progress   int    `json:"progress"`   // 0..100
	Detail     string `json:"detail"`     // "12 of 45 segments", free text
	Downloaded int64  `json:"downloaded"` // lifetime count
	Uploaded   int64  `json:"uploaded"`   // lifetime count
}

// agentReport receives one heartbeat.
func (w *web) agentReport(c *gin.Context) {
	if w.agentToken == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent runtime not configured (set AGENT_TOKEN)"})
		return
	}
	if !agentAuthorized(c, w.agentToken) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "bad or missing agent token"})
		return
	}
	var body agentReportBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "malformed JSON"})
		return
	}
	name := strings.TrimSpace(body.Agent)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "agent name is required"})
		return
	}
	ctx := c.Request.Context()
	userID := w.data.UserIDByUsername(ctx, strings.TrimSpace(body.User))
	if err := w.data.UpsertAgentReport(ctx, storage.AgentReport{
		Name:        name,
		Username:    body.User,
		Phase:       strings.ToLower(strings.TrimSpace(body.Phase)),
		RequestID:   body.RequestID,
		TaskTitle:   strings.TrimSpace(body.TaskTitle),
		ProgressPct: body.Progress,
		Detail:      strings.TrimSpace(body.Detail),
		Downloaded:  body.Downloaded,
		Uploaded:    body.Uploaded,
	}, userID); err != nil {
		w.log.Error("agent report", "agent", name, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not record report"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// agentAuthorized checks the shared bearer token, tolerant of the "Bearer "
// prefix or a bare token.
func agentAuthorized(c *gin.Context, want string) bool {
	h := strings.TrimSpace(c.GetHeader("Authorization"))
	h = strings.TrimSpace(strings.TrimPrefix(h, "Bearer"))
	return h != "" && h == want
}
