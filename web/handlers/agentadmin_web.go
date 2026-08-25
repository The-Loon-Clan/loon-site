package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-site/internal/storage"
)

// The fleet operator's view: every agent, its settings, its credentials, and
// what each one is doing right now.
//
// This is the "agent page" prod has, rebuilt over the demo's runtime so the UI
// can be driven and debugged without a real agent (the mock client POSTs the
// same protocol). It reads the same rows the plugin's fleet card reads, but
// WHOLE: the card is one member's public glance, this is the operator's roster
// with the owner, the live status an agent posts (VPN, public IP, transfer
// speeds, disk, per-file progress), lifetime completions, the concurrency
// setting they change, and the per-agent token they mint and rotate.

// agentFileRow is one file in flight within an agent's status, table-ready.
type agentFileRow struct {
	Name     string
	Size     string
	Percent  int
	Bucket   int // Percent rounded to nearest 10, for a width class
	Phase    string
	Speed    string
	UpSpeed  string
	Peers    int
	Warnings []string
}

// agentRow is one agent as the admin table shows it.
type agentRow struct {
	ID        int64
	Name      string
	Owner     string
	Online    bool
	LastSeen  string
	Max       int
	Protocol  string
	Version   string
	Completed int64
	TokenTail string // last 6 chars, so the operator can tell tokens apart

	HasTask bool
	Phase   string
	Title   string

	// The live status snapshot, flattened for the template.
	VPNStatus     string
	PublicIP      string
	DownloadSpeed string
	UploadSpeed   string
	NzbSpeed      string
	DiskFreeGB    string
	SeedingCount  int
	Files         []agentFileRow
}

type agentsVM struct {
	Rows        []agentRow
	Online      int
	Total       int
	MaxDefault  int
	TokenSet    bool
	RegisterURL string
	StatusURL   string
	// NewToken is set once, right after a create or rotate, so the operator can
	// copy the freshly minted token (never stored in the clear anywhere else).
	NewToken     string
	NewTokenName string
}

func (w *web) adminAgents(c *gin.Context) {
	w.renderAgents(c, "", "")
}

// renderAgents draws the roster, optionally surfacing a just-minted token.
func (w *web) renderAgents(c *gin.Context, newTokenName, newToken string) {
	vm := w.agentsVM(c.Request.Context())
	vm.TokenSet = w.agentToken != ""
	base := requestBaseURL(c)
	vm.RegisterURL = base + "/api/agent/register"
	vm.StatusURL = base + "/api/agent/status"
	vm.NewTokenName = newTokenName
	vm.NewToken = newToken
	w.render(c, "admin_agents.html", map[string]any{
		"Title": "Agents",
		"VM":    vm,
	})
}

func (w *web) agentsVM(ctx context.Context) agentsVM {
	vm := agentsVM{MaxDefault: hostMaxConcurrent()}
	if w.data == nil {
		return vm
	}
	agents, err := w.data.AllAgents(ctx)
	if err != nil {
		w.log.Error("list agents", "err", err)
		return vm
	}
	now := time.Now()
	online := 0
	for _, a := range agents {
		row := w.agentRowFrom(ctx, a, now)
		if row.Online {
			online++
		}
		vm.Rows = append(vm.Rows, row)
	}
	vm.Online, vm.Total = online, len(agents)
	return vm
}

