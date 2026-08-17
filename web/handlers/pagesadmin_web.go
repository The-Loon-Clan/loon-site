package handlers

import (
	"github.com/the-loon-clan/loon-site/internal/markdown"
	"github.com/the-loon-clan/loon-site/internal/storage"

	"html/template"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// Editable site pages — /admin/pages, and the public serving that makes it
// real. The FAQ, rules and about pages shipped as templates; the templates
// stay as the FALLBACK (and the seed: a fresh database serves them
// unchanged), but a saved row replaces one, and an operator can add pages
// the templates never had, served at /pages/<slug>.
//
// Deliberately not the wiki. The wiki is member-facing reference material
// with topics, uploads and recent-changes; these are the site's own fixed
// prose — what the nav links unconditionally — and conflating them would put
// "Rules" in the wiki index between fan articles. What they share is the
// markdown pipeline, which is the host's own sanitising renderer either way.

// builtinPages maps the slugs that exist as templates to their canonical
// routes. A row for one of these REPLACES the template at its own URL; a
// missing row falls back. Everything else lives under /pages/<slug>.
var builtinPages = map[string]struct {
	Href  string
	Title string
}{
	"faq":   {"/faq", "FAQ"},
	"rules": {"/rules", "Rules"},
	"about": {"/about", "About"},
}

// sitePageSlugPattern is a URL segment: lowercase, digits, single dashes.
var sitePageSlugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// renderPageBody turns a saved page's body into HTML by its format:
// markdown through the sanitising pipeline, html verbatim. The verbatim arm
// is Drupal's "Full HTML" text format with the role check made structural —
// site_pages rows are only ever written through /admin/pages, which sits
// behind Require(RoleAdmin).
func renderPageBody(p storage.SitePage) template.HTML {
	if p.Format == "html" {
		return template.HTML(p.BodyMD)
	}
	return markdown.Render(p.BodyMD)
}

// prosePage serves a built-in page's canonical route: the saved row when one
// exists, the fallback template otherwise. The template name is a call-site
// LITERAL, not a builtinPages field, so renderpage_test's pageNameWrappers
// scan can check it the same way it checked sitePagePlain — a computed name
// is the hole that test exists to keep closed. EditHref is always set; the
// templates gate it on IsAdmin, which render() computes for every page.
func (w *web) prosePage(slug, fallbackPage string) gin.HandlerFunc {
	b := builtinPages[slug]
	return func(c *gin.Context) {
		data := map[string]any{"Title": b.Title, "EditHref": "/admin/pages?edit=" + slug}
		if p, ok := w.data.GetSitePage(c.Request.Context(), slug); ok {
			data["Title"] = p.Title
			data["Fragment"] = renderPageBody(p)
			w.render(c, "site_page.html", data)
			return
		}
		w.render(c, fallbackPage, data)
	}
}

// customSitePage serves /pages/:slug — operator-created pages with no
// template behind them. A built-in slug redirects to its canonical route so
// one page cannot live at two URLs.
func (w *web) customSitePage(c *gin.Context) {
	slug := c.Param("slug")
	if b, ok := builtinPages[slug]; ok {
		c.Redirect(http.StatusMovedPermanently, b.Href)
		return
	}
	p, ok := w.data.GetSitePage(c.Request.Context(), slug)
	if !ok {
		w.renderStatus(c, http.StatusNotFound, "site_page.html", map[string]any{
			"Title": "Not found", "Fragment": template.HTML("<p>No such page.</p>")})
		return
	}
	w.render(c, "site_page.html", map[string]any{
		"Title":    p.Title,
		"Fragment": renderPageBody(p),
		"EditHref": "/admin/pages?edit=" + p.Slug,
	})
}

// pageRow is one line of the admin list: every saved page plus every
// built-in, so the three the nav links are editable from here even before
// they have a row.
type pageRow struct {
	Slug    string
	Title   string
	Href    string
	Updated string
	// Custom means no template stands behind this slug: deleting it is
	// removal, not revert.
	Custom bool
	// Saved means a row exists. A built-in without one shows as "template".
	Saved bool
}

// adminPages serves GET /admin/pages: the list, and an editor for ?edit=.
func (w *web) adminPages(c *gin.Context) {
	ctx := c.Request.Context()
	saved, err := w.data.ListSitePages(ctx)
	if err != nil {
		c.String(http.StatusInternalServerError, "could not read the pages")
		return
	}
	bySlug := map[string]storage.SitePage{}
	for _, p := range saved {
		bySlug[p.Slug] = p
	}

	var rows []pageRow
	for _, slug := range []string{"about", "faq", "rules"} {
		b := builtinPages[slug]
		r := pageRow{Slug: slug, Title: b.Title, Href: b.Href}
		if p, ok := bySlug[slug]; ok {
			r.Title, r.Saved = p.Title, true
			r.Updated = p.UpdatedAt.Format("2 Jan 2006 15:04")
		}
		rows = append(rows, r)
	}
	for _, p := range saved {
		if _, builtin := builtinPages[p.Slug]; builtin {
			continue
		}
		rows = append(rows, pageRow{
			Slug: p.Slug, Title: p.Title, Href: "/pages/" + p.Slug,
			Updated: p.UpdatedAt.Format("2 Jan 2006 15:04"),
			Custom:  true, Saved: true,
		})
	}

	data := map[string]any{
		"Title": "Pages",
		"Rows":  rows,
		"Saved": c.Query(querySaved) == "1",
		"Err":   c.Query(queryErr),
	}
	// The editor: an existing row's content, or a blank form for a built-in
	// still on its template, or a fresh page when ?edit= names nothing yet.
	if slug := strings.TrimSpace(c.Query("edit")); slug != "" && sitePageSlugPattern.MatchString(slug) {
		data["EditSlug"] = slug
		_, isBuiltin := builtinPages[slug]
		data["EditIsBuiltin"] = isBuiltin
		if p, ok := bySlug[slug]; ok {
			data["EditTitle"], data["EditBody"], data["EditExists"] = p.Title, p.BodyMD, true
			data["EditFormat"] = p.Format
		} else if isBuiltin {
			data["EditTitle"] = builtinPages[slug].Title
		}
		// The Grav idea: placement lives on the page form. Custom pages only —
		// the built-ins already sit in the menu as builtin rows, moved at
		// /admin/nav like any other entry.
		if !isBuiltin {
			for _, e := range navRows() {
				if e.Href == "/pages/"+slug {
					data["EditNavGroup"], data["EditNavOrdinal"] = e.Grp, e.Ordinal
					break
				}
			}
		}
	}
	w.render(c, "admin_pages.html", data)
}

// adminPagesSave handles POST /admin/pages.
func (w *web) adminPagesSave(c *gin.Context) {
	slug := strings.TrimSpace(c.PostForm("slug"))
	if !sitePageSlugPattern.MatchString(slug) || len(slug) > 64 {
		c.Redirect(http.StatusSeeOther, "/admin/pages?"+queryErr+"=slugs+are+lowercase+with+dashes%2C+like+privacy-policy")
		return
	}
	title := strings.TrimSpace(c.PostForm("title"))
	if title == "" {
		// Same rule as achievements: the slug is a usable label, a blank
		// heading is not.
		title = slug
	}
	format := c.PostForm("format")
	if format != "html" {
		// The closed set, defaulted rather than refused: markdown is the
		// safe arm, and an absent field (an old form) must not error.
		format = "markdown"
	}
	if err := w.data.UpsertSitePage(c.Request.Context(), slug, title, c.PostForm("body_md"), format); err != nil {
		w.log.Error("site page save", "slug", slug, "err", err)
		c.Redirect(http.StatusSeeOther, "/admin/pages?"+queryErr+"=save+failed&edit="+slug)
		return
	}
	// Menu placement, custom pages only (built-ins have builtin nav rows).
	// "none" removes the entry; a group writes/moves it. Best-effort: a page
	// that saved but failed to place is a page, not a lost edit.
	if _, isBuiltin := builtinPages[slug]; !isBuiltin {
		href := "/pages/" + slug
		grp := c.PostForm("nav_group")
		ctx := c.Request.Context()
		if validNavGroup(grp) {
			ord := 1000
			if n, err := strconv.Atoi(strings.TrimSpace(c.PostForm("nav_ordinal"))); err == nil {
				ord = n
			}
			if err := w.data.UpsertSiteNav(ctx, storage.NavEntry{
				Href: href, Label: title, Grp: grp, Ordinal: ord,
			}); err != nil {
				w.log.Error("site page nav place", "slug", slug, "err", err)
			}
		} else if err := w.data.DeleteSiteNav(ctx, href); err != nil {
			w.log.Error("site page nav remove", "slug", slug, "err", err)
		}
		if err := refreshNavMirror(ctx, w.data); err != nil {
			w.log.Error("nav mirror refresh", "err", err)
		}
	}
	c.Redirect(http.StatusSeeOther, "/admin/pages?"+querySaved+"=1&edit="+slug)
}

// adminPagesDelete handles POST /admin/pages/delete. For a built-in slug
// this reverts the page to its template; for a custom one it removes the
// page outright.
func (w *web) adminPagesDelete(c *gin.Context) {
	slug := strings.TrimSpace(c.PostForm("slug"))
	if !sitePageSlugPattern.MatchString(slug) {
		c.Redirect(http.StatusSeeOther, "/admin/pages?"+queryErr+"=bad+slug")
		return
	}
	if err := w.data.DeleteSitePage(c.Request.Context(), slug); err != nil {
		w.log.Error("site page delete", "slug", slug, "err", err)
		c.Redirect(http.StatusSeeOther, "/admin/pages?"+queryErr+"=delete+failed")
		return
	}
	// A deleted custom page must leave the menu too — a built-in reverts to
	// its template and its nav row rightly stays.
	if _, isBuiltin := builtinPages[slug]; !isBuiltin {
		if err := w.data.DeleteSiteNav(c.Request.Context(), "/pages/"+slug); err != nil {
			w.log.Error("site page nav remove", "slug", slug, "err", err)
		}
		if err := refreshNavMirror(c.Request.Context(), w.data); err != nil {
			w.log.Error("nav mirror refresh", "err", err)
		}
	}
	c.Redirect(http.StatusSeeOther, "/admin/pages?"+querySaved+"=1")
}
