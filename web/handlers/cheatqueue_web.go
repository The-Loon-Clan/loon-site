package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/the-loon-clan/loon-plugins/tracker"
	"github.com/the-loon-clan/loon-site/internal/config"
	"github.com/the-loon-clan/loon/core"
)

// The cheat-detection queue: what the sweep flagged, and clearing it.
//
// The detection has been running and recording since it was built. Nothing
// read it. tracker.OpenCheatFlags and ClearCheatFlag existed, were tested, and
// had no caller — so the site sampled announce counters, judged them, wrote a
// flag, and showed it to nobody. A detector whose output cannot be read is not
// a half-finished feature; it is a feature that does not exist, plus the cost
// of running it.
//
// Records only. Nothing here punishes anyone: the flag says "a human should
// look at this", and clearing it says a human did. The decision of what to DO
// about a member stays with the staff member, on the pages that already exist
// for it.
//
// Staff-gated at the route, alongside the avatar queue it sits next to — see
// admin_wiring.go. The two are the same job: a queue is only worked if it is
// somewhere a moderator already goes.

// cheatStore returns the tracker's store, or nil when the tracker is off.
//
// Built per request rather than held on the web struct, because the tracker is
// an optional plugin: a host that never enables it should not carry a handle to
// its schema, and one that enables it later should not need a restart for this
// page to work.
func (w *web) cheatStore() *tracker.PGStore {
	if !config.TrackerEnabled() || w.rt == nil || w.data == nil {
		return nil
	}
	return tracker.NewPGStore(core.NewStorage(w.data.DB().Raw()).SchemaDB("tracker"))
}

// cheatQueuePage serves GET /moderation/cheat.
func (w *web) cheatQueuePage(c *gin.Context) {
	data := map[string]any{"Title": "Cheat flags"}
	st := w.cheatStore()
	if st == nil {
		// The tracker is off. Say so rather than showing an empty queue: "no
		// flags" and "nothing is watching" look identical and mean opposite
		// things to somebody deciding whether to trust the page.
		data["TrackerOff"] = true
		w.render(c, "cheat_queue.html", data)
		return
	}
	flags, err := st.OpenCheatFlags(c.Request.Context(), cheatQueueLimit)
	if err != nil {
		w.log.Error("read cheat flags", "err", err)
		data["Err"] = "could not read the queue"
		w.render(c, "cheat_queue.html", data)
		return
	}
	data["Flags"] = flags
	data["Detection"] = config.CheatCheckEnabled()
	w.render(c, "cheat_queue.html", data)
}

// cheatQueueLimit caps one page of the queue.
//
// A queue longer than this is not a list to read, it is a signal that the
// policy is wrong — see the ratio allowance in the plugin's EvaluateCheat.
const cheatQueueLimit = 100

// cheatQueueClear serves POST /moderation/cheat/clear.
//
// Clearing records WHO looked, which is the point: an unattributed dismissal
// is indistinguishable from a flag nobody ever saw.
func (w *web) cheatQueueClear(c *gin.Context) {
	actor, ok := w.viewer(c)
	if !ok {
		return
	}
	st := w.cheatStore()
	if st == nil {
		c.Redirect(http.StatusSeeOther, "/moderation/cheat")
		return
	}
	id, err := strconv.ParseInt(c.PostForm(fieldID), 10, 64)
	if err != nil || id <= 0 {
		c.Redirect(http.StatusSeeOther, "/moderation/cheat")
		return
	}
	if err := st.ClearCheatFlag(c.Request.Context(), id, actor.ID); err != nil {
		w.log.Error("clear cheat flag", "flag", id, "actor", actor.ID, "err", err)
	}
	c.Redirect(http.StatusSeeOther, "/moderation/cheat")
}
