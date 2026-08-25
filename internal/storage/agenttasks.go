package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// The fleet's work queue: the missing middle between the two halves this host
// already had.
//
// The site side computes a complete grab every six-hourly schedule pass -- a
// TV gap, the best torrent for it, all the ids -- and handed it to a
// GrabDispatcher that was never registered, so it dispatched nothing
// (tvgrab_web.go). The fleet side speaks the whole v3 agent protocol but
// answered every /poll with 204, because there was nowhere for work to live.
// This table is that nowhere: Dispatch ENQUEUES, /poll LEASES, /complete
// CLOSES.
//
// DEDUP IS ON info_hash, AND SURVIVES COMPLETION. That is not an oversight:
// pluginapi's GrabDispatcher makes dedup the dispatcher's contract, and this
// demo never INGESTS what an agent produces (no ContentPipeline
// implementation), so the gap that caused a grab stays open forever. If a
// completed row stopped blocking, the six-hourly pass would re-queue the same
// episode for eternity. A row here therefore means "this info hash has been
// dealt with", whatever its state.
//
// LEASES EXPIRE rather than being reaped by a job. An agent that takes work
// and dies would otherwise hold it forever, and a queue whose recovery needs a
// separate scheduled task has two things to go wrong instead of one. The lease
// scan reclaims stale rows on the way past, so the queue heals itself at
// exactly the moment somebody asks it for work.

// Task states. A row is queued, leased to one agent, or finished either way.
const (
	TaskQueued    = "queued"
	TaskLeased    = "leased"
	TaskCompleted = "completed"
	TaskFailed    = "failed"
)

// taskLeaseTTL is how long a lease survives WITHOUT THE AGENT REPORTING. Prod
// agents post status every few seconds; a lease outliving several minutes of
// silence is an agent that crashed, and its work should go back in the queue.
//
// "Without reporting" is load-bearing and was once a lie: leased_at was only
// ever written when the lease was granted, so the reclaim measured how long a
// task had been HELD rather than how long its agent had been quiet. Real work
// here is a torrent fetch plus assembly plus a Usenet re-upload -- routinely
// hours -- so every long job was torn away from a healthy agent at fifteen
// minutes and handed to a second one, which downloaded and re-posted the same
// release while the first was still working it. RenewAgentLeases is the other
// half of this constant's meaning.
const taskLeaseTTL = 15 * time.Minute

// ErrNoTask is the ordinary "queue is empty, or you are at your cap" answer to
// a lease request. Not a failure -- an idle fleet is the normal state.
var ErrNoTask = errors.New("no task available")

// AgentTask is one torrent for the fleet to fetch and re-upload.
type AgentTask struct {
	ID       int64  `db:"id"`
	InfoHash string `db:"info_hash"`
	// Magnet is what an agent is served. DownloadURL is STORED but never sent:
	// a private tracker's link carries the member's passkey, and handing that
	// to any registered agent leaks it. Prod's answer is Private +
	// TorrentFileURL with the SITE fetching the file; until this demo does
	// that, the column is a record of where the copy came from, not a
	// credential to hand out. See agentapi_web.go's poll response.
	Magnet      sql.NullString `db:"magnet"`
	DownloadURL sql.NullString `db:"download_url"`
	TrackerSlug sql.NullString `db:"tracker_slug"`

	Title    string         `db:"title"`
	Category sql.NullString `db:"category"`
	Season   sql.NullInt64  `db:"season"`
	Episode  sql.NullInt64  `db:"episode"`
	ImdbID   sql.NullString `db:"imdb_id"`
	TvdbID   sql.NullString `db:"tvdb_id"`
	TmdbID   sql.NullString `db:"tmdb_id"`

	// RequestID links to an originating community request, 0 for an auto-grab
	// nobody asked for. NOT what is served as the wire's request_id -- see
	// WireRequestID.
	RequestID int64 `db:"request_id"`

	State         string         `db:"state"`
	LeasedAgentID sql.NullInt64  `db:"leased_agent_id"`
	LeasedAt      sql.NullTime   `db:"leased_at"`
	Progress      sql.NullString `db:"progress"`
	FailReason    sql.NullString `db:"fail_reason"`
	CreatedAt     time.Time      `db:"created_at"`
	CompletedAt   sql.NullTime   `db:"completed_at"`
}

// WireRequestID is the request_id this task is served as, and it is NEVER
// zero.
//
// The real agent parses a zero request_id as "empty response, no work"
// (loon-agent client.go: `if raw.RequestID == 0 { ... no request_id }`), so an
// auto-grab -- which by definition has no community request behind it -- would
// be silently discarded by every client if its own 0 were sent. The task id
// stands in: there is no request, and the task is the thing being worked on.
func (t AgentTask) WireRequestID() int64 {
	if t.RequestID > 0 {
		return t.RequestID
	}
	return t.ID
}

