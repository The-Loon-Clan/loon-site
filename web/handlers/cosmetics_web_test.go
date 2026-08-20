package handlers

import (
	"os"
	"strings"
	"testing"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// TestEveryEffectHasCSS is the seam between the two halves of a name effect.
//
// The catalogue lives in pluginapi because the plugin SELLS an effect and this
// host DRAWS it, and drawing it means a rule in this stylesheet. Nothing at
// runtime notices when they disagree: the purchase succeeds, the class goes on
// the name, no rule matches, and the member's name looks exactly as it did
// before. They paid for that.
func TestEveryEffectHasCSS(t *testing.T) {
	css, err := os.ReadFile("../static/css/components.css")
	if err != nil {
		t.Fatalf("reading the stylesheet: %v", err)
	}
	sheet := string(css)
	for _, e := range pluginapi.Effects {
		class := pluginapi.EffectClass(e.Slug)
		if class == "" {
			t.Errorf("%s: EffectClass returned empty for a catalogue entry", e.Slug)
			continue
		}
		// A selector rather than the bare string, so a slug that only appears
		// inside a comment does not count as covered.
		if !strings.Contains(sheet, "."+class+" ") &&
			!strings.Contains(sheet, "."+class+",") &&
			!strings.Contains(sheet, "."+class+"::") &&
			!strings.Contains(sheet, "."+class+"{") {
			t.Errorf("%s (%s): no rule for .%s in components.css — this effect "+
				"is sellable and undrawable", e.Slug, e.Label, class)
		}
	}
}

// TestAnimatedEffectsRespectReducedMotion checks the one accessibility promise
// this feature makes. An animation on a USERNAME is motion in the middle of
// text somebody is trying to read, in a table of forty of them.
func TestAnimatedEffectsRespectReducedMotion(t *testing.T) {
	css, err := os.ReadFile("../static/css/components.css")
	if err != nil {
		t.Fatalf("reading the stylesheet: %v", err)
	}
	sheet := string(css)
	i := strings.Index(sheet, "@media (prefers-reduced-motion: reduce)")
	if i < 0 {
		t.Fatal("no prefers-reduced-motion block at all")
	}
	// Everything from the first reduced-motion block onward: there may be more
	// than one, and an effect answered by any of them is answered.
	rest := sheet[i:]
	for _, e := range pluginapi.Effects {
		if !e.Animated {
			continue
		}
		if !strings.Contains(rest, "."+pluginapi.EffectClass(e.Slug)) {
			t.Errorf("%s is animated and never named under prefers-reduced-motion",
				e.Slug)
		}
	}
}
