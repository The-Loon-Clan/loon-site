package handlers

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/hitrun"
	"github.com/the-loon-clan/loon-plugins/perks"
	"github.com/the-loon-clan/loon-plugins/seedlock"
	"github.com/the-loon-clan/loon/core"
)

// Host side of the hit-and-run framework.
//
// The plugin decides WHO has hit and run; this decides what the site does about
// it. That split is deliberate: the punishment is a policy a site owns, and
// baking one into a framework would make the framework wrong for the next site.

// hitRunBlockPrefix is the tracker path a blocked member loses.
//
// DOWNLOADS only. Announce and scrape stay open, and that is the whole point:
// a member with warnings needs to keep seeding to clear them, and cutting their
// announces would make the punishment unserveable — they could not fix the
// thing they are being punished for. UNIT3D draws the same line with
// can_download.
const hitRunBlockPrefix = "/tracker/download"

// wireHitRunPlugin installs the notification seams.
//
// Everything here is a message to a member. The plugin never disables anything
// itself — see enforceHitRunBlock below for the half that does.
func wireHitRunPlugin(c *core.Core, w *web, logger *slog.Logger) {
	hitrun.SetDeps(hitrun.Deps{
		// The member page at /hitrun. Without this seam the plugin declines to
		// mount it, which is right: a member told their downloads are disabled,
		// with nowhere to see why, is the worst version of this feature.
		RenderPage: func(gc *gin.Context, title string, body template.HTML) {
			w.render(gc, "site_page.html", map[string]any{"Title": title, "Fragment": body})
		},
		// Adapted, like the tracker's: the host helper takes `any` because
		// templates call it, and the plugin asks for a time.Time.
		RelativeTime: func(t time.Time) string { return relativeTime(t) },
		// A freeleech token excuses the snatch it was spent on. Resolved
		// LAZILY off the registry rather than captured here, because perks
		// provisions after this runs — and a host with no perks plugin is a
		// legitimate host, so its absence exempts nobody rather than failing.
		Exempt: func(_ context.Context, userID int64, infoHash string) bool {
			fl := freeleechLookup(c)
			return fl != nil && fl.HasFreeleech(userID, infoHash)
		},
		// The courtesy notice, and the only message that can still change the
		// outcome — so it says what to do, not just what happened.
		Prewarn: func(ctx context.Context, userID int64, torrent, reason string) {
			notifyHitRun(ctx, c, logger, userID, "Seeding reminder",
				fmt.Sprintf("%s: %s. Reseed it to avoid a warning.", torrent, reason))
		},
		Warn: func(ctx context.Context, userID int64, torrent, reason string) {
			notifyHitRun(ctx, c, logger, userID, "Hit and run warning",
				fmt.Sprintf("%s: %s.", torrent, reason))
		},
		// The limit. Says what was lost, and — importantly — how to get it
		// back, because a warning that expires is not obvious from a message
		// that only announces a punishment.
		LimitReached: func(ctx context.Context, userID int64, active int) {
			notifyHitRun(ctx, c, logger, userID, "Downloads disabled",
				fmt.Sprintf("You have %d active hit-and-run warnings, so new downloads are "+
					"disabled. Seeding still works — warnings expire on their own, and "+
					"clearing them restores downloading.", active))
			logger.Warn("hitrun: downloads disabled", "user", userID, "warnings", active)
		},
	})
}

func notifyHitRun(ctx context.Context, c *core.Core, logger *slog.Logger, userID int64, title, body string) {
	if c.Notifications == nil {
		return
	}
	if err := c.Notifications.Notify(ctx, userID, core.Notification{
		Kind: "hitrun", Title: title, Body: body,
	}); err != nil {
		logger.Warn("hitrun notify", "user", userID, "err", err)
	}
}

// enforceHitRunBlock refuses new .torrent downloads to a member at the warning
// limit.
//
// Gin middleware on the HOST rather than a check inside the tracker, for two
// reasons. The tracker belongs to another plugin and knows nothing about
// hit-and-run — teaching it would put the rule in one repository and its
// enforcement in another. And the entitlement route does not work: tracker
// access is granted by ROLE BASELINE, which core evaluates at resolution and
// never writes to the store, so it cannot be revoked for one member.
//
// The count is read straight from hitrun.warnings, the same way the rest of
// this host reads catalog and usenet tables. One indexed count, on a path a
// member hits when they click download — not on every request.
func enforceHitRunBlock(w *web) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Cheapest possible check first: almost every request is not this path.
		if !strings.HasPrefix(c.Request.URL.Path, hitRunBlockPrefix) {
			c.Next()
			return
		}
		u, ok := w.currentUser(c)
		if !ok || u == nil || usersDB == nil {
			c.Next()
			return
		}
		var active int
		// Expiry is applied in the QUERY as well as by the sweep. The sweep
		// clears expired rows hourly, so between runs a member could otherwise
		// still be blocked by a warning that lapsed twenty minutes ago.
		err := usersDB.QueryRowContext(c.Request.Context(),
			`SELECT count(*) FROM hitrun.warnings
			  WHERE user_id = $1 AND cleared_at IS NULL AND expires_at > now()`, u.ID).Scan(&active)
		if err != nil {
			// Fail OPEN. A broken query is the site's fault, and refusing a
			// member their downloads over it punishes them for it. The opposite
			// choice — deny on error — turns one bad deploy into every member
			// being blocked at once.
			c.Next()
			return
		}
		if !hitrun.DownloadsBlocked(hitRunPolicy(), active) {
			c.Next()
			return
		}
		c.String(http.StatusForbidden,
			"Downloads are disabled: you have %d active hit-and-run warnings.\n\n"+
				"Seeding still works, and warnings expire on their own. "+
				"See /tracker/my for what you owe.", active)
		c.Abort()
	}
}

