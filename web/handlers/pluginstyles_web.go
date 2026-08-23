package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// Plugin stylesheets, served from a URL instead of shipped inside every page.
//
// A plugin renders a FRAGMENT, so its CSS has always travelled inside that
// fragment in a <style> block. renderStatus hoists those into the head, which
// makes the markup valid and leaves the two costs that matter: the bytes are in
// the DOCUMENT, so they are re-sent on every view, and they are INLINE, so this
// host cannot drop style-src 'unsafe-inline' from its CSP.
//
// A plugin now hands over its sheet at Provision (pluginapi.StylesheetRegistrar)
// and the host owns the URL, the hash and the caching. See docs/BACKLOG.md #13.
//
// WHY EVERY PAGE LINKS EVERY SHEET. The host does not know which plugin drew
// which part of a page -- a fragment is opaque HTML by the time it arrives. One
// <link> per plugin, fetched once and then immutable, is cheaper than the same
// bytes re-sent in every document, which is what happens today. It is not free,
// and the honest next step if it ever matters is for a plugin to declare which
// of its views need which sheet.
type pluginStyles struct {
	mu     sync.RWMutex
	sheets map[string]pluginSheet
}

type pluginSheet struct {
	css  string
	hash string
}

// A plugin name has to be safe in a URL path segment and is not user input --
// it is a compile-time constant in the plugin. Checked anyway: this ends up in
// a route.
var pluginNameOK = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

func newPluginStyles() *pluginStyles {
	return &pluginStyles{sheets: map[string]pluginSheet{}}
}

// RegisterStylesheet implements pluginapi.StylesheetRegistrar.
func (p *pluginStyles) RegisterStylesheet(plugin, css string) error {
	if !pluginNameOK.MatchString(plugin) {
		return fmt.Errorf("plugin stylesheet: %q is not a usable name", plugin)
	}
	if strings.TrimSpace(css) == "" {
		return fmt.Errorf("plugin stylesheet: %s registered an empty sheet", plugin)
	}
	sum := sha256.Sum256([]byte(css))
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sheets[plugin] = pluginSheet{css: css, hash: hex.EncodeToString(sum[:])[:10]}
	return nil
}

// links returns the <link> hrefs, in a stable order.
//
// Sorted by name, not by registration order: plugin Provision order is a
// function of the import list and a stylesheet that moves in the cascade
// because somebody reordered an import is a bug nobody would find.
func (p *pluginStyles) links() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	names := make([]string, 0, len(p.sheets))
	for n := range p.sheets {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, fmt.Sprintf("%s%s.css?v=%s", pluginCSSPrefix, n, p.sheets[n].hash))
	}
	return out
}

// pluginCSSPrefix is its own path, NOT under /static/.
//
// e.StaticFS("/static", ...) claims /static/*filepath, and gin refuses a
// sibling route under a catch-all -- the conflict is a panic at wiring time,
// not a 404 later. staticCacheHeaders was widened to cover this prefix on the
// same terms instead: a year, immutable, only when the URL carries ?v=, which
// links() always does.
const pluginCSSPrefix = "/pluginstyle/"

// serve answers /pluginstyle/<name>.css.
func (p *pluginStyles) serve(c *gin.Context) {
	name := strings.TrimSuffix(c.Param("name"), ".css")
	p.mu.RLock()
	sheet, ok := p.sheets[name]
	p.mu.RUnlock()
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Content-Type", "text/css; charset=utf-8")
	c.String(http.StatusOK, sheet.css)
}
