package handlers

import (
	"github.com/the-loon-clan/loon-site/internal/i18n"

	"fmt"
	"hash/fnv"
	"html/template"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/the-loon-clan/loon/core"

	site "github.com/the-loon-clan/loon-site"
	"github.com/the-loon-clan/loon-site/internal/markdown"
)

// The template function map, and the helpers behind it.
//
// Lifted out of views.go, which had grown to 1,750 lines and answered "what is
// this file about" with four different answers. These are the functions
// TEMPLATES call — formatting, role labels, relative times, the small
// predicates the markup asks questions with — plus the two that decide which
// files a page is parsed from.
//
// They are pure, and that is why they are worth having together: everything
// here can be tested by calling it, which tmplhelpers_test.go and
// home_vm_test.go already do.
// pluginTemplates parses the set gin renders for every plugin that draws its UI
// through c.HTML rather than the demo's own per-page map: the shared site chrome
// plus each plugin's full documents. One set, not one per plugin, because gin
// holds exactly one HTMLRender.
//
// Reads through site.FS (the embedded copy by default) rather than the
// filesystem. The runtime image is distroless and carries ONLY the binary, so a
// disk-relative ParseGlob here matches no files and takes the whole process down
// at boot via main.go's os.Exit(1) — which is exactly what it used to do.
//
// Template names are base filenames, so the dirs below share one flat namespace:
// two plugins must not both ship an "index.html".
func pluginTemplates() (*template.Template, error) {
	return template.New("plugins").Funcs(tmplHelpers()).ParseFS(site.FS,
		"web/templates/site_chrome.html",
		"web/templates/forum/*.html",
		"web/templates/editor.html",
		"web/templates/plugin/*.html",
	)
}

// pageFiles is the parse list for one page, in the order described above. Split
// out of newWeb so render() can rebuild the same set when site.DevReload is on.
func pageFiles(page string) []string {
	files := []string{"web/templates/base.html", "web/templates/site_chrome.html"}
	for _, p := range sharedPartials[page] {
		files = append(files, "web/templates/"+p)
	}
	return append(files, "web/templates/"+page)
}

// tmplFuncs exposes host helpers to templates. {{captcha}} renders the
// Turnstile widget (empty when captcha is disabled), so any form can drop it
// in; everything else comes from tmplHelpers (pure, host-independent).
func (w *web) tmplFuncs(loc i18n.Locale) template.FuncMap {
	fns := tmplHelpers()
	fns["captcha"] = func() template.HTML { return w.captcha.Widget() }
	// timeAgo and shortDate are BOUND TO A LOCALE here, which is why there is
	// one parsed template set per language rather than one set and a locale
	// argument at every call site.
	//
	// The alternative was {{timeAgo $.Locale .CreatedAt}} in 76 templates, and
	// then in 83 plugin ones, and then forever in every new one — a change
	// nobody can finish and which fails silently where it is missed, because a
	// date in the wrong language still renders. Closing over the locale here
	// makes every existing call site correct without being edited.
	//
	// The cost is parsing the page set once per supported language at boot.
	// Templates are cheap and there are three languages; the sets are built
	// once and never rebuilt.
	fns["timeAgo"] = func(t time.Time) string { return loc.Ago(t, time.Now()) }
	fns["shortDate"] = loc.Short
	return fns
}

// tmplHelpers are the pure template helpers — no host state, no I/O, so the
// SAME map can be registered on the forum plugin's separate template set
// (forum_web.go parses full documents through gin's HTML set, not the demo's
// per-page map, and its chrome hand-duplicates base.html's header). Anything
// needing the web struct belongs in tmplFuncs instead.
//
//	bytes t     4831838208            -> "4.5 GB"     (release sizes)
//	timeAgo t   2026-08-04T09:00:00Z  -> "3 hours ago" ("" when zero)
//	shortDate t 2026-08-04T09:00:00Z  -> "4 Aug 2026"  ("" when zero)
//	hue s       "Some.Release.1080p"  -> 0..7         (poster fallback bucket)
//	initials s  "[Grp] Some.Release"  -> "GS"         (poster fallback text)
//	roleName r  core.RoleMod          -> "Moderator"
//	ordinal n   3                     -> "3rd"
//	add a b     1 1                   -> 2            (loop indexes)
//	dict k v …  "Row" . "Size" "lg"   -> map          (multi-arg templates)
func tmplHelpers() template.FuncMap {
	return template.FuncMap{
		// asset appends the build's content hash to a static URL, so a
		// stylesheet change is a new URL rather than a cached old one. See
		// assetversion_web.go.
		"asset":     assetURL,
		"bytes":     humanBytes,
		"timeAgo":   timeAgo,
		"shortDate": shortDate,
		"hue":       hueBucket,
		"initials":  initials,
		"roleName":  roleName,
		// pwmin is the minimum password length, so a form's minlength attribute
		// and its help text quote the number the server enforces rather than a
		// number someone typed once. See password_web.go.
		"pwmin":     func() int { return minPasswordLen },
		"roleSlug":  roleSlug,
		"roleLabel": roleLabel,
		"eqID":      eqID,
		"hasPrefix": strings.HasPrefix,
		"navActive": navActive,
		"inGroup":   inGroup,
		// prose renders a person-authored body through the site's one
		// sanitizing markdown pipeline (markdown_web.go). It exists because the
		// plugin-rendered pages hand their bodies over as plain strings — no
		// Deps.Markdown seam to route them through — and printing those with
		// {{.Body}} means HTML collapses every newline, so a multi-paragraph
		// support ticket arrives as one run-on block.
		"prose":    markdown.Render,
		"ordinal":  ordinal,
		"ellipsis": markdown.Ellipsis,
		"excerpt":  markdown.Excerpt,
		"str":      str_,
		"add":      func(a, b int) int { return a + b },
		"dict":     dict,
		// cond is the ternary the template language does not have. It earns its
		// place in ARGUMENT position: {{if}} is a statement and cannot appear
		// inside a dict literal, so passing a component an optional label meant
		// a $var and a three-line {{if}} above every call site.
		//
		// Takes `any`, not bool, and applies the SAME truthiness {{if}} does.
		// As a bool it rejected `cond .Since "…" ""` with "wrong type for
		// value" at EXECUTE time — which truncates the page mid-document and
		// still returns 200, so it looks like a blank section rather than an
		// error. A ternary that only accepts one type is a trap in a language
		// where every {{if}} accepts all of them.
		"cond": func(c, yes, no any) any {
			if templateTruth(c) {
				return yes
			}
			return no
		},
	}
}