// hitRunSettings is the ONE policy both sides read.
//
// main.go builds it, hands it to the plugin through the config map, and keeps
// this copy for the middleware. A second set of numbers here — even the same
// defaults — is how the page that blocks a member ends up disagreeing with the
// job that warned them about which limit applies.
var hitRunSettings = hitrun.DefaultPolicy()

// hitRunPolicy is the live rule set.
func hitRunPolicy() hitrun.Policy { return hitRunSettings }

// hitRunEnabled reports whether the operator asked for the rules.
//
// An env flag for the same reason the tracker has one: a system that disables
// a member's downloads is not something a host should acquire by checking the
// repository out.
func hitRunEnabled() bool {
	v := os.Getenv("LOON_DEMO_HITRUN")
	return v == "1" || v == "true" || v == "yes"
}

// hitRunConfig is the plugins.hitrun.* section, built from the same struct the
// middleware reads.
func hitRunConfig() map[string]any {
	hitRunSettings.Enabled = hitRunEnabled()
	return map[string]any{
		"enabled":          hitRunSettings.Enabled,
		"seedtime_seconds": hitRunSettings.Seedtime,
		"prewarn_days":     hitRunSettings.PrewarnDays,
		"grace_days":       hitRunSettings.GraceDays,
		"max_warnings":     hitRunSettings.MaxWarnings,
		"expire_days":      hitRunSettings.ExpireDays,
		"buffer_percent":   hitRunSettings.BufferPercent,
		"ratio_satisfies":  hitRunSettings.RatioSatisfies,
	}
}

// freeleechCap is the structural view of the perks plugin's freeleech answer —
// stdlib types only, so the host need not care which plugin provides it.
type freeleechCap interface {
	HasFreeleech(userID int64, infoHash string) bool
}

// freeleechLookup resolves the capability off the extension registry.
//
// Looked up per call rather than cached, because the hit-and-run sweep runs
// hourly and the lookup is a map read — while caching it would capture a nil
// on a boot where perks provisioned later, and then exempt nobody for the life
// of the process.
func freeleechLookup(c *core.Core) freeleechCap {
	v, ok := c.Lookup(perksExtension)
	if !ok {
		return nil
	}
	cap, ok := v.(freeleechCap)
	if !ok {
		return nil
	}
	return cap
}

// perksExtension is the registry key the perks plugin publishes itself under.
const perksExtension = "perks"

// wirePerksPlugin installs the wallet page's seams. Without them the plugin
// still sells and applies tokens but mounts no page, which would leave a member
// holding something they cannot spend.
func wirePerksPlugin(w *web) {
	perks.SetDeps(perks.Deps{
		RenderPage: func(gc *gin.Context, title string, body template.HTML) {
			w.render(gc, "site_page.html", map[string]any{"Title": title, "Fragment": body})
		},
		CSRFToken: csrfToken,
	})
}

// seedLockEnabled reports whether the operator asked for the one-host rule.
//
// Its own flag rather than riding on the tracker's: a site may well want a
// tracker without telling members which machine they may seed from, and the
// failure mode here — somebody locked out of their own torrent — deserves to be
// switched on deliberately.
func seedLockEnabled() bool {
	v := os.Getenv("LOON_DEMO_SEEDLOCK")
	return v == "1" || v == "true" || v == "yes"
}

// wireSeedLockPlugin installs the claims page's seams.
//
// Not optional in practice: the refusal a torrent client shows tells members to
// "clear the lock on the site", so a host that arms the rule without this sends
// them looking for a page that does not exist.
func wireSeedLockPlugin(w *web) {
	seedlock.SetDeps(seedlock.Deps{
		RenderPage: func(gc *gin.Context, title string, body template.HTML) {
			w.render(gc, "site_page.html", map[string]any{"Title": title, "Fragment": body})
		},
		CSRFToken: csrfToken,
	})
}
