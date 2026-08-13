package site

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Admin → Cover art. The operator half of covermode_web.go.
//
// A setting only an environment variable can reach is not really a setting:
// changing it means editing a compose file and restarting, which is a deploy,
// not a decision. This page makes the four modes a choice an admin can make and
// reverse, and it takes effect on the next scraped cover with no restart —
// the mode is read from an atomic on every one.

// adminCovers serves GET /admin/covers.
func (w *web) adminCovers(c *gin.Context) {
	type modeVM struct {
		Value, Label string
		Current      bool
	}
	cur := coverMode()
	modes := make([]modeVM, 0, 4)
	for _, m := range coverModes() {
		modes = append(modes, modeVM{Value: m, Label: coverModeLabel(m), Current: m == cur})
	}
	w.render(c, "admin_covers.html", map[string]any{
		"Title":   "Cover art",
		"Modes":   modes,
		"Current": cur,
		"Saved":   c.Query("saved") == "1",
		"Err":     c.Query("err"),
	})
}

// adminCoversSave serves POST /admin/covers.
func (w *web) adminCoversSave(c *gin.Context) {
	mode := c.PostForm("mode")
	if err := saveCoverMode(c.Request.Context(), mode); err != nil {
		// An unknown mode is a bug in the form, not a state to adopt: storing
		// it would leave covers behaving in a way nothing here describes.
		w.log.Error("save cover mode", "mode", mode, "err", err)
		c.Redirect(http.StatusFound, "/admin/covers?err=could+not+save")
		return
	}
	w.log.Info("cover mode changed", "mode", mode, "meaning", coverModeLabel(mode))
	c.Redirect(http.StatusFound, "/admin/covers?saved=1")
}
