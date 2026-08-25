package handlers

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// The fleet operator's view: every agent, its settings, and what it is doing
// right now.
//
// This is the "agent page" prod has, rebuilt over the demo's report runtime so
// the UI can be driven and debugged without a real agent (a mock client
// POSTs). It reads the same rows the plugin's fleet card reads, but WHOLE: the
// card is one member's public-ish glance, this is the operator's roster with
// the owner, the lifetime counts, and the concurrency setting they can change.

// agentRow is one agent as the admin table shows it.
type agentRow struct {
	ID         int64
	Name       string
	Owner      string
	Online     bool
	LastSeen   string
	Max        int
	Phase      string
	Title      string
	Progress   int
	Bucket     int // Progress rounded to the nearest 10, for a width class
	Detail     string
	HasTask    bool
	Downloaded int64
	Uploaded   int64
}

type agentsVM struct {
	Rows       []agentRow
	Online     int
	Total      int
	MaxDefault int
	TokenSet   bool
	ReportURL  string
}

func (w *web) adminAgents(c *gin.Context) {
	vm := w.agentsVM(c.Request.Context())
	vm.TokenSet = w.agentToken != ""
	vm.ReportURL = requestBaseURL(c) + "/api/agent/report"
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
	// Owners resolved in one place; the store keeps only the id.
	now := time.Now()
	online := 0
	for _, a := range agents {
		row := agentRow{
			ID: a.ID, Name: a.Name, Max: a.MaxConcurrent,
			Downloaded: a.Downloaded, Uploaded: a.Uploaded,
		}
		if a.LastSeenAt.Valid {
			row.LastSeen = humanAge(now.Sub(a.LastSeenAt.Time))
			row.Online = now.Sub(a.LastSeenAt.Time) <= agentOnlineWindow
			if row.Online {
				online++
			}
		} else {
			row.LastSeen = "never"
		}
		if a.UserID.Valid {
			row.Owner = w.data.UsernameByID(ctx, a.UserID.Int64)
		}
		if a.Phase.Valid && a.Phase.String != "" {
			row.HasTask = true
			row.Phase = a.Phase.String
			row.Title = strOrEmpty(a.TaskTitle.String, a.TaskTitle.Valid)
			row.Detail = strOrEmpty(a.Detail.String, a.Detail.Valid)
			if a.ProgressPct.Valid {
				row.Progress = int(a.ProgressPct.Int64)
				row.Bucket = ((row.Progress + 5) / 10) * 10
				if row.Bucket > 100 {
					row.Bucket = 100
				}
			}
		}
		vm.Rows = append(vm.Rows, row)
	}
	vm.Online, vm.Total = online, len(agents)
	return vm
}

// adminAgentConcurrent updates one agent's dispatch cap (the one setting the
// operator owns; everything else an agent reports for itself).
func (w *web) adminAgentConcurrent(c *gin.Context) {
	id, _ := strconv.ParseInt(strings.TrimSpace(c.PostForm("id")), 10, 64)
	n, _ := strconv.Atoi(strings.TrimSpace(c.PostForm("max")))
	if id > 0 && n > 0 {
		if err := w.data.SetAgentMaxConcurrent(c.Request.Context(), id, n); err != nil {
			w.log.Error("set agent concurrent", "agent", id, "err", err)
		}
	}
	c.Redirect(http.StatusSeeOther, "/admin/agents")
}

func strOrEmpty(s string, valid bool) string {
	if !valid {
		return ""
	}
	return s
}
