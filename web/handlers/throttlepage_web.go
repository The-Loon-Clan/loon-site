package handlers

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// The page a member sees when a rate limit refuses them.
//
// The limiter's own body was a good SENTENCE in a bad page: plain text, no
// nav, no styling, no way back — a member who typed a third search landed on a
// blank white browser-default screen, which reads as the site breaking rather
// than as a rule they tripped. The words were never the problem, so they are
// kept; the chrome around them is what this adds.
//
// HTML only. A fetch, an htmx swap or an API client is better served by the
// plain sentence, and handing them a full document to parse would be a second
// bug — so anything that did not ask for HTML falls through to the limiter's
// own response.
func (w *web) throttleRefused(c *gin.Context, secs int) {
	if !strings.Contains(c.GetHeader("Accept"), "text/html") {
		c.String(http.StatusTooManyRequests,
			"Too many requests. This is a rate limit, not an error: "+
				"wait %d second(s) and it will work again.", secs)
		return
	}
	// The status is written before the body, and it stays 429: a page whose
	// text says "slow down" must not tell a cache or a crawler 200.
	w.renderStatus(c, http.StatusTooManyRequests, "site_page.html", map[string]any{
		"Title": "Slow down a moment",
		"Fragment": template.HTML(fmt.Sprintf(
			`<p>This is a rate limit, not an error &mdash; you asked for pages faster `+
				`than the site serves them to one visitor.</p>`+
				`<p class="text-muted">Wait about %d second%s and it will work again. `+
				`Nothing was lost.</p>`+
				`<p><a class="btn btn-primary btn-sm" href="/">Back to the site</a></p>`,
			secs, plural(secs))),
	})
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
