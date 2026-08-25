package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// The fleet-agent runtime, host-side, aligned to the production agent contract.
//
// An "agent" is a worker a member runs: it takes a torrent, downloads it, and
// re-uploads the content to Usenet. Production's agent protocol (loon-agent,
// X-Agent-Protocol v3) is a SPLIT of verbs -- poll for work, report progress,
// post a rich live status, complete an upload -- authenticated by a PER-AGENT
// bearer token so one agent can be attributed and revoked. This is the debug
// surface of exactly that contract: the demo drives the same endpoints and the
// same wire shapes, so a real agent binary pointed here would work and any gap
// is a gap in this, not in a throwaway seed.
//
// The rich state an agent posts to /status (AgentLiveStatus: phase, VPN,
// public IP, transfer speeds, per-FILE progress, disk) is stored verbatim as
// JSON -- prod's evolution rule is additive-with-omitempty, so a snapshot
// column grows with the contract without a migration. A few fields are lifted
// into columns for the roster's sort and filter.

// AgentLiveStatus is prod's /status payload, tag-for-tag (loon-agent
// client.AgentLiveStatus, X-Agent-Protocol v3). Stored as the snapshot the
// dashboard renders.
type AgentLiveStatus struct {
	Phase           string         `json:"phase"`
	VPNStatus       string         `json:"vpn_status"`
	PublicIP        string         `json:"public_ip"`
	DownloadSpeed   string         `json:"download_speed,omitempty"`
	UploadSpeed     string         `json:"upload_speed,omitempty"`
	NzbUploadSpeed  string         `json:"nzb_upload_speed,omitempty"`
	SeedUploadSpeed string         `json:"seed_upload_speed,omitempty"`
	Files           []FileProgress `json:"files,omitempty"`
	TaskTitle       string         `json:"task_title,omitempty"`
	RequestID       int64          `json:"request_id,omitempty"`
	DiskFreeGB      float64        `json:"disk_free_gb,omitempty"`
	DiskReservedGB  float64        `json:"disk_reserved_gb,omitempty"`
	SeedingCount    int            `json:"seeding_count,omitempty"`
}

// FileProgress is one file's progress within a status snapshot, prod's shape.
type FileProgress struct {
	Name        string   `json:"name"`
	Size        int64    `json:"size"`
	Transferred int64    `json:"transferred"`
	Percent     float64  `json:"percent"`
	Speed       string   `json:"speed,omitempty"`
	UpSpeed     string   `json:"up_speed,omitempty"`
	Phase       string   `json:"phase"`
	Peers       int      `json:"peers,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
}

// Agent is one fleet worker's stored identity, settings and last status.
type Agent struct {
	ID            int64          `db:"id"`
	Name          string         `db:"name"`
	Token         string         `db:"token"`
	UserID        sql.NullInt64  `db:"user_id"`
	MaxConcurrent int            `db:"max_concurrent"`
	Protocol      sql.NullInt64  `db:"protocol"`
	Version       sql.NullString `db:"version"`
	LastSeenAt    sql.NullTime   `db:"last_seen_at"`
	CreatedAt     time.Time      `db:"created_at"`
	Completed     int64          `db:"completed"`
	// Phase and TaskTitle are lifted from the last status for the roster; the
	// full snapshot lives in status_json.
	Phase      sql.NullString `db:"phase"`
	TaskTitle  sql.NullString `db:"task_title"`
	StatusJSON sql.NullString `db:"status_json"`
	StatusAt   sql.NullTime   `db:"status_at"`
}

// Status decodes the stored snapshot, or a zero value when there is none.
func (a Agent) Status() AgentLiveStatus {
	var s AgentLiveStatus
	if a.StatusJSON.Valid && a.StatusJSON.String != "" {
		_ = json.Unmarshal([]byte(a.StatusJSON.String), &s)
	}
	return s
}

// MigrateAgents creates the table. Idempotent.
//
// The 4faa33a runtime shipped an earlier, incompatible `agent` shape (a shared
// token, single-task columns, no per-agent token). Aligning to prod's contract
// changed the columns, and CREATE TABLE IF NOT EXISTS cannot reconcile an
// existing table -- so when the legacy shape is present (detected by the
// absence of the `token` column) it is dropped and rebuilt. This is safe here
// and only here: the agent table holds nothing durable -- ephemeral fleet
// heartbeats and mock rows -- with no foreign keys pointing into it, so a
// worker simply re-registers. A table that never held the old shape is
// untouched.
func (st *Store) MigrateAgents() error {
	var hasTable, hasToken bool
	if err := st.db.Get(&hasTable, `SELECT to_regclass('public.agent') IS NOT NULL`); err != nil {
		return err
	}
	if hasTable {
		if err := st.db.Get(&hasToken, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'agent' AND column_name = 'token'
			)`); err != nil {
			return err
		}
		if !hasToken {
			if _, err := st.db.Exec(`DROP TABLE agent`); err != nil {
				return err
			}
		}
	}
	_, err := st.db.Exec(`
		CREATE TABLE IF NOT EXISTS agent (
			id             BIGSERIAL PRIMARY KEY,
			name           TEXT NOT NULL UNIQUE,
			token          TEXT NOT NULL UNIQUE,
			user_id        BIGINT,
			max_concurrent INTEGER NOT NULL DEFAULT 2,
			protocol       INTEGER,
			version        TEXT,
			last_seen_at   TIMESTAMPTZ,
			created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
			completed      BIGINT NOT NULL DEFAULT 0,
			phase          TEXT,
			task_title     TEXT,
			status_json    TEXT,
			status_at      TIMESTAMPTZ
		)`)
	return err
}

