package handlers

import "testing"

// nameByID exists because `index $names .` does not work on a *int, and the
// failure mode is the dangerous one: html/template streams, so the page returns
// 200 and stops mid-render. /admin/donate did exactly that, unnoticed, because
// the page was in no nav and the crawler never reached it.
func TestNameByID(t *testing.T) {
	names := map[int]string{7: "alice", 8: "bob"}
	seven, eight := 7, 8
	var nilInt *int
	var nilInt64 *int64
	i64 := int64(7)

	for _, tc := range []struct {
		name string
		id   any
		want string
	}{
		{"a plain int", 7, "alice"},
		{"an int64", int64(8), "bob"},
		{"a *int — the case that broke the page", &seven, "alice"},
		{"another *int", &eight, "bob"},
		{"a *int64", &i64, "alice"},
		{"a nil *int", nilInt, ""},
		{"a nil *int64", nilInt64, ""},
		{"an untyped nil", nil, ""},
		{"an id nobody has", 999, ""},
		{"something else entirely", "seven", ""},
	} {
		if got := nameByID(names, tc.id); got != tc.want {
			t.Errorf("%s: nameByID(%v) = %q, want %q", tc.name, tc.id, got, tc.want)
		}
	}
}

// TestNameByIDOnANilMap — a view model that never populated the map must not
// panic the render it was meant to decorate.
func TestNameByIDOnANilMap(t *testing.T) {
	id := 7
	if got := nameByID(nil, &id); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
