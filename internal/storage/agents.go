package storage

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
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
//
// TokenHash is all the credential this row keeps: SHA-256 of the bearer token,
// hex. The plaintext exists exactly once, in the return value of the call that
// minted it — which is what the member page's token ceremony promises ("the
// site keeps only a hash"), and what makes a database read-out not a
// credential dump.
type Agent struct {
	ID            int64          `db:"id"`
	Name          string         `db:"name"`
	TokenHash     string         `db:"token_hash"`
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
// Two earlier shapes of `agent` have shipped: 4faa33a's (a shared token,
// single-task columns) and 2d379db's (per-agent tokens stored PLAINTEXT in a
// `token` column). This one keeps only token_hash. CREATE TABLE IF NOT EXISTS
// cannot reconcile either predecessor, so a table without the token_hash
// column -- which is both of them -- is dropped and rebuilt. Safe here and
// only here: the agent table holds nothing durable -- ephemeral fleet
// heartbeats and mock rows -- with no foreign keys pointing into it, so a
// worker simply re-registers. (A plaintext token could have been hashed in
// place instead of dropped; a debug rig's rows are not worth a migration path
// that then looks reusable for tables that ARE durable.)
func (st *Store) MigrateAgents() error {
	var hasTable, hasHash bool
	if err := st.db.Get(&hasTable, `SELECT to_regclass('public.agent') IS NOT NULL`); err != nil {
		return err
	}
	if hasTable {
		if err := st.db.Get(&hasHash, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'agent' AND column_name = 'token_hash'
			)`); err != nil {
			return err
		}
		if !hasHash {
			if _, err := st.db.Exec(`DROP TABLE agent`); err != nil {
				return err
			}
		}
	}
	if _, err := st.db.Exec(`
		CREATE TABLE IF NOT EXISTS agent (
			id             BIGSERIAL PRIMARY KEY,
			name           TEXT NOT NULL UNIQUE,
			token_hash     TEXT NOT NULL UNIQUE,
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
		)`); err != nil {
		return err
	}
	// The member's publishing choice for their fleet card: whether it renders
	// on /u/<name> for OTHER viewers. Its own table rather than a column on
	// agent, because it is a fact about the MEMBER (one row each), not about any
	// one worker — deleting your last agent must not delete your answer.
	//
	// show_on_profile defaults FALSE and an absent row reads as false: hidden is
	// the default the agent plugin documents (an agent roster names machines,
	// and nobody consented to that by installing an agent), so every path that
	// cannot find an answer must land on the same side.
	_, err := st.db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_prefs (
			user_id         BIGINT PRIMARY KEY,
			show_on_profile BOOLEAN NOT NULL DEFAULT FALSE,
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
		)`)
	return err
}

// EnsureAgent creates-or-refreshes an agent by name for the master-token
// provisioning path, returning the plaintext token — the ONE time it exists;
// only its hash is stored. A 0 owner does not clobber a known one.
//
// On a name it has seen, this RE-MINTS: the stored hash cannot be turned back
// into a token to hand out, so re-registering issues a fresh credential and
// the old one stops working. That is the right semantics for the path anyway
// — a worker that comes back to /register is a worker being re-provisioned —
// and it is why the mock client gets fresh tokens each run rather than
// accumulating live ones.
func (st *Store) EnsureAgent(ctx context.Context, name string, userID int64) (Agent, string, error) {
	var owner sql.NullInt64
	if userID > 0 {
		owner = sql.NullInt64{Int64: userID, Valid: true}
	}
	tok, err := newAgentToken()
	if err != nil {
		return Agent{}, "", err
	}
	var a Agent
	err = st.db.GetContext(ctx, &a, `
		INSERT INTO agent (name, token_hash, user_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE SET
			token_hash = EXCLUDED.token_hash,
			user_id    = COALESCE(EXCLUDED.user_id, agent.user_id)
		RETURNING *`, name, hashAgentToken(tok), owner)
	return a, tok, err
}

// The self-service refusals, as sentinels so the handler can tell "you cannot"
// from "the database broke" — the first is a message to the member, the second
// is a logged error.
var (
	// ErrAgentNameTaken: the name already belongs to an agent. Returned instead
	// of the existing row on purpose — see CreateAgentOwned.
	ErrAgentNameTaken = errors.New("agent name already taken")
	// ErrAgentNotOwned: no agent with that id belongs to that member. One
	// sentinel for missing and not-yours together, so the refusal does not
	// confirm which — an id probe learning "exists, someone else's" is exactly
	// the distinction not to hand out.
	ErrAgentNotOwned = errors.New("no such agent for this member")
)

// CreateAgentOwned is the SELF-SERVICE create: a member registering their own
// worker from /p/agents, returning the plaintext token shown once. Distinct
// from EnsureAgent, and the difference is the security line: EnsureAgent
// re-mints on a name it has seen, which behind the master token is
// re-provisioning — but in a member's hands "create seedbox-01" would
// REVOKE-AND-REPLACE the credential of whoever owns seedbox-01 already. So
// this inserts strictly: a taken name is ErrAgentNameTaken, never a write.
func (st *Store) CreateAgentOwned(ctx context.Context, ownerID int64, name string) (Agent, string, error) {
	if ownerID <= 0 {
		return Agent{}, "", ErrAgentNotOwned
	}
	tok, err := newAgentToken()
	if err != nil {
		return Agent{}, "", err
	}
	var a Agent
	err = st.db.GetContext(ctx, &a, `
		INSERT INTO agent (name, token_hash, user_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (name) DO NOTHING
		RETURNING *`, name, hashAgentToken(tok), sql.NullInt64{Int64: ownerID, Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, "", ErrAgentNameTaken
	}
	return a, tok, err
}

// RotateAgentTokenOwned mints a new token for the member's OWN agent,
// revoking the old one. Ownership is enforced in the WHERE clause, not by a
// prior read — two statements would leave a window, and a filter cannot.
func (st *Store) RotateAgentTokenOwned(ctx context.Context, ownerID, agentID int64) (string, error) {
	tok, err := newAgentToken()
	if err != nil {
		return "", err
	}
	res, err := st.db.ExecContext(ctx, `
		UPDATE agent SET token_hash = $3 WHERE id = $1 AND user_id = $2`, agentID, ownerID, hashAgentToken(tok))
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrAgentNotOwned
	}
	return tok, nil
}

// DeleteAgentOwned removes the member's OWN agent, same ownership rule.
func (st *Store) DeleteAgentOwned(ctx context.Context, ownerID, agentID int64) error {
	res, err := st.db.ExecContext(ctx, `
		DELETE FROM agent WHERE id = $1 AND user_id = $2`, agentID, ownerID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAgentNotOwned
	}
	return nil
}

// ShowAgentsOnProfile reads the member's publishing choice; an absent row is
// false — hidden is the default (see MigrateAgents).
func (st *Store) ShowAgentsOnProfile(ctx context.Context, userID int64) (bool, error) {
	var show bool
	err := st.db.GetContext(ctx, &show, `
		SELECT show_on_profile FROM agent_prefs WHERE user_id = $1`, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return show, err
}

// SetShowAgentsOnProfile records it, one row per member.
func (st *Store) SetShowAgentsOnProfile(ctx context.Context, userID int64, show bool) error {
	_, err := st.db.ExecContext(ctx, `
		INSERT INTO agent_prefs (user_id, show_on_profile, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (user_id) DO UPDATE SET
			show_on_profile = EXCLUDED.show_on_profile,
			updated_at      = now()`, userID, show)
	return err
}

// AgentByToken authenticates a report: the per-agent bearer token identifies
// exactly one agent, which is what a shared token could never do.
func (st *Store) AgentByToken(ctx context.Context, token string) (Agent, bool, error) {
	if token == "" {
		return Agent{}, false, nil
	}
	var a Agent
	err := st.db.GetContext(ctx, &a, `SELECT * FROM agent WHERE token_hash = $1`, hashAgentToken(token))
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
	_, err = st.db.ExecContext(ctx, `UPDATE agent SET token_hash = $2 WHERE id = $1`, id, hashAgentToken(tok))
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

// agentTokenSecret keys the at-rest hash when the operator provides one. Set
// once from the composition root (main), before any minting or lookup.
var agentTokenSecret []byte

// SetAgentTokenSecret installs the optional HMAC key (AGENT_TOKEN_SECRET).
// Changing it orphans existing rows — their stored hashes verify only under
// the key that made them — which the agent table can afford: workers
// re-register. A durable credential table could not adopt this scheme as-is.
func SetAgentTokenSecret(secret string) {
	if secret == "" {
		agentTokenSecret = nil
		return
	}
	agentTokenSecret = []byte(secret)
}

// hashAgentToken is the at-rest form of a bearer token, hex. No KDF in either
// branch, and correctly so: the input is 128 random bits, not a password, so
// brute force is the search for the token itself.
//
// With AGENT_TOKEN_SECRET set this is HMAC-SHA256 under that key — prod's
// scheme, where a database-only leak (dump, backup, an injected read) yields
// hashes that cannot even be VERIFIED against candidate tokens without the
// secret, which lives outside the database. Without it, plain SHA-256: weaker
// by exactly that one property, and the deliberate demo default because a
// required secret is a boot barrier and `git clone && docker compose up` must
// work. This is NOT parity with prod unless the secret is set.
func hashAgentToken(tok string) string {
	if len(agentTokenSecret) > 0 {
		mac := hmac.New(sha256.New, agentTokenSecret)
		mac.Write([]byte(tok))
		return hex.EncodeToString(mac.Sum(nil))
	}
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// newAgentToken mints a 32-character hex bearer token.
func newAgentToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
