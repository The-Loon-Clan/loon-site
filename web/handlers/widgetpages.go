package handlers

import "strings"

// Which pages a placed widget appears on.
//
// A region — sidebar, header bar, footer — is drawn by the chrome on every
// page that has one, so a widget placed in a region was placed on the whole
// site. That is right for a site notice and wrong for almost everything else:
// the demo's poll sat in the right sidebar of every admin screen, every wiki
// page and every release, because "in the sidebar" was the only thing a
// placement could say.
//
// THE RULE, and it is deliberately small. An operator types paths, not a
// pattern language:
//
//	(empty)        every page — what every placement meant before this existed
//	/              the front page, and only it
//	/browse        that page, and only it
//	/community*    that page and everything under it
//	!/admin*       everywhere EXCEPT the admin area
//
// One rule per line. Blank lines and lines starting with # are ignored, so an
// operator can leave themselves a note.
//
// EXCLUDES WIN, and are evaluated first: "/*" with "!/admin*" is everywhere
// but admin, and no ordering question arises. If any include is present the
// path must match one of them; if there are only excludes, everything else is
// included. That last case is the one people actually want when they write a
// single !line, and getting it wrong would render the widget nowhere and look
// like the feature is broken.
//
// A trailing * is the only wildcard. Not a glob and not a regex: the operator
// is naming pages, the set of paths is small and known, and every pattern
// language invites a rule nobody can predict the effect of. If this ever needs
// more, the honest next step is a page PICKER, not a syntax.
func pagesMatch(rule, path string) bool {
	if strings.TrimSpace(rule) == "" {
		return true
	}
	// A trailing slash is the same page: /browse/ is /browse. The front page
	// is the exception — "/" is not a trailing slash to strip.
	if len(path) > 1 {
		path = strings.TrimRight(path, "/")
		if path == "" {
			path = "/"
		}
	}

	var includes []string
	for _, line := range strings.Split(rule, "\n") {
		p := strings.TrimSpace(line)
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		if neg := strings.HasPrefix(p, "!"); neg {
			if patternMatches(strings.TrimSpace(p[1:]), path) {
				return false
			}
			continue
		}
		includes = append(includes, p)
	}
	if len(includes) == 0 {
		return true // only excludes, and none of them matched
	}
	for _, p := range includes {
		if patternMatches(p, path) {
			return true
		}
	}
	return false
}

// patternMatches is one line against one path: exact, or a trailing-* prefix.
func patternMatches(pattern, path string) bool {
	if pattern == "" {
		return false
	}
	if len(pattern) > 1 {
		pattern = strings.TrimRight(pattern, "/")
		if pattern == "" {
			pattern = "/"
		}
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(path, strings.TrimSuffix(pattern, "*"))
	}
	return path == pattern
}
