package handlers

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// An id search this index cannot answer must come back EMPTY, never as the
// whole catalogue.
//
// A Newznab client narrows by external id and sends no q with it. Ignoring the
// unknown parameter left nothing to filter on, so "give me this one show by
// tvdbid" returned 160,673 releases presented as matches for it. The response
// is indistinguishable from a real answer, which is exactly why it is worth
// refusing: NZBHydra2 probes with these on every caps check, and production
// recorded the same client caching such answers as genuine matches.
func TestUnsupportedSearchIDIsDetected(t *testing.T) {
	for _, param := range []string{"tvdbid", "imdbid", "tmdbid", "tvmazeid", "traktid", "rid", "tvrageid"} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/api?t=tvsearch&"+param+"=123", nil)
		if got, ok := unsupportedSearchID(c); !ok || got != param {
			t.Errorf("%s= not detected (got %q, ok=%v) — that request would answer "+
				"with the entire index", param, got, ok)
		}
	}
}

// Presence is the test, not the value: a client that sent an empty tvdbid=
// still asked an id question, and answering it with everything is the same lie.
func TestUnsupportedSearchIDCountsAnEmptyValue(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/api?t=tvsearch&tvdbid=", nil)
	if _, ok := unsupportedSearchID(c); !ok {
		t.Error("an empty tvdbid= was treated as absent")
	}
}

// The ordinary requests must be untouched — including `id=`, which this API
// DOES support (get/details) and which a looser rule like "anything ending in
// id" would refuse.
func TestOrdinaryRequestsAreNotRefused(t *testing.T) {
	for _, q := range []string{
		"/api?t=search&q=Breaking+Bad",
		"/api?t=tvsearch&q=Breaking+Bad&season=4&ep=1",
		"/api?t=caps",
		"/api?t=search", // the newest-releases feed: legitimate Newznab
		"/api?t=get&id=abc123",
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", q, nil)
		if got, ok := unsupportedSearchID(c); ok {
			t.Errorf("%s was refused over %q — a working request now returns nothing", q, got)
		}
	}
}

// The refusal has to be a VALID feed a client can parse, and say zero.
func TestEmptyFeedIsAWellFormedZeroResult(t *testing.T) {
	body := string(emptyNewznabFeed(pluginapi.NewznabRequest{
		Title: "loon indexer", BaseURL: "http://localhost:8090",
	}).Body)
	for _, want := range []string{
		`<?xml version="1.0"`,
		`xmlns:newznab=`,
		`<newznab:response offset="0" total="0"/>`,
		`<title>loon indexer</title>`,
		`</rss>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("empty feed is missing %q — a client that cannot parse the "+
				"refusal learns nothing from it\n%s", want, body)
		}
	}
}
