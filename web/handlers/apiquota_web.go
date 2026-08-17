package handlers

import (
	"github.com/the-loon-clan/loon-baseline/apikey"

	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon/schedule"
)

// The API quota, enforced where the demo actually serves the API.
//
// The key existed (loon-baseline/apikey, ?apikey= on /api and /rss) and the
// limits were DECLARED (the "Search API" service config: rate_per_day and
// friends, admin-editable under Jobs → Config) — but the declaration's
// enforcement loop lives in prod's separate loon-api read tier, and this
// host's /api never even resolved the key. So a Prowlarr pointed here
// authenticated with nothing and counted against nothing.
//
// Now every /api and /rss request resolves to a SUBJECT and counts:
//
//	u:<id>      a valid ?apikey= — attributed to the member
//	ip:<addr>   no key on a public-browsing site — anonymous but still
//	            capped, so dropping the key is not a way around the quota
//
// and the daily cap (rate_per_day, 0 = off) refuses with the Newznab error
// vocabulary clients understand. Members-only sites require the key
// outright — the access map has always said /api carries its own
// credential; this is that sentence made true.
//
// Staff are exempt from refusal but still counted: an admin debugging their
// own Prowlarr should see their graph move, not consume a member's story.

// apiServiceName must match the RegisterService call in wireBaselineStores.
const apiServiceName = "Search API"

// newznabError writes the error document Newznab clients parse. HTTP 200 on
// purpose — the protocol carries its errors in the body, and a bare 4xx
// turns "limit reached" into "indexer down" in every client's UI.
func newznabError(c *gin.Context, code int, description string) {
	c.Header("Content-Type", "application/xml; charset=utf-8")
	c.String(http.StatusOK, `<?xml version="1.0" encoding="UTF-8"?>`+"\n"+
		`<error code="%d" description="%s"/>`, code, description)
}

// apiGate authenticates and meters one API request. ok=false means the
// refusal is already written.
func (w *web) apiGate(c *gin.Context, rawKey string) bool {
	ctx := c.Request.Context()
	subject := ""
	staff := false
	if rawKey != "" {
		userID, found, err := w.apiKeys.Resolve(ctx, rawKey)
		if err == nil && found {
			subject = fmt.Sprintf("u:%d", userID)
			if u, err := w.store.ByID(ctx, userID); err == nil && u != nil {
				staff = u.Role >= core.RoleMod
			}
		} else if err == nil {
			// A WRONG key is refused even on a public site: the client
			// believes it authenticated, and letting it through as anonymous
			// would hide a broken configuration until the day the site goes
			// members-only. 100 is Newznab's "incorrect credentials".
			newznabError(c, 100, "Incorrect user credentials")
			return false
		}
		// A store error falls through to the anonymous path: refusing the
		// whole API because the key table hiccuped is the worse failure.
	}
	if subject == "" {
		if browsingMode() == BrowseMembers {
			newznabError(c, 100, "Incorrect user credentials")
			return false
		}
		subject = "ip:" + c.ClientIP()
	}

	n, err := w.data.IncrAPIRequest(ctx, subject)
	if err != nil {
		return true // count-keeping must never take the API down
	}
	limit := apiDailyLimit()
	if limit > 0 && n > int64(limit) && !staff {
		newznabError(c, 500, "Request limit reached")
		return false
	}
	return true
}

// apiDailyLimit reads the operator's rate_per_day (Jobs → Config → Search
// API). 0 disables the cap.
func apiDailyLimit() int {
	j := schedule.FindJob(apiServiceName)
	if j == nil {
		return 0
	}
	return j.GetConfigInt("rate_per_day")
}

// apiUsage builds the API-key page's usage panel: fourteen days, every day
// present, bar heights precomputed against the limit (or the busiest day
// when uncapped). ok=false only when the counters are unreadable — the
// page then simply shows the key, as it always did.
func (w *web) apiUsage(ctx context.Context, userID int64) (apikey.Usage, bool) {
	const days = 14
	rows, err := w.data.APIRequestDays(ctx, fmt.Sprintf("u:%d", userID), days)
	if err != nil {
		return apikey.Usage{}, false
	}
	byDay := map[string]int64{}
	for _, r := range rows {
		byDay[r.Day.Format("2006-01-02")] = r.Count
	}
	limit := apiDailyLimit()
	// The scale: the quota when there is one, else the busiest day — a
	// graph with no reference line still needs a top.
	scale := int64(limit)
	if scale <= 0 {
		for _, n := range byDay {
			if n > scale {
				scale = n
			}
		}
		if scale == 0 {
			scale = 1
		}
	}
	us := apikey.Usage{Limit: limit}
	now := time.Now()
	for i := days - 1; i >= 0; i-- {
		d := now.AddDate(0, 0, -i)
		n := byDay[d.Format("2006-01-02")]
		pct := int(n * 100 / scale)
		if pct > 100 {
			pct = 100
		}
		if n > 0 && pct == 0 {
			pct = 2 // a day with traffic must not look like a day without
		}
		us.Days = append(us.Days, apikey.UsageDay{
			Label: d.Format("2 Jan"),
			Count: n,
			Pct:   pct,
			Over:  limit > 0 && n > int64(limit),
		})
		if i == 0 {
			us.Today = n
		}
		if i < 7 {
			us.Week += n
		}
	}
	return us, true
}
