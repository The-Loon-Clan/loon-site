package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-site/internal/i18n"
)

// Which language a request is in, and how a reader changes it.
//
// Three inputs, in order: an explicit ?lang= on this request, the reader's
// saved choice in a cookie, then the browser's Accept-Language. Anything
// unrecognised falls through to the next one and finally to English — there is
// no path that selects a language this site cannot render.

// langCookie holds the reader's own choice.
//
// A cookie rather than a column on users, because the readers who most need a
// language picker are the ones who have not signed up yet: a visitor deciding
// whether this site is for them cannot be asked to register first to find out.
// A logged-in preference belongs on top of this later, not instead of it.
const langCookie = "lang"

// langCookieMaxAge is a year. A language choice is not a session — a reader who
// picked Japanese last month has not changed their mind by closing the tab.
const langCookieMaxAge = 365 * 24 * 60 * 60

// localeKey is where the resolved locale is stashed on the gin context, so the
// renderer and chromeData agree without resolving twice.
const localeKey = "loon.locale"

// locale returns the language for this request, resolving once per request.
func (w *web) locale(c *gin.Context) i18n.Locale {
	if v, ok := c.Get(localeKey); ok {
		if loc, ok := v.(i18n.Locale); ok {
			return loc
		}
	}
	// ?lang= is a request to CHANGE language, not just to render one, so it is
	// persisted here rather than only honoured for this page. Without that the
	// picker works for exactly one click and the next link reverts.
	override := c.Query("lang")
	if override != "" {
		if loc, ok := i18n.ByKey(i18n.Match("", override).Key()); ok && override != "" {
			setLangCookie(c, loc.Key())
		}
	}
	if override == "" {
		if v, err := c.Cookie(langCookie); err == nil {
			override = v
		}
	}
	loc := i18n.Match(c.GetHeader("Accept-Language"), override)
	c.Set(localeKey, loc)
	return loc
}

// setLangCookie persists a choice. Path "/" so it survives the whole site, and
// HttpOnly false on purpose: this is a display preference with nothing secret
// in it, and a client-side picker should be able to read what is currently set
// rather than infer it from the page.
func setLangCookie(c *gin.Context, key string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     langCookie,
		Value:    key,
		Path:     "/",
		MaxAge:   langCookieMaxAge,
		SameSite: http.SameSiteLaxMode,
	})
}

// localeOption is one entry in a language picker.
type localeOption struct {
	Key    string
	Label  string
	Active bool
}

// localeOptions is the picker's data: every supported language, each named IN
// ITSELF.
//
// "日本語", not "Japanese" — a reader who needs the picker is by definition one
// who may not read the language the site is currently in, so a list written in
// that language is a list they cannot use.
func localeOptions(current i18n.Locale) []localeOption {
	labels := map[string]string{
		"en":      "English",
		"zh-Hans": "简体中文",
		"ja":      "日本語",
	}
	out := make([]localeOption, 0, len(i18n.Locales))
	for _, l := range i18n.Locales {
		label := labels[l.Key()]
		if label == "" {
			label = l.Key()
		}
		out = append(out, localeOption{Key: l.Key(), Label: label, Active: l.Key() == current.Key()})
	}
	return out
}
