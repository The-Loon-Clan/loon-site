package i18n

import (
	"strings"
	"testing"
	"time"
)

// English must come out EXACTLY as it did before this package existed.
//
// timeAgo and shortDate in tmplfuncs.go produced these strings, and they are on
// most pages of the site. A locale layer that quietly changes the wording of
// the default language has broken something for every existing reader in order
// to serve a reader it does not have yet.
func TestEnglishIsUnchanged(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	en := Default()
	for _, tc := range []struct {
		ago  time.Duration
		want string
	}{
		{0, "just now"},
		{59 * time.Second, "just now"},
		{time.Minute, "1 minute ago"},
		{2 * time.Minute, "2 minutes ago"},
		{time.Hour, "1 hour ago"},
		{23 * time.Hour, "23 hours ago"},
		{24 * time.Hour, "1 day ago"},
		{6 * 24 * time.Hour, "6 days ago"},
		{7 * 24 * time.Hour, "1 week ago"},
		{29 * 24 * time.Hour, "4 weeks ago"},
		{30 * 24 * time.Hour, "1 month ago"},
		{364 * 24 * time.Hour, "12 months ago"},
		{365 * 24 * time.Hour, "1 year ago"},
	} {
		if got := en.Ago(now.Add(-tc.ago), now); got != tc.want {
			t.Errorf("en.Ago(now-%s) = %q, want %q", tc.ago, got, tc.want)
		}
	}
	if got := en.Short(time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC)); got != "4 Aug 2026" {
		t.Errorf("en.Short = %q, want %q", got, "4 Aug 2026")
	}
	if got := en.Short(time.Time{}); got != "" {
		t.Errorf("en.Short(zero) = %q, want empty so a template can {{if}} on it", got)
	}
	if got := en.Ago(time.Time{}, now); got != "" {
		t.Errorf("en.Ago(zero) = %q, want empty", got)
	}
}

// Every locale must actually SAY something for every unit.
//
// The failure this catches is a locale added with three of six units filled in:
// the zero Forms formats as "%!d(MISSING)" or worse, an empty string, and the
// page shows a blank where a date was. It is invisible in review because the
// table looks full.
func TestEveryLocaleCoversEveryUnit(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	spans := []time.Duration{
		2 * time.Minute, 2 * time.Hour, 2 * 24 * time.Hour,
		2 * 7 * 24 * time.Hour, 2 * 30 * 24 * time.Hour, 2 * 365 * 24 * time.Hour,
	}
	for _, l := range Locales {
		if l.JustNow == "" {
			t.Errorf("%s: no JustNow string", l.Key())
		}
		if l.Date == "" {
			t.Errorf("%s: no date layout", l.Key())
		}
		for _, d := range spans {
			got := l.Ago(now.Add(-d), now)
			if got == "" {
				t.Errorf("%s: empty result for %s — a unit is missing from the table", l.Key(), d)
			}
			if strings.Contains(got, "%") || strings.Contains(got, "MISSING") {
				t.Errorf("%s: %s formatted as %q — the format string is wrong", l.Key(), d, got)
			}
			if !strings.ContainsAny(got, "0123456789") {
				t.Errorf("%s: %s formatted as %q with no number in it", l.Key(), d, got)
			}
		}
		// And the date layout must actually substitute. A layout with no
		// reference components in it formats every date to the same literal.
		a := l.Short(time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC))
		b := l.Short(time.Date(2019, 1, 31, 0, 0, 0, 0, time.UTC))
		if a == b {
			t.Errorf("%s: two different dates both format as %q — the layout has no components", l.Key(), a)
		}
	}
}

// The singular is not the plural, in the languages that have one.
func TestEnglishSingularIsNotPluralised(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	for _, d := range []time.Duration{
		time.Minute, time.Hour, 24 * time.Hour,
		7 * 24 * time.Hour, 30 * 24 * time.Hour, 365 * 24 * time.Hour,
	} {
		got := Default().Ago(now.Add(-d), now)
		if strings.HasPrefix(got, "1 ") && strings.Contains(got, "s ago") {
			t.Errorf("Ago(now-%s) = %q", d, got)
		}
	}
}

func TestMatch(t *testing.T) {
	for _, tc := range []struct {
		name, accept, override, want string
	}{
		{"no headers at all falls back", "", "", "en"},
		{"plain english", "en-GB,en;q=0.9", "", "en"},
		{"chinese", "zh-CN,zh;q=0.9", "", "zh-Hans"},
		{"japanese", "ja,en-US;q=0.8", "", "ja"},
		// The reader's own choice beats the browser's. Without this a language
		// picker does nothing on somebody else's laptop, which is exactly the
		// machine a reader most needs one on.
		{"override beats accept-language", "en-US,en;q=0.9", "ja", "ja"},
		{"override of a regional variant resolves to its base", "en", "zh-TW", "zh-Hans"},
		{"a garbage override is ignored, not fatal", "ja", "!!!!", "ja"},
		{"an unsupported override falls through to the header", "ja", "ko", "ja"},
		// A language nobody here speaks must NOT be matched to whichever
		// supported tag happens to be least dissimilar.
		{"an unsupported language gets the default", "ko-KR,ko;q=0.9", "", "en"},
		{"a malformed header is not fatal", "!!!!", "", "en"},
	} {
		if got := Match(tc.accept, tc.override).Key(); got != tc.want {
			t.Errorf("%s: Match(%q, %q) = %s, want %s", tc.name, tc.accept, tc.override, got, tc.want)
		}
	}
}

// Every locale's key round-trips, because the key is what keys the per-locale
// template map — a key that does not resolve is a page that cannot render.
func TestKeysRoundTrip(t *testing.T) {
	for _, l := range Locales {
		got, ok := ByKey(l.Key())
		if !ok {
			t.Errorf("%s does not resolve by its own key", l.Key())
			continue
		}
		if got.Tag != l.Tag {
			t.Errorf("%s resolved to %s", l.Key(), got.Key())
		}
	}
	if _, ok := ByKey("kl-GL"); ok {
		t.Error("ByKey accepted a language that is not supported")
	}
}
