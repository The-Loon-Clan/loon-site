package storage

import (
	"context"
	"database/sql"
	"time"
)

// The fleet-agent runtime, host-side.
//
// An "agent" is a worker a member runs: it takes a torrent, downloads it, and
// re-uploads the content to Usenet, reporting its progress back here. In
// production that runtime is substantial (a poll queue, upload locks, lifetime
// accounting); this is the DEBUG surface of it -- one table an agent reports
// into, so the fleet UI can be built and driven WITHOUT a real agent, and the
// eventual real client meets the same endpoint. The agent PLUGIN
// (loon-plugins/agent) renders read-only surfaces over exactly these fields.
//
// One row per agent, latest report wins: an agent reports its whole current
// state each heartbeat (who it is, what it is doing, how far), so there is no
// separate task table to keep in step -- the row IS the agent's last-known
// state, and last_seen_at says how stale it is.

// Agent is one fleet worker's stored state.
type Agent struct {
	ID            int64         `db:"id"`
	Name          string        `db:"name"`
	UserID        sql.NullInt64 `db:"user_id"`
	MaxConcurrent int           `db:"max_concurrent"`
	LastSeenAt    sql.NullTime  `db:"last_seen_at"`
	CreatedAt     time.Time     `db:"created_at"`
	// Current task, all nullable: an idle agent has none.
	Phase       sql.NullString `db:"phase"`      // "downloading" | "uploading" | "assembling" | "idle" | ""
	RequestID   sql.NullInt64  `db:"request_id"` // the request it is fulfilling
	TaskTitle   sql.NullString `db:"task_title"` // what it is fetching, for a reader
	ProgressPct sql.NullInt64  `db:"progress_pct"`
	Detail      sql.NullString `db:"detail"`
	TaskAt      sql.NullTime   `db:"task_at"`
	// Lifetime counters, so the admin page has more than a spinner.
	Downloaded int64 `db:"downloaded"`
	Uploaded   int64 `db:"uploaded"`
}

// AgentReport is one heartbeat from an agent: its identity and its whole
// current state. Every field but the name is optional -- an idle agent reports
// a name and nothing else.
type AgentReport struct {
	Name        string
	Username    string // owner; resolved to user_id, ignored when unknown
	Phase       string
	RequestID   int64
	TaskTitle   string
	ProgressPct int
	Detail      string
	Downloaded  int64
	Uploaded    int64
}

// MigrateAgents creates the table. Idempotent.
func (st *Store) MigrateAgents() error {
	_, err := st.db.Exec(`
		CREATE TABLE IF NOT EXISTS agent (
			id             BIGSERIAL PRIMARY KEY,
			name           TEXT NOT NULL UNIQUE,
			user_id        BIGINT,
			max_concurrent INTEGER NOT NULL DEFAULT 2,
			last_seen_at   TIMESTAMPTZ,
			created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
			phase          TEXT,
			request_id     BIGINT,
			task_title     TEXT,
			progress_pct   INTEGER,
			detail         TEXT,
			task_at        TIMESTAMPTZ,
			downloaded     BIGINT NOT NULL DEFAULT 0,
			uploaded       BIGINT NOT NULL DEFAULT 0
		)`)
	return err
}

