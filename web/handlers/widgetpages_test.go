package handlers

import "testing"

func TestPagesMatch(t *testing.T) {
	for _, c := range []struct {
		name, rule, path string
		want             bool
	}{
		// Empty is every page: this is what every placement written before the
		// rule existed means, so getting it wrong changes every site silently.
		{"empty rule, any page", "", "/admin/widgets", true},
		{"whitespace only", "  \n\t\n ", "/anything", true},

		// The case this was built for: a poll on the front page only.
		{"front page only, on it", "/", "/", true},
		{"front page only, elsewhere", "/", "/browse", false},
		{"front page only, deeper", "/", "/c/usenet", false},

		{"exact page", "/browse", "/browse", true},
		{"exact page, not a child", "/browse", "/browse/tv", false},
		{"trailing slash is the same page", "/browse", "/browse/", true},

		{"prefix takes children", "/community*", "/community/forums", true},
		{"prefix takes itself", "/community*", "/community", true},
		{"prefix does not take a sibling", "/community*", "/communities", false},

		// Only-excludes is the single line people actually write. Rendering
		// nowhere here would look exactly like the feature not working.
		{"exclude only, ordinary page", "!/admin*", "/browse", true},
		{"exclude only, excluded page", "!/admin*", "/admin/widgets", false},

		{"include and exclude", "/*\n!/admin*", "/browse", true},
		{"exclude wins over include", "/*\n!/admin*", "/admin/widgets", false},
		{"exclude wins whatever the order", "!/admin*\n/*", "/admin/widgets", false},

		{"several includes", "/\n/browse\n/help*", "/help/donate", true},
		{"several includes, no match", "/\n/browse\n/help*", "/wiki", false},

		{"comments and blanks are ignored", "# the front page\n\n/\n", "/", true},
		// A rule that is ONLY a comment is a rule nobody has written yet, so
		// it means what empty means. The first version of this test asserted
		// the opposite — that it should match nothing — which would hide a
		// widget the moment an operator typed a note above an empty box.
		{"only a comment is the same as empty", "# /browse", "/browse", true},
		{"only a comment, any page", "# nothing here yet", "/wiki", true},
	} {
		if got := pagesMatch(c.rule, c.path); got != c.want {
			t.Errorf("%s: pagesMatch(%q, %q) = %v, want %v",
				c.name, c.rule, c.path, got, c.want)
		}
	}
}