// agentRowFrom flattens one stored agent (identity + last status snapshot) into
// the table row.
func (w *web) agentRowFrom(ctx context.Context, a storage.Agent, now time.Time) agentRow {
	row := agentRow{
		ID: a.ID, Name: a.Name, Max: a.MaxConcurrent, Completed: a.Completed,
		// The tail of the HASH, not of a token: the plaintext is gone the
		// moment it was shown. Still enough to tell two credentials apart in
		// the roster, which is all this column is for.
		TokenTail: tokenTail(a.TokenHash),
	}
	if a.LastSeenAt.Valid {
		row.LastSeen = humanAge(now.Sub(a.LastSeenAt.Time))
		row.Online = now.Sub(a.LastSeenAt.Time) <= agentOnlineWindow
	} else {
		row.LastSeen = "never"
	}
	if a.UserID.Valid {
		row.Owner = w.data.UsernameByID(ctx, a.UserID.Int64)
	}
	if a.Protocol.Valid {
		row.Protocol = "v" + strconv.FormatInt(a.Protocol.Int64, 10)
	}
	if a.Version.Valid {
		row.Version = a.Version.String
	}

	s := a.Status()
	row.VPNStatus = s.VPNStatus
	row.PublicIP = s.PublicIP
	row.DownloadSpeed = s.DownloadSpeed
	row.UploadSpeed = s.UploadSpeed
	row.NzbSpeed = s.NzbUploadSpeed
	row.SeedingCount = s.SeedingCount
	if s.DiskFreeGB > 0 {
		row.DiskFreeGB = fmt.Sprintf("%.0f GB", s.DiskFreeGB)
	}
	if a.Phase.Valid && strings.TrimSpace(a.Phase.String) != "" && !strings.EqualFold(a.Phase.String, "idle") {
		row.HasTask = true
		row.Phase = a.Phase.String
		row.Title = s.TaskTitle
	}
	for _, f := range s.Files {
		pct := int(f.Percent + 0.5)
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		row.Files = append(row.Files, agentFileRow{
			Name:     f.Name,
			Size:     humanBytes(f.Size),
			Percent:  pct,
			Bucket:   ((pct + 5) / 10) * 10,
			Phase:    f.Phase,
			Speed:    f.Speed,
			UpSpeed:  f.UpSpeed,
			Peers:    f.Peers,
			Warnings: f.Warnings,
		})
	}
	return row
}

// adminAgentConcurrent updates one agent's dispatch cap (the one runtime
// setting the operator owns; everything else an agent reports for itself).
func (w *web) adminAgentConcurrent(c *gin.Context) {
	id, _ := strconv.ParseInt(strings.TrimSpace(c.PostForm("id")), 10, 64)
	n, _ := strconv.Atoi(strings.TrimSpace(c.PostForm("max")))
	if id > 0 && n > 0 {
		if err := w.data.SetAgentMaxConcurrent(c.Request.Context(), id, int64(n)); err != nil {
			w.log.Error("set agent concurrent", "agent", id, "err", err)
		}
	}
	c.Redirect(http.StatusSeeOther, "/admin/agents")
}

// adminAgentCreate provisions an agent by name (+ optional owner) and shows its
// freshly minted token once. This is the admin path to the same token the
// /register endpoint mints for a self-provisioning client.
func (w *web) adminAgentCreate(c *gin.Context) {
	name := strings.TrimSpace(c.PostForm("name"))
	owner := strings.TrimSpace(c.PostForm("owner"))
	if name == "" {
		c.Redirect(http.StatusSeeOther, "/admin/agents")
		return
	}
	ctx := c.Request.Context()
	a, tok, err := w.data.EnsureAgent(ctx, name, w.data.UserIDByUsername(ctx, owner))
	if err != nil {
		w.log.Error("create agent", "agent", name, "err", err)
		c.Redirect(http.StatusSeeOther, "/admin/agents")
		return
	}
	w.renderAgents(c, a.Name, tok)
}

// adminAgentToken rotates one agent's token, revoking the old one, and shows
// the new one once.
func (w *web) adminAgentToken(c *gin.Context) {
	id, _ := strconv.ParseInt(strings.TrimSpace(c.PostForm("id")), 10, 64)
	if id <= 0 {
		c.Redirect(http.StatusSeeOther, "/admin/agents")
		return
	}
	ctx := c.Request.Context()
	tok, err := w.data.RegenerateAgentToken(ctx, id)
	if err != nil {
		w.log.Error("rotate agent token", "agent", id, "err", err)
		c.Redirect(http.StatusSeeOther, "/admin/agents")
		return
	}
	name := ""
	if a, ok, _ := w.data.AgentByID(ctx, id); ok {
		name = a.Name
	}
	w.renderAgents(c, name, tok)
}

// adminAgentDelete removes an agent and its credential entirely.
func (w *web) adminAgentDelete(c *gin.Context) {
	id, _ := strconv.ParseInt(strings.TrimSpace(c.PostForm("id")), 10, 64)
	if id > 0 {
		if err := w.data.DeleteAgent(c.Request.Context(), id); err != nil {
			w.log.Error("delete agent", "agent", id, "err", err)
		}
	}
	c.Redirect(http.StatusSeeOther, "/admin/agents")
}

// tokenTail is the last 6 characters, enough to tell two tokens apart in the
// roster without printing the secret.
func tokenTail(tok string) string {
	if len(tok) <= 6 {
		return tok
	}
	return tok[len(tok)-6:]
}