// UpsertAgentReport records one heartbeat, creating the agent on first sight.
//
// Keyed on the agent NAME: an agent is a long-lived named worker, and a
// restart that keeps the name keeps the row (and its lifetime counters). The
// owner is resolved by the caller to a user id, 0 when unknown, and a 0 does
// not clobber a previously-known owner.
func (st *Store) UpsertAgentReport(ctx context.Context, r AgentReport, userID int64) error {
	var owner sql.NullInt64
	if userID > 0 {
		owner = sql.NullInt64{Int64: userID, Valid: true}
	}
	// A blank phase clears the task columns -- an agent that went idle should
	// stop reading as if it were still mid-download.
	var (
		phase   = nullStr(r.Phase)
		reqID   = nullInt(r.RequestID)
		title   = nullStr(r.TaskTitle)
		prog    sql.NullInt64
		detail  = nullStr(r.Detail)
		taskNow sql.NullTime
	)
	if r.Phase != "" && r.Phase != "idle" {
		prog = sql.NullInt64{Int64: int64(clampPct(r.ProgressPct)), Valid: true}
		taskNow = sql.NullTime{Time: time.Now(), Valid: true}
	} else {
		phase, reqID, title, detail = sql.NullString{}, sql.NullInt64{}, sql.NullString{}, sql.NullString{}
	}
	_, err := st.db.ExecContext(ctx, `
		INSERT INTO agent (name, user_id, last_seen_at, phase, request_id, task_title,
		                   progress_pct, detail, task_at, downloaded, uploaded)
		VALUES ($1, $2, now(), $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (name) DO UPDATE SET
			-- a 0 owner does not overwrite a known one
			user_id      = COALESCE(EXCLUDED.user_id, agent.user_id),
			last_seen_at = now(),
			phase        = EXCLUDED.phase,
			request_id   = EXCLUDED.request_id,
			task_title   = EXCLUDED.task_title,
			progress_pct = EXCLUDED.progress_pct,
			detail       = EXCLUDED.detail,
			task_at      = EXCLUDED.task_at,
			-- counters only ever climb: a report carries the running total, and
			-- a restart that reports a lower total must not rewind the record.
			downloaded   = GREATEST(agent.downloaded, EXCLUDED.downloaded),
			uploaded     = GREATEST(agent.uploaded, EXCLUDED.uploaded)`,
		r.Name, owner, phase, reqID, title, prog, detail, taskNow, r.Downloaded, r.Uploaded)
	return err
}

// AgentsForUser lists one member's agents (fleet card).
func (st *Store) AgentsForUser(ctx context.Context, userID int64) ([]Agent, error) {
	var out []Agent
	err := st.db.SelectContext(ctx, &out, `
		SELECT * FROM agent WHERE user_id = $1 ORDER BY name`, userID)
	return out, err
}

// AllAgents lists every agent, for the admin page.
func (st *Store) AllAgents(ctx context.Context) ([]Agent, error) {
	var out []Agent
	err := st.db.SelectContext(ctx, &out, `SELECT * FROM agent ORDER BY name`)
	return out, err
}

// AgentByID is one agent's current state (the plugin's ActiveTask reads this).
func (st *Store) AgentByID(ctx context.Context, id int64) (Agent, bool, error) {
	var a Agent
	err := st.db.GetContext(ctx, &a, `SELECT * FROM agent WHERE id = $1`, id)
	if err == sql.ErrNoRows {
		return Agent{}, false, nil
	}
	return a, err == nil, err
}

// CountAgents reports how many agents reported in since onlineSince and how
// many exist at all (admin overview).
func (st *Store) CountAgents(ctx context.Context, onlineSince time.Time) (online, total int, err error) {
	err = st.db.GetContext(ctx, &total, `SELECT count(*) FROM agent`)
	if err != nil {
		return 0, 0, err
	}
	err = st.db.GetContext(ctx, &online,
		`SELECT count(*) FROM agent WHERE last_seen_at >= $1`, onlineSince)
	return online, total, err
}

// SetAgentMaxConcurrent updates one agent's dispatch cap (admin setting).
func (st *Store) SetAgentMaxConcurrent(ctx context.Context, id int64, n int) error {
	if n < 1 {
		n = 1
	}
	_, err := st.db.ExecContext(ctx,
		`UPDATE agent SET max_concurrent = $2 WHERE id = $1`, id, n)
	return err
}

// UserIDByUsername resolves an owner name to its id, 0 when unknown.
func (st *Store) UserIDByUsername(ctx context.Context, username string) int64 {
	if username == "" {
		return 0
	}
	var id int64
	if err := st.db.GetContext(ctx, &id,
		`SELECT id FROM users WHERE username = $1`, username); err != nil {
		return 0
	}
	return id
}

func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func nullInt(n int64) sql.NullInt64 {
	if n == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: n, Valid: true}
}

func clampPct(p int) int {
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

// UsernameByID resolves an owner id to a display name, "" when unknown -- the
// reverse of UserIDByUsername, for the admin roster.
func (st *Store) UsernameByID(ctx context.Context, id int64) string {
	if id <= 0 {
		return ""
	}
	var name string
	if err := st.db.GetContext(ctx, &name,
		`SELECT username FROM users WHERE id = $1`, id); err != nil {
		return ""
	}
	return name
}
