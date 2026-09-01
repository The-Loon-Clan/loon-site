package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-site/internal/storage"
)

// Neutral leech, host side.
//
// The plugin contract is pluginapi.PolicySource: a RESTRICTION, resolved by
// ANY (one source asserting it is enough, nothing can out-bid it) and applied
// by the tracker's Credit after the promotions have been settled, zeroing both
// halves. This file is the host's source for that flag and the admin surface
// that sets it.
//
// WHY IT IS NOT A MULTIPLIER, recorded because it is the thing that is easy to
// get wrong twice: promotions combine by "the best offer wins", starting from
// a 1.0 floor. A source asking for upload × 0 therefore always loses, and
// neutral silently resolved to (1, 0) -- ordinary freeleech, i.e. free
// downloads AND full upload credit. It did not fail; it quietly paid out more
// than intended.

// neutralSettingKey holds the site-wide window's end, as a Unix timestamp.
// Absent or past means no window.
const neutralSettingKey = "neutral_until"

// neutralState is the announce-path mirror.
//
// Flag() runs per peer per announce -- every few minutes, for every peer on
// the site -- and the plugin contract asks for "one cheap read". A database
// query per peer per announce is not that, so both halves are mirrored in
// memory and refreshed on write, the same shape the nav chrome uses.
type neutralState struct {
	Hashes map[string]bool
	// Until is when the site-wide window ends. Zero means no window.
	Until time.Time
}

var (
	neutralMirror atomic.Value // neutralState
	// neutralStore is the site_settings accessor, set at boot beside the other
	// package-level setting stores (access_web.go, covermode_web.go).
	neutralStore siteSettings
)

func loadNeutralState() neutralState {
	s, _ := neutralMirror.Load().(neutralState)
	if s.Hashes == nil {
		s.Hashes = map[string]bool{}
	}
	return s
}

// hostPolicySource answers pluginapi's restriction questions for this host.
type hostPolicySource struct{}

// Flag reports whether this announce is neutral.
//
// Deliberately reads only the mirror: no context use, no database, no lock
// beyond the atomic load. An error is impossible here by construction, which
// matters because the contract treats an error as "no opinion" and fails
// GENEROUS -- a restriction that cannot answer is not applied. That is the
// right direction (nobody is silently denied credit they earned), but it means
// a source that could fail would silently stop restricting, so this one cannot
// fail.
func (hostPolicySource) Flag(_ context.Context, flag string, mc pluginapi.MultiplierContext) (bool, bool, error) {
	if flag != pluginapi.FlagNeutral {
		return false, false, nil // no opinion on flags this host does not implement
	}
	s := loadNeutralState()
	if !s.Until.IsZero() && time.Now().Before(s.Until) {
		return true, true, nil // site-wide window
	}
	if mc.InfoHash != "" && s.Hashes[strings.ToLower(mc.InfoHash)] {
		return true, true, nil
	}
	// A definite NO, not "no opinion": this host has looked and the answer is
	// that the announce is not neutral.
	return false, true, nil
}

// refreshNeutralMirror re-reads both halves into the mirror.
func (w *web) refreshNeutralMirror(ctx context.Context) error {
	hashes, err := w.data.ActiveNeutralHashes(ctx)
	if err != nil {
		return err
	}
	lower := make(map[string]bool, len(hashes))
	for h := range hashes {
		lower[strings.ToLower(h)] = true
	}
	var until time.Time
	if v, err := neutralStore.GetSetting(ctx, neutralSettingKey); err == nil && v != "" {
		if secs, err := strconv.ParseInt(v, 10, 64); err == nil && secs > 0 {
			until = time.Unix(secs, 0)
		}
	}
	neutralMirror.Store(neutralState{Hashes: lower, Until: until})
	return nil
}

// neutralActive reports whether anything is neutral right now, for the admin
// page's summary line.
func neutralActive() (siteWide bool, until time.Time, torrents int) {
	s := loadNeutralState()
	if !s.Until.IsZero() && time.Now().Before(s.Until) {
		return true, s.Until, len(s.Hashes)
	}
	return false, time.Time{}, len(s.Hashes)
}

// ── admin surface ───────────────────────────────────────────────────