// EnsureAgent creates-or-returns an agent by name, minting a per-agent token on
// first sight. Idempotent, so a provisioning caller (the demo register flow)
// gets a stable token to hand its worker. A 0 owner does not clobber a known
// one.
func (st *Store) EnsureAgent(ctx context.Context, name string, userID int64) (Agent, error) {
	var owner sql.NullInt64
	if userID > 0 {
		owner = sql.NullInt64{Int64: userID, Valid: true}
	}
	tok, err := newAgentToken()
	if err != nil {
		return Agent{}, err
	}
	var a Agent
	err = st.db.GetContext(ctx, &a, `
		INSERT INTO agent (name, token, user_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE SET
			user_id = COALESCE(EXCLUDED.user_id, agent.user_id)
		RETURNING *`, name, tok, owner)
	return a, err
}

// AgentByToken authenticates a report: the per-agent bearer token identifies
// exactly one agent, which is what a shared token could never do.
func (st *Store) AgentByToken(ctx context.Context, token string) (Agent, bool, error) {
	if token == "" {
		return Agent{}, false, nil
	}
	var a Agent
	err := st.db.GetContext(ctx, &a, `SELECT * FROM agent WHERE token = $1`, token)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, false, nil
	}
	return a, err == nil, err
}

// RecordStatus stores one /status snapshot: the verbatim JSON plus the columns
// the roster sorts on, and stamps last-seen and the reported protocol/version.
func (st *Store) RecordStatus(ctx context.Context, agentID int64, protocol int, version string, s AgentLiveStatus) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	var proto sql.NullInt64
	if protocol > 0 {
		proto = sql.NullInt64{Int64: int64(protocol), Valid: true}
	}
	_, err = st.db.ExecContext(ctx, `
		UPDATE agent SET
			last_seen_at = now(),
			protocol     = COALESCE($2, protocol),
			version      = COALESCE(NULLIF($3, ''), version),
			phase        = NULLIF($4, ''),
			task_title   = NULLIF($5, ''),
			status_json  = $6,
			status_at    = now()
		WHERE id = $1`,
		agentID, proto, version, s.Phase, s.TaskTitle, string(raw))
	return err
}

