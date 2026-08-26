package handlers

import (
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The external ids a Newznab client narrows by, none of which this index
// stores — which is why caps advertises none of them.
//
// Listed rather than pattern-matched, because "any parameter ending in id" also
// catches `id=` (the get/details parameter, which this API does support) and
// would refuse a working request. These six are what NZBHydra2 probes with and
// what Sonarr, Radarr and Prowlarr send.
var newznabSearchIDs = []string{
	"tvdbid", "rid", "tvrageid", "tvmazeid", "traktid", "imdbid", "tmdbid",
}

// unsupportedSearchID reports the first external id the request carries, if
// any. Presence is what matters, not the value: an empty tvdbid= is a client
// that did not fill it in, and answering that with the whole index is the same
// lie as answering a populated one.
func unsupportedSearchID(c *gin.Context) (string, bool) {
	for _, name := range newznabSearchIDs {
		if _, present := c.GetQuery(name); present {
			return name, true
		}
	}
	return "", false
}

// emptyNewznabFeed is a valid, zero-result Newznab response.
//
// Built here rather than asked of the indexer because it is the HOST refusing
// its own API surface: nothing is being searched, so there is nothing to ask.
// Every variable part is a value the host already set on the request, so this
// is not a second copy of anything the plugin owns — and the shape matches what
// a genuine no-match search returns, so a client cannot tell a refusal from an
// honest miss, which is the point.
func emptyNewznabFeed(req pluginapi.NewznabRequest) pluginapi.NewznabResult {
	return pluginapi.NewznabResult{
		ContentType: "application/xml; charset=utf-8",
		Body: []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
  <channel>
    <title>%s</title>
    <description>%s NZB Search</description>
    <link>%s</link>
    <newznab:response offset="0" total="0"/>
  </channel>
</rss>`, req.Title, req.Title, req.BaseURL)),
	}
}