// wireNeutral creates the table, primes the mirror and registers this host as
// a policy source.
//
// Registered UNCONDITIONALLY, not behind flavourTracker(): the flag is only
// ever consulted by the tracker's announce path, so on an indexer-flavoured
// site nothing asks and the source costs nothing. Gating registration instead
// would mean an operator who switches the flavour on has a neutral list that
// silently does not apply until the next restart.
func (w *web) wireNeutral(c *core.Core, db storage.Conn, logger *slog.Logger) {
	neutralStore = siteSettings{db: db}
	if err := w.data.MigrateNeutral(); err != nil {
		logger.Error("neutral migrate", "err", err)
		return
	}
	if err := w.refreshNeutralMirror(context.Background()); err != nil {
		logger.Error("neutral mirror", "err", err)
	}
	if err := c.Register(pluginapi.PolicyFlagPrefix+"host", hostPolicySource{}); err != nil {
		logger.Error("register neutral policy source", "err", err)
	}
}

// adminNeutral draws the page.
func (w *web) adminNeutral(c *gin.Context) {
	rows, err := w.data.NeutralTorrents(c.Request.Context())
	if err != nil {
		w.log.Error("neutral list", "err", err)
	}
	siteWide, until, count := neutralActive()

	type neutralRow struct {
		storage.NeutralTorrent
		Expired bool
		Expires string
	}
	view := make([]neutralRow, 0, len(rows))
	now := time.Now()
	for _, r := range rows {
		vr := neutralRow{NeutralTorrent: r, Expires: "never"}
		if r.ExpiresAt != nil {
			vr.Expires = r.ExpiresAt.Format("Jan 02, 15:04")
			vr.Expired = !r.ExpiresAt.After(now)
		}
		view = append(view, vr)
	}

	data := map[string]any{
		"Title":    "Neutral leech",
		"Rows":     view,
		"SiteWide": siteWide,
		"Active":   count,
		// Only set when a window is running, so the template does not have to
		// know that a zero time means "no window".
		"Until": "",
		// Whether the flag can currently do anything at all. An operator
		// marking torrents neutral on an indexer-flavoured site is configuring
		// something nothing will ever ask about, and should be told.
		"TrackerOn": flavourTracker(),
	}
	if siteWide {
		data["Until"] = until.Format("Jan 02, 15:04")
	}
	w.render(c, "admin_neutral.html", data)
}

// adminNeutralSet marks one torrent, or opens/closes the site-wide window.
func (w *web) adminNeutralSet(c *gin.Context) {
	ctx := c.Request.Context()
	who := ""
	if u, ok := w.currentUser(c); ok && u != nil {
		who = u.Username
	}

	switch c.PostForm("action") {
	case "site":
		// Hours, not a timestamp: an operator opening a neutral window is
		// thinking "for the next six hours", and a form that asks for a date
		// invites a typo that leaves the whole site neutral for a year.
		hours, _ := strconv.Atoi(strings.TrimSpace(c.PostForm("hours")))
		v := ""
		if hours > 0 {
			if hours > neutralMaxWindowHours {
				hours = neutralMaxWindowHours
			}
			v = strconv.FormatInt(time.Now().Add(time.Duration(hours)*time.Hour).Unix(), 10)
		}
		if err := neutralStore.SetSetting(ctx, neutralSettingKey, v); err != nil {
			w.log.Error("neutral window", "err", err)
		}
	case "clear":
		if h := neutralHash(c.PostForm("info_hash")); h != "" {
			if err := w.data.ClearNeutralTorrent(ctx, h); err != nil {
				w.log.Error("neutral clear", "hash", h, "err", err)
			}
		}
	default:
		h := neutralHash(c.PostForm("info_hash"))
		if h == "" {
			c.Redirect(http.StatusSeeOther, "/admin/neutral?error=1")
			return
		}
		var expires *time.Time
		if hours, _ := strconv.Atoi(strings.TrimSpace(c.PostForm("hours"))); hours > 0 {
			t := time.Now().Add(time.Duration(hours) * time.Hour)
			expires = &t
		}
		if err := w.data.SetNeutralTorrent(ctx, h, strings.TrimSpace(c.PostForm("reason")), who, expires); err != nil {
			w.log.Error("neutral set", "hash", h, "err", err)
		}
	}

	// The mirror is what the announce path reads, so a write that did not
	// refresh it would leave the change invisible until the next boot.
	if err := w.refreshNeutralMirror(ctx); err != nil {
		w.log.Error("neutral mirror", "err", err)
	}
	c.Redirect(http.StatusSeeOther, "/admin/neutral")
}

// neutralMaxWindowHours bounds a site-wide window. A typo in the hours box
// should cost an afternoon, not a year of uncounted traffic.
const neutralMaxWindowHours = 24 * 14

// neutralHash normalises and validates an info hash. Anything that is not 40
// hex characters is rejected rather than stored: a mistyped hash would sit in
// the list looking exactly like a working one and never match an announce.
func neutralHash(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != 40 {
		return ""
	}
	if strings.Trim(s, "0123456789abcdef") != "" {
		return ""
	}
	return s
}
