package handlers

import (
	"compress/gzip"
	"errors"
	"io"
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
	//
	// The keys are the ones the client actually READS (loon-agent
	// parseUpgradeRequired: min_protocol, message, error). It used to send
	// "min", which unmarshalled into nothing — so an operator running an old
	// agent was told the site required "protocol v0", a version that has never
	// existed, instead of the real floor.
	if p, _ := strconv.Atoi(c.GetHeader("X-Agent-Protocol")); p != 0 && p < minAgentProtocol {
		c.JSON(http.StatusUpgradeRequired, gin.H{
			"error":        "agent protocol too old",
			"message":      "this site requires a newer agent",
			"min_protocol": minAgentProtocol,
		})
		return storage.Agent{}, false
	}
	return a, true
}

// agentTaskWire is the poll response, prod's AgentTask tag-for-tag (loon-agent
// client.go). Only the fields this host can honestly fill are sent; the rest
// are omitempty by prod's additive rule, so a real client sees a task it
// understands with the optional half absent.
//
// Season and Episodes are STRINGS here and ints in the store. That is prod's
// wire, not a slip: its episodes field carries ranges ("01-12"), which an int
// cannot hold.
type agentTaskWire struct {
	RequestID int64  `json:"request_id"`
	LockID    int64  `json:"lock_id"`
	Title     string `json:"title"`
	InfoHash  string `json:"info_hash,omitempty"`
	Category  string `json:"category,omitempty"`
	Season    string `json:"season,omitempty"`
	Episodes  string `json:"episodes,omitempty"`
	Magnet    string `json:"magnet,omitempty"`
}

