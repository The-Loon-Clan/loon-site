package request

import (
	"reflect"
	"testing"
)

// A POINTER field exists so "not asked" survives the parse, distinct from
// "asked for zero". The Newznab tvsearch narrowing is the case that wanted it:
// the indexer's schema stores season 0 as "never parsed", so a client that
// sent no season and one that sent season=0 must not look the same.
//
// Before pointers were supported, such a field fell to setField's default
// branch and Bind returned an "unsupported kind ptr" error — which the API
// handler ignores by design, so the parameter silently never arrived and the
// declared filter did nothing.
func TestBindPointerKeepsAbsentDistinctFromZero(t *testing.T) {
	type in struct {
		Season  *int `form:"season"`
		Episode *int `form:"ep"`
	}

	bind := func(vals map[string]string) in {
		t.Helper()
		var got in
		get := func(k string) (string, bool) { v, ok := vals[k]; return v, ok }
		getAll := func(string) []string { return nil }
		if err := bindStruct(reflect.ValueOf(&got).Elem(), get, getAll); err != nil {
			t.Fatalf("bind: %v", err)
		}
		return got
	}

	t.Run("absent stays nil", func(t *testing.T) {
		got := bind(map[string]string{})
		if got.Season != nil || got.Episode != nil {
			t.Errorf("absent params bound to %v/%v, want nil/nil — a client that "+
				"sent no narrowing must get an unfiltered feed", got.Season, got.Episode)
		}
	})

	t.Run("zero is asked-for, not absent", func(t *testing.T) {
		got := bind(map[string]string{"season": "0"})
		if got.Season == nil {
			t.Fatal("season=0 bound to nil — indistinguishable from not asking, " +
				"which is the whole reason this field is a pointer")
		}
		if *got.Season != 0 {
			t.Errorf("season = %d, want 0", *got.Season)
		}
	})

	t.Run("a real value arrives", func(t *testing.T) {
		got := bind(map[string]string{"season": "4", "ep": "1"})
		if got.Season == nil || *got.Season != 4 || got.Episode == nil || *got.Episode != 1 {
			t.Errorf("bound %v/%v, want 4/1", got.Season, got.Episode)
		}
	})
}