// str_ renders a value as a plain string, dereferencing a *string first.
//
// It exists because {{print "/u/" .Name}} on a *string emits the POINTER —
// "/u/0x1129d1d30910" — while {{.Name}} beside it prints "bob", because
// html/template auto-indirects when printing a value on its own but not inside
// a fmt.Sprint of several. Plugin row structs use *string for nullable columns
// (the forum's LastPostUsername), so the result is a correct-looking name whose
// link is garbage: nothing errors, nothing logs, and the page looks right.
func str_(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case *string:
		if x == nil {
			return ""
		}
		return *x
	}
	return fmt.Sprint(v)
}

// navActive reports whether a nav entry covers the current path — an exact
// match, or a parent of it.
//
// An equality test loses the highlight the moment a nav target grows child
// pages: /admin/settings/usenet is the Settings page, and the subnav has to
// keep saying so. The child test is segment-aware for the same reason
// matchesSection's is — a bare strings.HasPrefix would make /admin/plugins a
// child of /admin/plug.
func navActive(path, href string) bool {
	href = strings.TrimSuffix(href, "/")
	return path == href || strings.HasPrefix(path, href+"/")
}

// inGroup reports whether the current path is one of the plugin pages merged
// into a host dropdown (see hostNavGroups). Without it a page that opted into
// Community would appear in that menu and still leave it unlit, which reads as
// the nav having lost track of where you are.
func inGroup(m map[string][]navItem, group, path string) bool {
	for _, it := range m[group] {
		if navActive(path, it.Href) {
			return true
		}
	}
	return false
}

// timeAgo renders a coarse "3 hours ago" for a past instant. A zero time (the
// crawler never learned a post date) renders empty rather than "56 years ago",
// and a clock-skewed future stamp reads "just now".
// relativeTime adapts timeAgo to the any-taking seam plugins ask for. They
// take `any` because a plugin's row type may carry a timestamp as time.Time, a
// pointer, or an interface field — and a seam that demanded one concrete shape
// would push that conversion into every caller.
//
// Anything unrecognised renders empty rather than a Go dump: a malformed
// timestamp should cost a line its "2 hours ago", not print a struct at a user.
func relativeTime(v any) string {
	switch t := v.(type) {
	case time.Time:
		return timeAgo(t)
	case *time.Time:
		if t == nil {
			return ""
		}
		return timeAgo(*t)
	}
	return ""
}

func timeAgo(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	plural := func(n int, unit string) string {
		if n == 1 {
			return "1 " + unit + " ago"
		}
		return strconv.Itoa(n) + " " + unit + "s ago"
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute")
	case d < 24*time.Hour:
		return plural(int(d/time.Hour), "hour")
	case d < 7*24*time.Hour:
		return plural(int(d/(24*time.Hour)), "day")
	case d < 30*24*time.Hour:
		return plural(int(d/(7*24*time.Hour)), "week")
	case d < 365*24*time.Hour:
		return plural(int(d/(30*24*time.Hour)), "month")
	default:
		return plural(int(d/(365*24*time.Hour)), "year")
	}
}

// shortDate is the human date form used in captions ("4 Aug 2026"). Empty for
// a zero time, so a template can {{if}} on it.
func shortDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2 Jan 2006")
}

// hueBucket maps any string (a release title, a username) onto a stable 0-7
// bucket, so a release with no cover art always gets the SAME gradient tile
// across page loads and processes. FNV-1a: cheap and deterministic.
//
// The modulus is 8 because that is exactly how many hue stops the stylesheet
// defines (.poster--h0 … .poster--h7, components.css). Emitting a bucket with
// no matching class is silent: --poster-hue just keeps its default and every
// such tile renders the same colour. If more stops are ever added to the CSS,
// raise this to match — templates index the class directly off this number.
func hueBucket(s string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return int(h.Sum32() % 8)
}