// MigrateAgentTasks creates the queue. Idempotent.
func (st *Store) MigrateAgentTasks() error {
	_, err := st.db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_task (
			id              BIGSERIAL PRIMARY KEY,
			info_hash       TEXT NOT NULL UNIQUE,
			magnet          TEXT,
			download_url    TEXT,
			tracker_slug    TEXT,
			title           TEXT NOT NULL,
			category        TEXT,
			season          INTEGER,
			episode         INTEGER,
			imdb_id         TEXT,
			tvdb_id         TEXT,
			tmdb_id         TEXT,
			request_id      BIGINT NOT NULL DEFAULT 0,
			state           TEXT NOT NULL DEFAULT 'queued',
			leased_agent_id BIGINT,
			leased_at       TIMESTAMPTZ,
			progress        TEXT,
			fail_reason     TEXT,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
			completed_at    TIMESTAMPTZ
		)`)
	return err
}

// EnqueueTask adds one grab to the queue, or reports that its info hash is
// already known. queued=false is the ordinary answer, not an error: the
// six-hourly pass re-offers everything it can still see.
func (st *Store) EnqueueTask(ctx context.Context, t AgentTask) (id int64, queued bool, err error) {
	err = st.db.GetContext(ctx, &id, `
		INSERT INTO agent_task (
			info_hash, magnet, download_url, tracker_slug,
			title, category, season, episode, imdb_id, tvdb_id, tmdb_id, request_id
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (info_hash) DO NOTHING
		RETURNING id`,
		t.InfoHash, t.Magnet, t.DownloadURL, t.TrackerSlug,
		t.Title, t.Category, t.Season, t.Episode, t.ImdbID, t.TvdbID, t.TmdbID, t.RequestID)
	if errors.Is(err, sql.ErrNoRows) {
		// The info hash is already in the table, in whatever state.
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// LeaseNextTask hands the oldest queued task to one agent, respecting that
// agent's concurrency cap, or returns ErrNoTask.
//
// Stale leases are reclaimed first, so a crashed agent's work returns to the
// queue without a separate reaper. SKIP LOCKED makes two agents polling at the
// same instant take two different rows instead of one blocking on the other.
func (st *Store) LeaseNextTask(ctx context.Context, agentID int64, maxConcurrent int) (AgentTask, error) {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	if _, err := st.db.ExecContext(ctx, `
		UPDATE agent_task
		SET state = 'queued', leased_agent_id = NULL, leased_at = NULL
		WHERE state = 'leased' AND leased_at < now() - $1::interval`,
		taskLeaseTTL.String()); err != nil {
		return AgentTask{}, err
	}
	var t AgentTask
	err := st.db.GetContext(ctx, &t, `
		UPDATE agent_task SET
			state           = 'leased',
			leased_agent_id = $1,
			leased_at       = now()
		WHERE id = (
			SELECT c.id FROM agent_task c
			WHERE c.state = 'queued'
			  AND (
				SELECT count(*) FROM agent_task held
				WHERE held.state = 'leased' AND held.leased_agent_id = $1
			  ) < $2
			ORDER BY c.created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING *`, agentID, maxConcurrent)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentTask{}, ErrNoTask
	}
	return t, err
}

// RenewAgentLeases pushes the expiry out on every task this agent holds,
// because the agent just spoke and is therefore not the crashed worker the
// reclaim exists to catch.
//
// Renewed per AGENT rather than per task, deliberately: an agent reports one
// live status for the whole worker, so its heartbeat is evidence for every
// lease it holds, and a per-task rule would expire the second of two
// concurrent jobs while the first kept the worker visibly alive.
//
// The blind spot, stated rather than papered over: an agent that keeps
// heartbeating while its transfer is wedged holds its lease indefinitely.
// Catching that needs progress to actually MOVE, which is a judgement this
// demo does not make; an operator has Retire on /admin/agents.
func (st *Store) RenewAgentLeases(ctx context.Context, agentID int64) error {
	_, err := st.db.ExecContext(ctx, `
		UPDATE agent_task SET leased_at = now()
		WHERE state = 'leased' AND leased_agent_id = $1`, agentID)
	return err
}

// RecordTaskProgress stores the agent's short progress line against its lease.
// Scoped to the leasing agent: a progress report about somebody else's task is
// not a thing an agent gets to file.
func (st *Store) RecordTaskProgress(ctx context.Context, agentID, taskID int64, progress string) error {
	_, err := st.db.ExecContext(ctx, `
		UPDATE agent_task SET progress = NULLIF($3, '')
		WHERE id = $1 AND leased_agent_id = $2 AND state = 'leased'`,
		taskID, agentID, progress)
	return err
}

// CloseTask finishes a leased task. ok is false when the task was not this
// agent's to close -- an unknown id, or somebody else's lease.
func (st *Store) CloseTask(ctx context.Context, agentID, taskID int64, completed bool, failReason string) (ok bool, err error) {
	state := TaskCompleted
	if !completed {
		state = TaskFailed
	}
	res, err := st.db.ExecContext(ctx, `
		UPDATE agent_task SET
			state        = $3,
			fail_reason  = NULLIF($4, ''),
			completed_at = now()
		WHERE id = $1 AND leased_agent_id = $2 AND state = 'leased'`,
		taskID, agentID, state, failReason)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// TaskCounts is the queue at a glance, for the operator roster.
type TaskCounts struct {
	Queued    int
	Leased    int
	Completed int
	Failed    int
}

// CountTasks totals the queue by state.
func (st *Store) CountTasks(ctx context.Context) (TaskCounts, error) {
	var c TaskCounts
	err := st.db.GetContext(ctx, &c, `
		SELECT
			count(*) FILTER (WHERE state = 'queued')    AS queued,
			count(*) FILTER (WHERE state = 'leased')    AS leased,
			count(*) FILTER (WHERE state = 'completed') AS completed,
			count(*) FILTER (WHERE state = 'failed')    AS failed
		FROM agent_task`)
	return c, err
}

// RecentTasks lists the queue newest-first for the admin page.
func (st *Store) RecentTasks(ctx context.Context, limit int) ([]AgentTask, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	var out []AgentTask
	err := st.db.SelectContext(ctx, &out, `
		SELECT * FROM agent_task ORDER BY created_at DESC LIMIT $1`, limit)
	return out, err
}

// DeleteTask removes one queue row, so an operator can retire a grab the demo
// will never finish (nothing here ingests a completed upload, so rows are
// permanent by design -- see the file comment).
func (st *Store) DeleteTask(ctx context.Context, id int64) error {
	_, err := st.db.ExecContext(ctx, `DELETE FROM agent_task WHERE id = $1`, id)
	return err
}
