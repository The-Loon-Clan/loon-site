package site

import (
	"encoding/json"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

// A DEV-ONLY side-by-side inspector: the reference design on the left, the
// real site live on the right, and a picker that names whatever is clicked.
//
// It exists because describing a fault in prose has repeatedly sent me at the
// wrong element — "the bit under the heading", "the top still has a
// background" — and each round cost a build, a deploy and a screenshot to
// disprove. Clicking the thing and reading back its selector, its classes and
// the computed values that actually paint it removes the translation step
// entirely.
//
// OFF unless LOON_DEMO_UI_INSPECT is set, and that is not caution for its own
// sake. It serves files from a directory on disk and injects script into a
// frame of the site; neither belongs on anything reachable from outside a
// developer's machine.
//
// This site otherwise ships NO JavaScript for its chrome, deliberately. The
// script here is not chrome — it is a tool that renders nothing on the site
// itself and is absent from every build that does not ask for it.

// uiInspectEnabled reports whether the operator asked for the inspector.
func uiInspectEnabled() bool {
	v := os.Getenv("LOON_DEMO_UI_INSPECT")
	return v == "1" || v == "true" || v == "yes"
}

// refsDir is where reference screenshots live. Outside the embedded FS on
// purpose: they are pasted in during a session, not shipped with the binary.
const refsDir = "refs"

// mountUIInspect wires the tool when it is switched on.
func (w *web) mountUIInspect(e *gin.Engine) {
	if !uiInspectEnabled() {
		return
	}
	e.GET("/dev/compare", w.uiCompare)
	e.GET("/dev/refs/:name", w.uiRefImage)
	e.POST("/dev/focus", w.uiSaveFocus)
}

// uiRefImage serves one reference image off disk.
func (w *web) uiRefImage(c *gin.Context) {
	name := filepath.Base(c.Param("name"))
	// Base() alone is the guard: a traversal attempt reduces to its last
	// segment, so "../../etc/passwd" asks for "passwd" inside refs/ and simply
	// is not there. The extension check keeps this to images rather than
	// anything that happens to be in the folder.
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".webp":
	default:
		c.Status(http.StatusNotFound)
		return
	}
	c.File(filepath.Join(refsDir, name))
}

// uiCompare renders the split view.
func (w *web) uiCompare(c *gin.Context) {
	ref := filepath.Base(c.Query("ref"))
	if ref == "" || ref == "." {
		ref = "target_home.png"
	}
	page := c.Query("page")
	if page == "" || !strings.HasPrefix(page, "/") {
		page = "/"
	}
	// A list of what is actually in refs/, so the picker offers real files
	// rather than asking somebody to remember a filename.
	var available []string
	if entries, err := os.ReadDir(refsDir); err == nil {
		for _, e := range entries {
			n := e.Name()
			if strings.HasPrefix(n, "_match_") {
				continue // generated comparison sheets, not references
			}
			switch strings.ToLower(filepath.Ext(n)) {
			case ".png", ".jpg", ".jpeg", ".webp":
				available = append(available, n)
			}
		}
	}
	// Parsed and executed here rather than through the host's render(): this
	// page is standalone by design and must NOT inherit base.html, the site
	// chrome or the site's stylesheets. Anything inherited would be a thing
	// that could differ between the tool and the page it is inspecting.
	//
	// Parsed per request, which is right for a dev tool: editing the template
	// takes effect on reload rather than on a rebuild.
	t, err := template.ParseFS(siteFS, "web/templates/dev_compare.html")
	if err != nil {
		c.String(http.StatusInternalServerError, "dev_compare: %v", err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusOK)
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(c.Writer, map[string]any{
		"Ref":       ref,
		"Page":      page,
		"Available": available,
	}); err != nil {
		// Streaming has already begun, so this can only be logged.
		w.log.Error("dev_compare render", "err", err)
	}
}

// uiFocus is a saved region of interest: the same area on the reference and on
// the live page, so a comparison can be run against one part rather than a
// whole screen.
//
// Rectangles are stored in each SOURCE's own pixels — the reference image's
// natural size, and the live page's CSS pixels — never in screen pixels. A rect
// recorded at whatever zoom or window size happened to be set would be
// meaningless the moment either changed, and this file's whole purpose is to
// outlive the session that made it.
type uiFocus struct {
	Ref  string `json:"ref"`
	Page string `json:"page"`
	// LiveWidth is the iframe width the live rect was measured against, so a
	// screenshot taken at a different width can still be cropped to the same
	// region.
	LiveWidth int    `json:"liveWidth"`
	RefRect   uiRect `json:"refRect"`
	LiveRect  uiRect `json:"liveRect"`
	Note      string `json:"note"`
}

type uiRect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// uiSaveFocus writes refs/_focus.json — the handoff between "I can see what is
// wrong" and "the tooling can measure it".
func (w *web) uiSaveFocus(c *gin.Context) {
	var f uiFocus
	if err := c.ShouldBindJSON(&f); err != nil {
		c.String(http.StatusBadRequest, "bad focus: %v", err)
		return
	}
	f.Ref = filepath.Base(f.Ref)
	if !strings.HasPrefix(f.Page, "/") {
		f.Page = "/" + f.Page
	}
	if f.LiveWidth <= 0 {
		f.LiveWidth = 1400
	}
	if f.RefRect.W <= 0 || f.LiveRect.W <= 0 {
		c.String(http.StatusBadRequest, "both rectangles are required")
		return
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		c.String(http.StatusInternalServerError, "encode: %v", err)
		return
	}
	path := filepath.Join(refsDir, "_focus.json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		c.String(http.StatusInternalServerError, "write: %v", err)
		return
	}
	c.String(http.StatusOK, "%s\n%s", path, string(b))
}
