// Package i18n resolves which language a request is in, and formats the two
// things this site prints that a message catalogue cannot cover: dates, and
// how long ago something happened.
//
// It deliberately does NOT do message lookup yet. That is a bigger job — the
// strings live in 76 host templates and 83 plugin ones, and the plugin half
// needs a seam through Deps that does not exist. This is the plumbing under it:
// a locale on every request, an honest <html lang>, and the formatting that a
// catalogue could never fix because it happens in Go rather than in markup.
//
// Worth having even at one language. A hardcoded lang="en" is wrong for a
// screen reader the moment a page shows anything else, and t.Format("2 Jan
// 2006") is a decision about English that is invisible until somebody looks
// for it.
package i18n

import (
	"fmt"
	"strings"
	"time"

	"golang.org/x/text/language"
)

// Unit is a granularity of elapsed time. Its own type so a locale table cannot
// silently miss one: adding a unit here breaks every locale that has not
// spelled it out, at compile time.
type Unit int

const (
	Minute Unit = iota
	Hour
	Day
	Week
	Month
	Year
	numUnits
)

// Forms is one unit's wording. Two forms, because English needs two and the
// languages that need more are not supported yet — Russian and Polish take
// three or four, and adding one is a change to this struct rather than a
// workaround at a call site.
//
// One and Other are format strings taking the count. For Chinese and Japanese
// they are identical: neither language inflects for number, and writing the
// same string twice is the honest way to say so.
type Forms struct{ One, Other string }

// Locale is everything this package knows about one language.
type Locale struct {
	Tag language.Tag
	// Date is a Go reference layout. Not a CLDR pattern: three locales do not
	// justify a CLDR dependency, and Go's layout handles the CJK forms
	// ("2006年1月2日") because it substitutes components rather than parsing a
	// grammar.
	Date string
	// JustNow covers anything under a minute.
	JustNow string
	units   [numUnits]Forms
}

// Ago renders how long before now t was, in this locale.
//
// now is a parameter rather than a call to time.Now inside, so every boundary
// is testable without freezing a clock.
func (l Locale) Ago(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	pick := func(n int, u Unit) string {
		f := l.units[u]
		if n == 1 {
			return fmt.Sprintf(f.One, n)
		}
		return fmt.Sprintf(f.Other, n)
	}
	switch {
	case d < time.Minute:
		return l.JustNow
	case d < time.Hour:
		return pick(int(d/time.Minute), Minute)
	case d < 24*time.Hour:
		return pick(int(d/time.Hour), Hour)
	case d < 7*24*time.Hour:
		return pick(int(d/(24*time.Hour)), Day)
	case d < 30*24*time.Hour:
		return pick(int(d/(7*24*time.Hour)), Week)
	case d < 365*24*time.Hour:
		return pick(int(d/(30*24*time.Hour)), Month)
	default:
		return pick(int(d/(365*24*time.Hour)), Year)
	}
}

// Short is the caption date form. Empty for a zero time, so a template can
// {{if}} on it.
func (l Locale) Short(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(l.Date)
}

// Key is the BCP-47 string: what goes in <html lang>, in a cookie, and in the
// per-locale template map.
func (l Locale) Key() string { return l.Tag.String() }

// Locales are the languages this site claims to support, English first.
//
// FIRST IS THE DEFAULT, and it is also the fallback the matcher returns for a
// language nobody here speaks. Claiming a locale means having its strings; a
// tag listed with English wording underneath is worse than not listing it,
// because a reader who picks it learns the site lied rather than that it is
// untranslated.
var Locales = []Locale{
	{
		Tag: language.English, Date: "2 Jan 2006", JustNow: "just now",
		units: [numUnits]Forms{
			Minute: {"%d minute ago", "%d minutes ago"},
			Hour:   {"%d hour ago", "%d hours ago"},
			Day:    {"%d day ago", "%d days ago"},
			Week:   {"%d week ago", "%d weeks ago"},
			Month:  {"%d month ago", "%d months ago"},
			Year:   {"%d year ago", "%d years ago"},
		},
	},
	{
		// Simplified Chinese. The date is year-month-day with the unit
		// characters, which is how a date is written rather than a translation
		// of the English order.
		Tag: language.SimplifiedChinese, Date: "2006年1月2日", JustNow: "刚刚",
		units: [numUnits]Forms{
			Minute: {"%d分钟前", "%d分钟前"},
			Hour:   {"%d小时前", "%d小时前"},
			Day:    {"%d天前", "%d天前"},
			Week:   {"%d周前", "%d周前"},
			Month:  {"%d个月前", "%d个月前"},
			Year:   {"%d年前", "%d年前"},
		},
	},
	{
		Tag: language.Japanese, Date: "2006年1月2日", JustNow: "たった今",
		units: [numUnits]Forms{
			Minute: {"%d分前", "%d分前"},
			Hour:   {"%d時間前", "%d時間前"},
			Day:    {"%d日前", "%d日前"},
			Week:   {"%d週間前", "%d週間前"},
			Month:  {"%dか月前", "%dか月前"},
			Year:   {"%d年前", "%d年前"},
		},
	},
}

// Default is what an unmatched request gets.
func Default() Locale { return Locales[0] }

var matcher = func() language.Matcher {
	tags := make([]language.Tag, len(Locales))
	for i, l := range Locales {
		tags[i] = l.Tag
	}
	return language.NewMatcher(tags)
}()

// Match picks a locale for a request.
//
// override wins over the browser: it is the reader's explicit choice, held in a
// cookie, and a site that overrules that with Accept-Language is a site whose
// language picker does not work on a borrowed laptop.
//
// Both inputs are untrusted strings straight off the wire. ParseAcceptLanguage
// rejects malformed input rather than guessing, and an unmatched tag falls back
// to Locales[0] — there is no path here that returns a locale this site cannot
// render.
func Match(acceptLanguage, override string) Locale {
	if override = strings.TrimSpace(override); override != "" {
		if t, err := language.Parse(override); err == nil {
			if l, ok := byTag(t); ok {
				return l
			}
		}
	}
	tags, _, err := language.ParseAcceptLanguage(acceptLanguage)
	if err != nil || len(tags) == 0 {
		return Default()
	}
	_, i, conf := matcher.Match(tags...)
	// language.No means the matcher found nothing better than the default and
	// is saying so. Taking it anyway would serve Chinese to a browser that
	// asked for Korean because both are "not English".
	if conf == language.No {
		return Default()
	}
	return Locales[i]
}

// byTag finds a supported locale by exact base language.
//
// Base rather than exact tag: a reader who saved "zh-Hant" or "ja-JP" gets the
// closest thing this site has rather than silently falling back to English,
// which is what an exact-match lookup would do to every regional variant.
func byTag(t language.Tag) (Locale, bool) {
	base, _ := t.Base()
	for _, l := range Locales {
		lb, _ := l.Tag.Base()
		if lb == base {
			return l, true
		}
	}
	return Locale{}, false
}

// ByKey resolves a stored key ("en", "zh-Hans", "ja") back to a locale. Used
// for the per-locale template sets, where the key came from Locales in the
// first place and a miss is a programming error rather than bad input.
func ByKey(key string) (Locale, bool) {
	for _, l := range Locales {
		if l.Key() == key {
			return l, true
		}
	}
	return Locale{}, false
}
