package handlers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	agentplugin "github.com/the-loon-clan/loon-plugins/agent"

	"github.com/the-loon-clan/loon-site/internal/storage"
)

// Host side of the fleet-agent plugin.
//
// The agent PLUGIN (loon-plugins/agent) is surfaces-only: a profile fleet card
// and an admin dispatch panel, both read-only. Everything they show comes from
// the host through SetDeps -- the plugin owns no tables, deliberately, because
// the agent runtime (the report endpoint, the fleet rows) is the host's. So
// this file is the whole of the plugin's data supply: it adapts the agent
// store (internal/storage/agents.go) to the plugin's view types, and it must
// run BEFORE core.Boot because the plugin checks its Deps at Provision.

// agentOnlineWindow is how recently an agent must have reported to count as
// online. An agent heartbeats often; a few missed beats is a hiccup, a few
// minutes of silence is offline.
const agentOnlineWindow = 3 * time.Minute

// wireAgentPlugin injects the host's fleet data into the agent plugin.
func (w *web) wireAgentPlugin() {
	agentplugin.SetDeps(agentplugin.Deps{
		Viewer: func(c *gin.Context) (int, bool) {
			u, ok := w.currentUser(c)
			if !ok || u == nil {
				return 0, false
			}
			return int(u.ID), true
		},
		AgentsForUser: func(ctx context.Context, userID int) ([]agentplugin.Agent, error) {
			rows, err := w.data.AgentsForUser(ctx, int64(userID))
			if err != nil {
				return nil, err
			}
			out := make([]agentplugin.Agent, 0, len(rows))
			for _, a := range rows {
				out = append(out, toPluginAgent(a))
			}
			return out, nil
		},
		ActiveTask: func(ctx context.Context, agentID int) (*agentplugin.Task, error) {
			a, ok, err := w.data.AgentByID(ctx, int64(agentID))
			if err != nil || !ok || !agentBusy(a) {
				return nil, err // no task, or the agent is idle
			}
			return &agentplugin.Task{
				RequestID: a.Status().RequestID,
				Progress:  agentProgressLine(a),
			}, nil
		},
		CountAgents: func(ctx context.Context, onlineSince time.Time) (int, int, error) {
			return w.data.CountAgents(ctx, onlineSince)
		},
		MaxConcurrent: func(ctx context.Context) int {
			return hostMaxConcurrent()
		},

		// The /p/agents member page's optional seams — this host wires the full
		// set. Every verb is owner-scoped IN SQL (WHERE user_id = owner), so
		// ownership holds even if a caller above ever went wrong.
		AgentsDetail: func(ctx context.Context, ownerID int) ([]agentplugin.AgentDetail, error) {
			rows, err := w.data.AgentsForUser(ctx, int64(ownerID))
			if err != nil {
				return nil, err
			}
			out := make([]agentplugin.AgentDetail, 0, len(rows))
			for _, a := range rows {
				out = append(out, agentplugin.AgentDetail{
					Agent:  toPluginAgent(a),
					Status: toPluginStatus(a),
				})
			}
			return out, nil
		},
		CreateAgentFor: func(ctx context.Context, ownerID int, name string) (string, error) {
			_, tok, err := w.data.CreateAgentOwned(ctx, int64(ownerID), name)
			if errors.Is(err, storage.ErrAgentNameTaken) {
				// The plugin's sentinel, so its create action can say "that
				// name is taken" instead of the generic something-went-wrong.
				return "", fmt.Errorf("%w: %s", agentplugin.ErrNameTaken, name)
			}
			return tok, err
		},
		RotateTokenFor: func(ctx context.Context, ownerID, agentID int) (string, error) {
			return w.data.RotateAgentTokenOwned(ctx, int64(ownerID), int64(agentID))
		},
		DeleteAgentFor: func(ctx context.Context, ownerID, agentID int) error {
			return w.data.DeleteAgentOwned(ctx, int64(ownerID), int64(agentID))
		},
		ShowOnProfile: func(ctx context.Context, ownerID int) (bool, error) {
			return w.data.ShowAgentsOnProfile(ctx, int64(ownerID))
		},
		SetShowOnProfile: func(ctx context.Context, ownerID int, show bool) error {
			return w.data.SetShowAgentsOnProfile(ctx, int64(ownerID), show)
		},
		// Who may use the fleet at all. The plugin asks in five places the host
		// cannot reach individually — the page, its four actions, and the
		// profile card — which is the portable inner gate; canView here stays
		// the outer one and covers surfaces the plugin has no view of, like the
		// sitemap and the home widgets.
		//
		// An ERROR is a refusal, deliberately: this seam exists to answer the
		// question, and one that failed has not answered it. Returning
		// (true, err) is the shape a careless caller waves through, so the error
		// is returned alone.
		// TEMPORARY, for the side-by-side diff agreed with the plugin's owner:
		// wiring this mounts the plugin's /admin/p/agents beside this host's
		// /admin/agents so the two can be compared as rendered pages rather
		// than as my reading of two source files. One of them goes when we have
		// agreed which half each owns; nothing is deleted before then.
		AllAgents: func(ctx context.Context) ([]agentplugin.AdminAgent, error) {
			rows, err := w.data.AllAgents(ctx)
			if err != nil {
				return nil, err
			}
			out := make([]agentplugin.AdminAgent, 0, len(rows))
			for _, a := range rows {
				aa := agentplugin.AdminAgent{
					ID: int(a.ID), Name: a.Name, CreatedAt: a.CreatedAt,
					// The plugin documents this vocabulary as "active"/"revoked"
					// (its deps.go). This host has no revoked STATE: an agent row
					// exists or it is deleted, so the only honest word here is
					// "active" -- see the note sent to the plugin's owner about
					// whether the column earns its place on a host like this one.
					Status: "active",
				}
				if a.UserID.Valid {
					aa.Owner = w.data.UsernameByID(ctx, a.UserID.Int64)
				}
				if a.LastSeenAt.Valid {
					t := a.LastSeenAt.Time
					aa.LastSeen = &t
				}
				out = append(out, aa)
			}
			return out, nil
		},
		CanUseAgents: func(ctx context.Context, userID int) (bool, error) {
			if w.ents == nil {
				return false, errEntitlementsUnwired
			}
			return w.ents.Has(ctx, int64(userID), agentplugin.EntitlementKey), nil
		},
	})
}

