package handlers

import (
	"context"
	"database/sql"
	"os"
	"strings"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"

	"github.com/the-loon-clan/loon-site/internal/storage"
)

// The host's agent.dispatch implementation: the outbound half of the content
// loop, and the thing that finally makes the auto-grab's work leave the page.
//
// pluginapi's grabdispatch.go says a GrabDispatcher is implemented by whatever
// owns the agent queue, and on this stack that is the host. The schedule finds
// an aired episode, the gap join finds the index has nothing, the tracker
// search picks the best copy -- and this enqueues that copy for an agent to
// lease from /api/agent/poll.
//
// WHY IT IS OFF BY DEFAULT, and why the flag is not AGENT_TOKEN. A queued row
// carries a LIVE magnet from a real public tracker. Nothing here simulates
// that: a genuine loon-agent pointed at this host would lease the task and
// actually download the torrent and actually try to post it to Usenet, under
// whoever's Usenet credentials that agent holds. The mock client is harmless
// because it invents its own transfers, but the stated plan for this rig is to
// point a REAL client at it and look for contract gaps -- which is exactly the
// moment an ungated queue stops being a debug surface and starts being a
// download-and-upload robot.
//
// AGENT_TOKEN cannot be that gate: docker-compose gives it a default value, so
// gating on it would mean "always on" in the very deployment that most needs
// the brake. AGENT_DISPATCH is separate, unset by default, and its absence
// leaves the demo exactly as it has always behaved -- the auto-grab computes
// the grab and dispatches nothing.
const agentDispatchEnv = "AGENT_DISPATCH"

// agentDispatchEnabled reports whether real grabs may enter the queue.
func agentDispatchEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(agentDispatchEnv)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// agentDispatcher enqueues grabs for the fleet.
type agentDispatcher struct{ w *web }

// Dispatch enqueues one chosen torrent, deduped on info hash.
//
// Dedup is this dispatcher's contract (grabdispatch.go), and here it reaches
// across every state including completed -- because this demo never ingests
// what an agent produces, so the gap that caused the grab never closes and a
// finished row is the only thing standing between the six-hourly pass and an
// eternal re-queue. See internal/storage/agenttasks.go.
func (d agentDispatcher) Dispatch(ctx context.Context, req pluginapi.GrabRequest) (pluginapi.GrabResult, error) {
	// An info hash is the dedup key, so a grab without one cannot be queued
	// safely: every pass would add another row for the same episode.
	if strings.TrimSpace(req.InfoHash) == "" {
		return pluginapi.GrabResult{}, nil
	}
	t := storage.AgentTask{
		InfoHash:    strings.ToLower(strings.TrimSpace(req.InfoHash)),
		Magnet:      nullString(req.Magnet),
		DownloadURL: nullString(req.DownloadURL),
		TrackerSlug: nullString(req.TrackerSlug),
		Title:       req.Title,
		Category:    nullString(req.Category),
		Season:      nullInt(req.Season),
		Episode:     nullInt(req.Episode),
		ImdbID:      nullString(req.ImdbID),
		TvdbID:      nullString(req.TvdbID),
		TmdbID:      nullString(req.TmdbID),
		RequestID:   req.RequestID,
	}
	id, queued, err := d.w.data.EnqueueTask(ctx, t)
	if err != nil {
		return pluginapi.GrabResult{}, err
	}
	if queued {
		d.w.log.Info("agent task queued", "task", id, "title", req.Title)
	}
	return pluginapi.GrabResult{Queued: queued, TaskID: id}, nil
}

// wireAgentDispatch publishes agent.dispatch when the operator opted in. MUST
// run before wireTVSchedule, which resolves the dispatcher once at boot.
func (w *web) wireAgentDispatch(c *core.Core) {
	if !agentDispatchEnabled() {
		// Silent by design: this is the default state, and a warning on every
		// boot for a feature nobody asked for trains operators to ignore logs.
		return
	}
	if err := c.Register(pluginapi.GrabDispatchName, pluginapi.GrabDispatcher(agentDispatcher{w: w})); err != nil {
		w.log.Error("register agent dispatch", "err", err)
		return
	}
	w.log.Info("agent dispatch enabled — auto-grabs will queue REAL torrents for the fleet")
}

func nullString(s string) sql.NullString {
	s = strings.TrimSpace(s)
	return sql.NullString{String: s, Valid: s != ""}
}

func nullInt(n int) sql.NullInt64 {
	return sql.NullInt64{Int64: int64(n), Valid: n > 0}
}
