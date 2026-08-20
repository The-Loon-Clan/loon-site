package handlers

import (
	"github.com/the-loon-clan/loon-site/internal/i18n"

	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/core"
)

// The message catalogue: operator-authored strings, one slug, one text per
// locale. This is the thing the achievements definition page's localization
// dropdowns read from — the dropdowns existed as a PENDING note from the day
// that page shipped, because a dropdown with no catalogue behind it is
// configuration that cannot work. This file is the catalogue.
//
// HOST-owned, deliberately. The strings are the operator's content, like news
// posts; the supported locale set is the host's (internal/i18n); and plugins
// go through registered seams rather than reading a table that is not theirs
// — two reads and one seed-only write (see registerI18nSeams below).
//
// Resolution order: the viewer's locale, then the default locale, then
// nothing — a missing translation falls back to the definition's own text
// columns, which stay mandatory for exactly this reason.

// i18nSlugPattern is what a catalogue slug may look like. Dotted lowercase,
// like every other slug vocabulary here (events, metrics), and enforced at
// CREATE so the dropdowns never fill with near-duplicates that differ by case
// or stray spaces.
var i18nSlugPattern = regexp.MustCompile(`^[a-z0-9]+([.-][a-z0-9]+)*$`)

// adminI18n serves GET /admin/i18n: the slug × locale grid.
func (w *web) adminI18n(c *gin.Context) {
	msgs, err := w.data.ListI18nMessages(c.Request.Context())
	if err != nil {
		c.String(http.StatusInternalServerError, "could not read the catalogue")
		return
	}
	// rows: slug -> locale -> text, plus the stable slug order the template
	// ranges (a Go map range order would reshuffle the grid on every load).
	bySlug := map[string]map[string]string{}
	var slugs []string
	for _, m := range msgs {
		if bySlug[m.Slug] == nil {
			bySlug[m.Slug] = map[string]string{}
			slugs = append(slugs, m.Slug)
		}
		bySlug[m.Slug][m.Locale] = m.Text
	}
	var locales []string
	for _, l := range i18n.Locales {
		locales = append(locales, l.Key())
	}
	// "LocaleKeys", not "Locales": render() overwrites "Locales" on every page
	// with the language picker's options, so that name cannot carry data.
	w.render(c, "admin_i18n.html", map[string]any{
		"Title":      "Localization",
		"Slugs":      slugs,
		"LocaleKeys": locales,
		"Rows":       bySlug,
		"Saved":      c.Query(querySaved) == "1",
		"Err":        c.Query(queryErr),
	})
}

// adminI18nSave handles POST /admin/i18n: every cell of the grid in one
// submit, plus optionally one new slug.
//
// Whole-grid saves rather than per-cell endpoints: translation is done in
// passes ("fill in the Japanese column"), and a save per cell would be thirty
// round trips through a page reload each. Field names are "t/<slug>/<locale>",
// parsed rather than trusted — an unknown locale key is dropped, so a stale
// form cannot invent a locale row.
func (w *web) adminI18nSave(c *gin.Context) {
	if err := c.Request.ParseForm(); err != nil {
		c.Redirect(http.StatusSeeOther, "/admin/i18n?"+queryErr+"=could+not+read+the+form")
		return
	}
	known := map[string]bool{}
	for _, l := range i18n.Locales {
		known[l.Key()] = true
	}
	ctx := c.Request.Context()

	// The new slug first, so its (empty) cells exist for the pass below when
	// an operator adds and fills it in one submit.
	if slug := strings.TrimSpace(c.PostForm("new_slug")); slug != "" {
		if !i18nSlugPattern.MatchString(slug) {
			c.Redirect(http.StatusSeeOther, "/admin/i18n?"+queryErr+"=slugs+are+dotted+lowercase%2C+like+ach.night-owl.title")
			return
		}
		// The default locale's row is what makes the slug EXIST — see
		// I18nSlugs, which lists distinct slugs. Empty text is fine; it is a
		// slug awaiting translation, which the grid shows honestly.
		if err := w.data.UpsertI18nMessage(ctx, slug, i18n.Default().Key(), strings.TrimSpace(c.PostForm("new_text"))); err != nil {
			c.Redirect(http.StatusSeeOther, "/admin/i18n?"+queryErr+"=could+not+add+the+slug")
			return
		}
	}

	for field, vals := range c.Request.PostForm {
		parts := strings.SplitN(field, "/", 3)
		if len(parts) != 3 || parts[0] != "t" || !known[parts[2]] || len(vals) == 0 {
			continue
		}
		if err := w.data.UpsertI18nMessage(ctx, parts[1], parts[2], strings.TrimSpace(vals[0])); err != nil {
			c.Redirect(http.StatusSeeOther, "/admin/i18n?"+queryErr+"=save+failed+on+"+parts[1])
			return
		}
	}
	c.Redirect(http.StatusSeeOther, "/admin/i18n?"+querySaved+"=1")
}