// toPluginStatus narrows a stored live report to the member page's view type,
// or nil when there is none fresh enough: a status older than the online
// window is an agent that has STOPPED, and rendering its last words as live
// detail says "downloading" about a machine that is off.
func toPluginStatus(a storage.Agent) *agentplugin.AgentStatus {
	if !a.StatusAt.Valid || time.Since(a.StatusAt.Time) > agentOnlineWindow {
		return nil
	}
	s := a.Status()
	out := &agentplugin.AgentStatus{
		Phase:         s.Phase,
		VPNStatus:     s.VPNStatus,
		PublicIP:      s.PublicIP,
		DownloadSpeed: s.DownloadSpeed,
		// The plugin's view has one upload figure; the wire has three (direct,
		// nzb post, seeding). Show the one that is moving.
		UploadSpeed: firstNonEmpty(s.UploadSpeed, s.NzbUploadSpeed, s.SeedUploadSpeed),
		DiskFreeGB:  s.DiskFreeGB,
		TaskTitle:   s.TaskTitle,
		RequestID:   s.RequestID,
	}
	for _, f := range s.Files {
		out.Files = append(out.Files, agentplugin.FileDetail{
			Name:    f.Name,
			Percent: f.Percent,
			Phase:   f.Phase,
			Speed:   firstNonEmpty(f.Speed, f.UpSpeed),
		})
	}
	return out
}

// toPluginAgent narrows a stored agent to the plugin's public view type: name
// and last-seen only, never the owner id or the task detail a public-ish
// profile card should not carry.
func toPluginAgent(a storage.Agent) agentplugin.Agent {
	out := agentplugin.Agent{ID: int(a.ID), Name: a.Name}
	if a.LastSeenAt.Valid {
		t := a.LastSeenAt.Time
		out.LastSeen = &t
	}
	return out
}

// agentBusy reports whether an agent has an active, non-idle task. Prod's
// phase is "" or "idle" when a worker is between jobs.
func agentBusy(a storage.Agent) bool {
	if !a.Phase.Valid {
		return false
	}
	p := strings.ToLower(strings.TrimSpace(a.Phase.String))
	return p != "" && p != "idle"
}

// agentProgressLine renders one agent's current task as the short human line
// the fleet card shows: "downloading · 42% · 24.1 MB/s". The percent is the
// mean across the files in flight, which is what a member reads as "how far
// along is this job".
func agentProgressLine(a storage.Agent) string {
	s := a.Status()
	parts := []string{}
	if a.Phase.Valid && a.Phase.String != "" {
		parts = append(parts, a.Phase.String)
	}
	if pct, ok := agentOverallPercent(s); ok {
		parts = append(parts, fmt.Sprintf("%d%%", pct))
	}
	if speed := firstNonEmpty(s.DownloadSpeed, s.UploadSpeed, s.NzbUploadSpeed); speed != "" {
		parts = append(parts, speed)
	}
	return strings.Join(parts, " · ")
}

// agentOverallPercent is the mean file percent, rounded. ok is false when there
// are no files to average.
func agentOverallPercent(s storage.AgentLiveStatus) (int, bool) {
	if len(s.Files) == 0 {
		return 0, false
	}
	var sum float64
	for _, f := range s.Files {
		sum += f.Percent
	}
	return int(sum/float64(len(s.Files)) + 0.5), true
}

// firstNonEmpty returns the first non-empty string, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// hostMaxConcurrent is the host-wide per-agent dispatch cap the panel shows,
// read-only. AGENT_MAX_CONCURRENT overrides the default of 2.
func hostMaxConcurrent() int {
	if v := os.Getenv("AGENT_MAX_CONCURRENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 2
}
