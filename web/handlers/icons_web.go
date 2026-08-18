package handlers

import (
	"io/fs"
	"sort"
	"strings"
	"sync"

	site "github.com/the-loon-clan/loon-site"
)

// The site's icon set, as a list a plugin can offer in a dropdown.
//
// A plugin that draws an icon already couples itself to these ids — the store's
// cards, the ranks groups widget and the medals cabinet all emit
// <use href="#name"> and get an empty space if this host has no such symbol.
// What they could not do until now is ASK what exists, so an operator picking
// an icon had a free-text box and a guess, which is how a medal ended up
// holding a Windows path and drawing the broken-image glyph.
//
// Read from the sprite sheet rather than hand-listed. The alternative is a
// second copy of the ids in Go, free to disagree with the markup — and the
// disagreement would be invisible, because a wrong id renders as nothing at
// all. This is the same reasoning TestSpriteSymbolsCoverUses is built on; it
// scans the same file for the same attribute.
//
// The first brick of a general resource registry. Strings already have one (the
// message catalogue at /admin/i18n); images do not, and this is the ask a
// picker needs answered — "what may I choose" — for one kind of image.

// siteIcons is the parsed sprite id list, computed once.
var siteIcons = sync.OnceValue(func() []string {
	b, err := fs.ReadFile(site.FS, "web/templates/site_chrome.html")
	if err != nil {
		return nil
	}
	src := string(b)
	var out []string
	seen := map[string]bool{}
	for i := 0; ; {
		j := strings.Index(src[i:], `<symbol id="`)
		if j < 0 {
			break
		}
		start := i + j + len(`<symbol id="`)
		k := strings.IndexByte(src[start:], '"')
		if k < 0 {
			break
		}
		if id := src[start : start+k]; id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
		i = start + k
	}
	// Alphabetical: a dropdown is scanned by eye, and sprite-sheet order is
	// the order somebody happened to draw them in.
	sort.Strings(out)
	return out
})

// IconCatalogExtension is where this host publishes the list, as
// func() []string. Named per consumer the way the csrf and l10n seams are.
const IconCatalogExtension = "icons.catalogue"