// resolveI18n answers a slug for the current viewer, falling back through the
// default locale. ok=false means "use your own fallback text".
func (w *web) resolveI18n(c *gin.Context, slug string) (string, bool) {
	loc := w.locale(c)
	if text, ok := w.data.ResolveI18n(c.Request.Context(), slug, loc.Key(), i18n.Default().Key()); ok {
		return text, true
	}
	return "", false
}

// declareI18n is the host's side of pluginapi.I18nDeclarer: seed-only writes
// under the DEFAULT locale, because the declared text is the fallback string
// and the fallback column is the host's to define. Validation refuses the
// whole batch on the first bad slug — a plugin shipping a malformed slug
// should fail its Provision loudly, not seed a vocabulary the resolve path
// can never be asked for.
func (w *web) declareI18n(ctx context.Context, defaults map[string]string) error {
	for slug := range defaults {
		if !i18nSlugPattern.MatchString(slug) {
			return fmt.Errorf("i18n.declare: slug %q is not dotted lowercase (like ach.night-owl.title)", slug)
		}
	}
	def := i18n.Default().Key()
	for slug, text := range defaults {
		if err := w.data.SeedI18nMessage(ctx, slug, def, strings.TrimSpace(text)); err != nil {
			return fmt.Errorf("i18n.declare: %s: %w", slug, err)
		}
	}
	return nil
}

// registerI18nSeams publishes the catalogue's three seams. All are looked up
// by plugins at Provision, so this MUST run before core.Boot:
//
//	achievements.l10n.slugs    the slug list, for definition-form dropdowns
//	achievements.l10n.resolve  slug -> text for the CURRENT VIEWER's locale
//	i18n.declare               pluginapi.I18nDeclarer — plugins seed defaults
//
// The declarer is registered AS the pluginapi type, not a bare func: a
// Lookup's type assertion matches identical types only, so a host that
// registered its own func type would hand every plugin a value that asserts
// to nothing, silently.
func registerI18nSeams(c *core.Core, w *web) error {
	if err := c.Register("achievements.l10n.slugs",
		func(ctx context.Context) ([]string, error) { return w.data.I18nSlugs(ctx) }); err != nil {
		return err
	}
	if err := c.Register("achievements.l10n.resolve",
		func(gc *gin.Context, slug string) (string, bool) { return w.resolveI18n(gc, slug) }); err != nil {
		return err
	}
	// The DECLARED contract, which is what a plugin should resolve: one key,
	// one interface, any number of consumers. The four per-plugin keys around
	// it are what this replaced — the same two closures registered four times,
	// which is what the comment below used to be describing rather than
	// apologising for.
	if err := c.Register(pluginapi.MessageCatalogueName,
		pluginapi.MessageCatalogue(hostCatalogue{w})); err != nil {
		return err
	}

	// Kept so a plugin pinned to an older pluginapi keeps working. Nothing new
	// should add a fifth — see SEAMS.md on the tier these belonged to.
	// The medals plugin reads the same catalogue through its own keys — one
	// key per consumer, the same closures.
	if err := c.Register("medals.l10n.slugs",
		func(ctx context.Context) ([]string, error) { return w.data.I18nSlugs(ctx) }); err != nil {
		return err
	}
	if err := c.Register("medals.l10n.resolve",
		func(gc *gin.Context, slug string) (string, bool) { return w.resolveI18n(gc, slug) }); err != nil {
		return err
	}
	return c.Register(pluginapi.I18nDeclarerName, pluginapi.I18nDeclarer(w.declareI18n))
}

// hostCatalogue adapts this host to pluginapi.MessageCatalogue.
//
// A named type rather than a struct literal of funcs, because a Lookup asserts
// on the exact type: registering a bare func where an interface is expected
// hands every plugin a value that asserts to nothing, silently — which is the
// failure the comment above registerI18nSeams already warns about for the
// declarer.
type hostCatalogue struct{ w *web }

func (h hostCatalogue) Slugs(ctx context.Context) ([]string, error) {
	return h.w.data.I18nSlugs(ctx)
}

func (h hostCatalogue) Resolve(gc *gin.Context, slug string) (string, bool) {
	return h.w.resolveI18n(gc, slug)
}
