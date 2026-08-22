package handlers

import (
	"html/template"
	"regexp"
	"strings"
)

// Hoisting a fragment's <style> blocks into the document head.
//
// A plugin returns a FRAGMENT and the host wraps the chrome around it. A
// fragment that needs CSS ships it in a <style> block, which is the only place
// it can put one — it has no access to the head. The host then inserts that
// fragment into an <article> or a <div>, and the result is invalid: <style>
// belongs in the head, and the W3C validator says so on twenty pages of this
// site. Browsers accept it, so nothing ever complained.
//
// The fix belongs on the HOST side, not in twenty plugin templates. A plugin
// cannot reach the head; the host can, and this is the seam where the fragment
// becomes a page.
//
// ORDER IS PRESERVED. Blocks come out in the order they appeared and go into
// the head together, after the site's own stylesheets, so a fragment rule that
// overrode a site rule still does.
//
// WHAT IT DOES NOT DO. It does not parse HTML. A <style> written inside a
// string in an inline <script>, or inside a comment, would be moved too, which
// is why the pattern requires the tag to open a line's worth of markup on its
// own rather than matching anywhere. If a fragment ever needs a literal
// "<style>" in text it must escape it, which is true of any markup in text.
var fragmentStyle = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style>`)

// hoistFragmentStyles splits a fragment into its body and its <style> blocks.
func hoistFragmentStyles(frag template.HTML) (template.HTML, template.HTML) {
	s := string(frag)
	if !strings.Contains(strings.ToLower(s), "<style") {
		return frag, ""
	}
	var styles strings.Builder
	body := fragmentStyle.ReplaceAllStringFunc(s, func(block string) string {
		styles.WriteString(block)
		styles.WriteString("\n")
		return ""
	})
	if styles.Len() == 0 {
		return frag, ""
	}
	return template.HTML(body), template.HTML(styles.String()) //nolint:gosec // both halves came from the same fragment
}