// TouchAgent stamps last-seen for a lightweight /progress ping between statuses.
func (st *Store) TouchAgent(ctx context.Context, agentID int64) error {
	_, err := st.db.ExecContext(ctx, `UPDATE agent SET last_seen_at = now() WHERE id = $1`, agentID)
	return err
}

// CompleteTask records one finished upload, bumping the lifetime counter.
func (st *Store) CompleteTask(ctx context.Context, agentID int64) error {
	_, err := st.db.ExecContext(ctx, `
		UPDATE agent SET completed = completed + 1, last_seen_at = now() WHERE id = $1`, agentID)
	return err
}

// AgentsForUser lists one member's agents (fleet card).
func (st *Store) AgentsForUser(ctx context.Context, userID int64) ([]Agent, error) {
	var out []Agent
	err := st.db.SelectContext(ctx, &out, `SELECT * FROM agent WHERE user_id = $1 ORDER BY name`, userID)
	return out, err
}

// AllAgents lists every agent, for the admin roster.
func (st *Store) AllAgents(ctx context.Context) ([]Agent, error) {
	var out []Agent
	err := st.db.SelectContext(ctx, &out, `SELECT * FROM agent ORDER BY name`)
	return out, err
}

// AgentByID is one agent's row (the plugin's ActiveTask reads this).
func (st *Store) AgentByID(ctx context.Context, id int64) (Agent, bool, error) {
	var a Agent
	err := st.db.GetContext(ctx, &a, `SELECT * FROM agent WHERE id = $1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, false, nil
	}
	return a, err == nil, err
}

// CountAgents reports how many agents reported in since onlineSince and how
// many exist at all (admin overview + the plugin dispatch panel).
func (st *Store) CountAgents(ctx context.Context, onlineSince time.Time) (online, total int, err error) {
	if err = st.db.GetContext(ctx, &total, `SELECT count(*) FROM agent`); err != nil {
		return 0, 0, err
	}
	err = st.db.GetContext(ctx, &online, `SELECT count(*) FROM agent WHERE last_seen_at >= $1`, onlineSince)
	return online, total, err
}

// SetAgentMaxConcurrent updates one agent's dispatch cap (admin setting).
func (st *Store) SetAgentMaxConcurrent(ctx context.Context, id, n int64) error {
	if n < 1 {
		n = 1
	}
	_, err := st.db.ExecContext(ctx, `UPDATE agent SET max_concurrent = $2 WHERE id = $1`, id, n)
	return err
}

// RegenerateAgentToken mints a new token, revoking the old one -- the point of
// per-agent tokens: one worker's credential can be rotated alone.
func (st *Store) RegenerateAgentToken(ctx context.Context, id int64) (string, error) {
	tok, err := newAgentToken()
	if err != nil {
		return "", err
	}
	_, err = st.db.ExecContext(ctx, `UPDATE agent SET token = $2 WHERE id = $1`, id, tok)
	return tok, err
}

// DeleteAgent removes an agent and its token entirely.
func (st *Store) DeleteAgent(ctx context.Context, id int64) error {
	_, err := st.db.ExecContext(ctx, `DELETE FROM agent WHERE id = $1`, id)
	return err
}

// UserIDByUsername resolves an owner name to its id, 0 when unknown.
func (st *Store) UserIDByUsername(ctx context.Context, username string) int64 {
	if username == "" {
		return 0
	}
	var id int64
	if err := st.db.GetContext(ctx, &id, `SELECT id FROM users WHERE username = $1`, username); err != nil {
		return 0
	}
	return id
}

// UsernameByID resolves an owner id to a display name, "" when unknown.
func (st *Store) UsernameByID(ctx context.Context, id int64) string {
	if id <= 0 {
		return ""
	}
	var name string
	if err := st.db.GetContext(ctx, &name, `SELECT username FROM users WHERE id = $1`, id); err != nil {
		return ""
	}
	return name
}

// newAgentToken mints a 32-character hex bearer token.
func newAgentToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