// initials takes up to two leading alphanumerics from the first words of a
// title, for the text on a cover-less poster tile. Scene punctuation is skipped,
// so "[SubGrp] Some.Show.S01E02" reads "SS".
func initials(s string) string {
	var out []rune
	inWord := false
	for _, r := range s {
		alnum := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !alnum {
			inWord = false
			continue
		}
		if !inWord {
			out = append(out, unicode.ToUpper(r))
			if len(out) == 2 {
				break
			}
		}
		inWord = true
	}
	return string(out)
}

// roleSlug maps a role onto the kebab token the .user-tag--<slug> classes and
// the --user-tag-<slug>-fg theme tokens key off.
//
// It takes `any` because usernames reach templates in two different shapes and
// both must render identically: host pages carry a typed core.Role, while the
// forum plugin hands back a free-text role string from its user_display view
// ("admin", "mod", "user", …). Anything unrecognised — including the empty
// string a plugin row with no role yields — falls back to "member", so an
// unknown role renders as a plain member rather than an unstyled tag.
func roleSlug(v any) string {
	// Plugin row structs carry a nullable role as *string (the forum's
	// LastPostRole). Without this, such a value matches no case below and every
	// last poster silently renders as "member" — the exact half-right output
	// eqID exists to prevent, and the reason normalising belongs in the helper
	// rather than at each call site.
	if p, ok := v.(*string); ok {
		if p == nil {
			return "member"
		}
		v = *p
	}
	switch r := v.(type) {
	case core.Role:
		switch {
		case r <= core.RoleBanned:
			return "banned"
		case r == core.RoleDisabled:
			return "disabled"
		case r == core.RoleContributor:
			return "contributor"
		case r == core.RoleMod:
			return "mod"
		case r >= core.RoleAdmin:
			return "admin"
		}
	case string:
		switch strings.ToLower(strings.TrimSpace(r)) {
		case "admin", "administrator", "owner":
			return "admin"
		case "mod", "moderator", "staff":
			return "mod"
		case "contributor", "uploader":
			return "contributor"
		case "banned":
			return "banned"
		case "disabled":
			return "disabled"
		}
	}
	return "member"
}

// eqID compares two user ids that arrive as different integer types.
//
// {{eq}} refuses this: core.User.ID is int64 while several plugins carry their
// user ids as plain int, and html/template reports "incompatible types for
// comparison" at EXECUTE time — which means the page half-renders in
// production rather than failing a build. Normalising both sides here is the
// only comparison an ownership check should use.
func eqID(a, b any) bool {
	toI64 := func(v any) (int64, bool) {
		switch n := v.(type) {
		case int:
			return int64(n), true
		case int32:
			return int64(n), true
		case int64:
			return n, true
		case uint:
			return int64(n), true
		case uint32:
			return int64(n), true
		case uint64:
			return int64(n), true
		}
		return 0, false
	}
	x, okA := toI64(a)
	y, okB := toI64(b)
	// Two non-numbers are not "equal" — a false here fails an ownership check
	// closed, which is the right direction for the thing this guards.
	return okA && okB && x == y
}

// roleLabel is roleName for the mixed-shape case. roleName takes a typed
// core.Role and is kept that way for the host's own call sites; the user-tag
// block also renders forum rows whose role is a plain string, so it needs a
// label helper that accepts both. Derived from roleSlug so the label a tag
// shows can never disagree with the colour it is painted.
func roleLabel(v any) string {
	switch roleSlug(v) {
	case "admin":
		return "Admin"
	case "mod":
		return "Moderator"
	case "contributor":
		return "Contributor"
	case "banned":
		return "Banned"
	case "disabled":
		return "Disabled"
	}
	return "Member"
}

// roleName is the display label for a role level — the same names the
// user_display view exposes to plugin SQL, title-cased for the UI.
func roleName(r core.Role) string {
	switch {
	case r <= core.RoleBanned:
		return "Banned"
	case r == core.RoleDisabled:
		return "Disabled"
	case r == core.RoleContributor:
		return "Contributor"
	case r == core.RoleMod:
		return "Moderator"
	case r >= core.RoleAdmin:
		return "Admin"
	default:
		return "Member"
	}
}

// ordinal renders a 1-based rank as "1st"/"2nd"/"3rd"/"4th" for rank chips.
func ordinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return strconv.Itoa(n) + suffix
}

// dict builds a map from alternating key/value pairs, so a shared {{define}}
// can take more than one value: {{template "poster" dict "Row" . "Size" "lg"}}.
// An odd argument count or a non-string key fails the render loudly rather
// than silently dropping a value.
func dict(kv ...any) (map[string]any, error) {
	if len(kv)%2 != 0 {
		return nil, fmt.Errorf("dict: got %d arguments, want an even number of key/value pairs", len(kv))
	}
	m := make(map[string]any, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict: argument %d is %T, want a string key", i, kv[i])
		}
		m[k] = kv[i+1]
	}
	return m, nil
}
