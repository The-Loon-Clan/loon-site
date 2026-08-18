package handlers

import (
	"html/template"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// Widget shortcodes: a registered widget, dropped into an editable page's body
// by the operator who wrote the prose around it.
//
//	[widget staff]
//	[widget staff admin]
//	[widget ranks-groups assigned]
//
// The alternative was a widget REGION per page, arranged at /admin/widgets like
// the sidebars. It was rejected for one reason: a region can only put a card
// above or below the prose, never between two paragraphs — and "here is who
// runs the site, and here is how to reach them" is a page where the position
// inside the text is the whole point.
//
// Everything after the slug is the widget's config, passed through verbatim as
// the SAME single string a placement's config field holds (core.WidgetConfig).
// One contract, not two: a widget author writes one parser, and what an
// operator learns to type in the editor works in a page body as well.
//
// Expanded AFTER the body is rendered, which is what makes it safe in both page
// formats. A markdown body goes through the sanitising pipeline first, so a
// widget's own markup is never handed to the sanitiser to be stripped — and a
// shortcode is inert text until this runs, so nothing an author writes can
// smuggle HTML through markdown by dressing it up as one.

// widgetShortcode matches one shortcode: a slug, then the rest of the brackets
// as config. Slugs are the registry's own shape (lowercase, digits, dashes) and
// config stops at the closing bracket or the end of the line, so an unclosed
// shortcode consumes a line rather than the remainder of the page.
var widgetShortcode = regexp.MustCompile(`\[widget\s+([a-z0-9][a-z0-9-]*)([^\]\n]*)\]`)

// widgetShortcodeBlock is the same thing alone in its own paragraph, which is
// what markdown produces for a shortcode on its own line. Matched first so the
// wrapping <p> goes with it — a card inside a paragraph is invalid markup, and
// browsers close the <p> early, which puts everything after the card outside
// the paragraph it was written in.
var widgetShortcodeBlock = regexp.MustCompile(`(?i)<p>\s*` + widgetShortcode.String() + `\s*</p>`)

// expandWidgetShortcodes replaces every shortcode in a rendered page body.
//
// A shortcode that resolves to nothing — no such widget, not for this viewer,
// or the widget itself having nothing to say — is removed. That is the same
// contract a placement has (widgets_web.go drops an empty fragment rather than
// framing it), and it is what lets an operator write one page that serves a
// site with the plugin and a site without.
//
// The one exception is an unknown slug shown to an ADMIN, who gets a line
// naming it. A typo that silently renders nothing is indistinguishable from a
// widget that had nothing to say, and the person who can fix it is exactly the
// one who should be told the difference.
// The registry and the viewer are PARAMETERS rather than resolved inside.
// *core.Runtime keeps its Core unexported and can only be built by a Boot that
// insists on real storage, so reaching through w.rt here would have meant
// standing up a database to test one fake widget — and the viewer is the same
// person for every shortcode on the page, so resolving them once is also one
// session read instead of one per widget. The single caller (pageFragment)
// resolves both; everything below takes what it is handed.
func (w *web) expandWidgetShortcodes(c *gin.Context, in template.HTML, reg *core.Core, viewer *core.User) template.HTML {
	body := string(in)
	// The overwhelming majority of pages carry no shortcode at all, and this
	// runs on every page render.
	if !strings.Contains(body, "[widget") {
		return in
	}
	expand := func(match string) string {
		m := widgetShortcode.FindStringSubmatch(match)
		if m == nil {
			return match
		}
		return w.renderShortcodeWidget(c, reg, viewer, m[1], strings.TrimSpace(m[2]))
	}
	body = widgetShortcodeBlock.ReplaceAllStringFunc(body, expand)
	body = widgetShortcode.ReplaceAllStringFunc(body, expand)
	return template.HTML(body)
}

// renderShortcodeWidget resolves one slug and renders it for this viewer.
func (w *web) renderShortcodeWidget(c *gin.Context, reg *core.Core, viewer *core.User, slug, config string) string {
	if reg == nil {
		return ""
	}
	widget, ok := reg.WidgetBySlug(slug)
	if !ok {
		if viewer != nil && viewer.AtLeast(core.RoleAdmin) {
			return `<p class="text-muted small">No widget named &ldquo;` +
				template.HTMLEscapeString(slug) + `&rdquo; is registered here.</p>`
		}
		return ""
	}
	// An operator says WHERE a widget goes; the widget says who may see it. A
	// page body is no different from a region in that respect, so a members-only
	// widget on a public page renders nothing for anonymous readers rather than
	// making the page refuse them.
	if !widget.AllowsUser(viewer) {
		return ""
	}
	// Widget.Regions is deliberately NOT consulted. It is a hint about the shape
	// of a layout slot — "this wide table does not belong in a narrow sidebar" —
	// and a page body is the full-width main column, which is the one place
	// every widget fits.
	core.SetWidgetConfig(c, config)
	frag, err := widget.Render(c)
	if err != nil {
		// Swallowed exactly as a placement's error is: this runs inside somebody
		// else's page, and a broken widget must cost its own box, never the page
		// it was written into.
		w.log.Error("widget shortcode", "slug", slug, "err", err)
		return ""
	}
	if strings.TrimSpace(string(frag)) == "" {
		return ""
	}
	return `<div class="page-widget">` + string(frag) + `</div>`
}

// shortcodeHelp is the line the page editor shows above the body field, built
// from the registry so it names the widgets THIS site actually has. An operator
// cannot use a mechanism nobody told them about, and a hardcoded list of
// examples would name widgets a given site never installed.
func (w *web) shortcodeHelp() []widgetHelpRow {
	reg := w.registry()
	if reg == nil {
		return nil
	}
	var out []widgetHelpRow
	for _, x := range reg.Widgets() {
		out = append(out, widgetHelpRow{
			Slug: x.Slug, Title: x.Title, Description: x.Description,
			ConfigLabel: x.ConfigLabel, ConfigHint: x.ConfigHint,
		})
	}
	return out
}

// widgetHelpRow is one line of that help.
type widgetHelpRow struct {
	Slug        string
	Title       string
	Description string
	ConfigLabel string
	ConfigHint  string
}

// registry is the widget registry the runtime booted, nil-safe for a host
// wired without one.
func (w *web) registry() *core.Core {
	if w == nil || w.rt == nil {
		return nil
	}
	return w.rt.Core()
}
