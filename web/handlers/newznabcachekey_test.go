package handlers

import (
	"testing"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// This host caches Newznab responses under pluginapi.NewznabCacheKey. It used
// to skip the cache entirely for any request carrying season or episode,
// because the key did not hash them: tvsearch&q=X&season=4&ep=1 and
// tvsearch&q=X produced the same key, so whichever ran first answered for
// both -- a client that narrowed got the whole series, or one that did not got
// somebody else's narrowing, and either response looked perfectly valid.
//
// The key covers them now and the skip is gone. This is the test that says so,
// and it lives here rather than only upstream because THIS host is what breaks
// if it regresses -- silently, and in favour of a wrong cached answer.
func TestNewznabCacheKeySeparatesNarrowed(t *testing.T) {
	base := pluginapi.NewznabRequest{Function: "tvsearch", Query: "Shogun"}
	four, one, zero := 4, 1, 0

	keys := map[string]string{
		"unnarrowed":   pluginapi.NewznabCacheKey(base),
		"season4":      pluginapi.NewznabCacheKey(withSE(base, &four, nil)),
		"season4ep1":   pluginapi.NewznabCacheKey(withSE(base, &four, &one)),
		"season0":      pluginapi.NewznabCacheKey(withSE(base, &zero, nil)),
		"episode1only": pluginapi.NewznabCacheKey(withSE(base, nil, &one)),
	}
	seen := map[string]string{}
	for name, k := range keys {
		if prev, dup := seen[k]; dup {
			t.Errorf("%s and %s share a cache key; one would answer for the other", prev, name)
		}
		seen[k] = name
	}
	// The distinction that motivated pointers: "did not ask" must not collide
	// with "asked for zero".
	if keys["unnarrowed"] == keys["season0"] {
		t.Error(`no season and season=0 share a key; "did not ask" is not "asked for zero"`)
	}
}

func withSE(r pluginapi.NewznabRequest, season, ep *int) pluginapi.NewznabRequest {
	r.Season, r.Episode = season, ep
	return r
}