// agentPoll answers "is there work for me": it leases the oldest queued task
// this agent has capacity for, or reports an empty queue.
//
// An empty queue is 204, which the real client explicitly handles as "no
// content" -- and is what every poll answered before there was a queue at all.
//
// The lease is what makes two agents polling at once take two different rows.
// A crashed agent's lease expires and its task returns to the queue; see
// LeaseNextTask.
func (w *web) agentPoll(c *gin.Context) {
	a, ok := w.authAgent(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	// Polling IS contact. Without this an agent that only ever polls -- which
	// is every idle agent -- showed "never seen" on the roster while visibly
	// talking to the site.
	if err := w.data.TouchAgent(ctx, a.ID); err != nil {
		w.log.Error("agent poll touch", "agent", a.ID, "err", err)
	}

	cap := a.MaxConcurrent
	if cap < 1 {
		cap = hostMaxConcurrent()
	}
	t, err := w.data.LeaseNextTask(ctx, a.ID, cap)
	if errors.Is(err, storage.ErrNoTask) {
		c.Status(http.StatusNoContent)
		return
	}
	if err != nil {
		w.log.Error("agent lease task", "agent", a.ID, "err", err)
		c.Status(http.StatusNoContent) // an idle answer beats an error the client must special-case
		return
	}

	out := agentTaskWire{
		// NEVER zero: the real client parses request_id 0 as "empty response,
		// no work" and would silently discard every auto-grab. See
		// AgentTask.WireRequestID.
		RequestID: t.WireRequestID(),
		LockID:    t.ID,
		Title:     t.Title,
		InfoHash:  t.InfoHash,
		Category:  t.Category.String,
		// DownloadURL is deliberately NOT sent. A private tracker's link
		// carries the member's passkey; prod's answer is Private +
		// TorrentFileURL with the site fetching the file, and until this host
		// does that, a magnet is the only artifact it can hand out safely.
		Magnet: t.Magnet.String,
	}
	if t.Season.Valid && t.Season.Int64 > 0 {
		out.Season = strconv.FormatInt(t.Season.Int64, 10)
	}
	if t.Episode.Valid && t.Episode.Int64 > 0 {
		out.Episodes = strconv.FormatInt(t.Episode.Int64, 10)
	}
	w.log.Info("agent task leased", "agent", a.ID, "task", t.ID, "title", t.Title)
	c.JSON(http.StatusOK, out)
}

// agentProgressBody is the lightweight per-lock ping between statuses.
type agentProgressBody struct {
	LockID   int    `json:"lock_id"`
	Progress string `json:"progress"`
	Speed    string `json:"speed"`
	Warnings string `json:"warnings"`
}

// agentProgress records a fast progress ping (keeps the agent marked online)
// and stores the line against the agent's own lease.
func (w *web) agentProgress(c *gin.Context) {
	a, ok := w.authAgent(c)
	if !ok {
		return
	}
	var body agentProgressBody
	_ = c.ShouldBindJSON(&body) // fields optional; the ping itself is the signal
	ctx := c.Request.Context()
	if err := w.data.TouchAgent(ctx, a.ID); err != nil {
		w.log.Error("agent progress", "agent", a.ID, "err", err)
	}
	// The agent just spoke, so none of its leases belong to a crashed worker.
	if err := w.data.RenewAgentLeases(ctx, a.ID); err != nil {
		w.log.Error("agent renew leases", "agent", a.ID, "err", err)
	}
	if body.LockID > 0 {
		line := strings.TrimSpace(body.Progress)
		if s := strings.TrimSpace(body.Speed); s != "" {
			line = strings.TrimSpace(line + " · " + s)
		}
		// Scoped to this agent's lease inside the query, so a report about
		// somebody else's task updates nothing.
		if err := w.data.RecordTaskProgress(ctx, a.ID, int64(body.LockID), line); err != nil {
			w.log.Error("agent task progress", "agent", a.ID, "task", body.LockID, "err", err)
		}
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
	// THE heartbeat the lease TTL is written against: prod agents post this
	// every few seconds, so it is what keeps a long job from being reclaimed
	// out from under a healthy worker.
	if err := w.data.RenewAgentLeases(c.Request.Context(), a.ID); err != nil {
		w.log.Error("agent renew leases", "agent", a.ID, "err", err)
	}
	if err := w.data.RecordStatus(c.Request.Context(), a.ID, protocol, version, s); err != nil {
		w.log.Error("agent status", "agent", a.ID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not record status"})
		return
	}
	// The status response can carry a cancel command; the demo never cancels.
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// agentComplete records a finished upload and closes the task's lease.
//
// DUAL-DIALECT, because the two clients that reach it speak differently and
// both are legitimate: the real agent posts a MULTIPART bundle (the NZB with
// its screenshots, metadata and subtitles), while the mock posts JSON. Reading
// only one dialect would either break the rig or fail the real-client test
// this whole runtime exists to pass. gin's PostForm already covers multipart
// and urlencoded, so JSON is the only extra case.
//
// The bundle itself is ACCEPTED AND DROPPED: this demo has no ContentPipeline
// implementation, so there is nothing to ingest an NZB into. That is the
// honest gap, and it is why a completed row keeps blocking its info hash --
// the gap that caused the grab is still open. See agenttasks.go.
func (w *web) agentComplete(c *gin.Context) {
	a, ok := w.authAgent(c)
	if !ok {
		return
	}
	lockID, status, failReason := agentCompleteFields(c)
	ctx := c.Request.Context()

	// "completed" is the success verdict; prod also sends failure statuses,
	// and a failed grab must not be counted as an upload.
	succeeded := status == "" || strings.EqualFold(status, "completed") || strings.EqualFold(status, "success")

	if lockID > 0 {
		closed, err := w.data.CloseTask(ctx, a.ID, lockID, succeeded, failReason)
		if err != nil {
			w.log.Error("agent close task", "agent", a.ID, "task", lockID, "err", err)
		} else if !closed {
			// Not this agent's lease, or already closed. Logged rather than
			// refused: the upload really did happen, and an agent that retries
			// a completion must not be told its work failed.
			w.log.Warn("agent completed an unheld task", "agent", a.ID, "task", lockID)
		}
	}
	if succeeded {
		if err := w.data.CompleteTask(ctx, a.ID); err != nil {
			w.log.Error("agent complete", "agent", a.ID, "err", err)
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "completed"})
}

// agentCompleteFields reads the completion in whichever dialect arrived.
func agentCompleteFields(c *gin.Context) (lockID int64, status, failReason string) {
	if strings.Contains(c.ContentType(), "application/json") {
		var body struct {
			LockID     int64  `json:"lock_id"`
			RequestID  int64  `json:"request_id"`
			Status     string `json:"status"`
			FailReason string `json:"fail_reason"`
		}
		if err := c.ShouldBindJSON(&body); err == nil {
			return body.LockID, body.Status, body.FailReason
		}
		return 0, "", ""
	}
	lockID, _ = strconv.ParseInt(strings.TrimSpace(c.PostForm("lock_id")), 10, 64)
	return lockID, strings.TrimSpace(c.PostForm("status")), strings.TrimSpace(c.PostForm("fail_reason"))
}

// gunzipAgentBody transparently decompresses a gzipped request body.
//
// The real agent GZIPS its heavy posts -- /complete, /backfill, /screenshot
// all go through postGzippedWith, which compresses the body and sets
// Content-Encoding: gzip while keeping the multipart Content-Type. Go's
// net/http never transparently decodes a REQUEST body (only responses), and
// nothing else in this stack does either, so without this the multipart parser
// reads gzip bytes, finds no fields, and every completion arrives with
// lock_id 0: the task is never closed, its lease expires, and the same release
// is downloaded and re-posted to Usenet on a loop.
//
// The mock could never have caught it -- it posts JSON, the one dialect that
// worked -- which is exactly the kind of gap pointing a real client at this
// rig is supposed to find, found by reading the client instead.
func gunzipAgentBody() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !strings.EqualFold(strings.TrimSpace(c.GetHeader("Content-Encoding")), "gzip") {
			c.Next()
			return
		}
		zr, err := gzip.NewReader(c.Request.Body)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "malformed gzip body"})
			return
		}
		//nolint:errcheck // a decompressor's Close only reports trailing-CRC
		// trouble, and by then the handler has already read and acted on the body
		defer zr.Close()
		c.Request.Body = io.NopCloser(zr)
		// Both matter. The header must go or a later reader would try to
		// decompress the already-decompressed stream; ContentLength describes
		// the COMPRESSED body, and leaving it would truncate the multipart
		// parse at that many bytes -- a subtler version of the same bug.
		c.Request.Header.Del("Content-Encoding")
		c.Request.ContentLength = -1
		c.Next()
	}
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
