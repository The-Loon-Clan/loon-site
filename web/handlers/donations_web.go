package handlers

import (
	"html/template"

	"errors"
	"github.com/the-loon-clan/loon-site/internal/middleware"
	"github.com/the-loon-clan/loon-site/internal/storage"

	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/donations"
)

// Donations (loon-plugins/donations) host wiring — UNIT3D's donation area.
//
// DEV-ONLY, BEHIND A FLAG. This plugin takes real money through BTCPay, so it
// is gated on LOON_DONATIONS=1 and is OFF unless that is set. The gate is
// the ENV VAR, not the admin toggle: the plugin's own donate_enabled setting is
// still honoured, but ANDed with the flag, so nobody can turn payments on from
// the admin UI of a deployment that was never meant to take them.
//
// The webhook at POST /api/btcpay/webhook registers regardless — routes are
// bound at Provision and core has no per-plugin disable. That is acceptable
// because the plugin authenticates it by HMAC-SHA256 over the raw body against
// btcpay_webhook_secret ("the HMAC verification IS the auth", webhook.go). With
// no secret configured, no signature can validate, so the endpoint rejects
// everything it receives.

// donationsEnabled is the host-level master switch, read once at wiring time.
// A var rather than a const so the tests and the SetDonateEnabled seam below
// can reason about it in one place.
var donationsEnabled = os.Getenv("LOON_DONATIONS") == "1"

// donateToggle is the in-process half of the plugin's master toggle. The plugin
// wants a flip to take effect NOW without a restart, so the setting is mirrored
// here and read on every gate check.
var donateToggle atomic.Bool

// siteSettings is the demo's donations.Settings: a tiny key/value table. The
// plugin keeps donate_* config and BTCPay credentials here, which is why this
// is a real table and not an in-memory map — a restart must not silently
// discard a webhook secret and start accepting unverifiable callbacks.
type siteSettings struct{ db storage.Conn }

// GetSetting reads one key from the shared site_settings table.
//
// The donations plugin keeps its BTCPay credentials here rather than in
// memory: a restart must not silently discard a webhook secret and start
// accepting callbacks it can no longer verify.
func (s siteSettings) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.GetContext(ctx, &v, `SELECT value FROM site_settings WHERE key = $1`, key)
	// errors.Is, not ==. A wrapped sql.ErrNoRows fails the equality check and
	// falls through as a real error, so the plugin would see a lookup failure
	// where it should see "not set" — and it would only start happening the
	// day something in the driver or a helper began wrapping.
	if errors.Is(err, sql.ErrNoRows) {
		// Absent is not an error: the plugin reads keys that have never been
		// set and expects "" for them.
		return "", nil
	}
	return v, err
}

// SetSetting writes one key to the shared site_settings table.
func (s siteSettings) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO site_settings (key, value) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value)
	return err
}

// SettingsWithPrefix reads every key under a prefix, for a caller that keeps a
// SET of settings rather than a handful of named ones — the feature flags, one
// row per decision. Absent keys are simply absent; an empty map is a site that
// has decided nothing, not an error.
func (s siteSettings) SettingsWithPrefix(ctx context.Context, prefix string) (map[string]string, error) {
	var rows []struct {
		Key   string `db:"key"`
		Value string `db:"value"`
	}
	if err := s.db.SelectContext(ctx, &rows,
		`SELECT key, value FROM site_settings WHERE key LIKE $1`, prefix+"%"); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Key] = r.Value
	}
	return out, nil
}

// DeleteSetting forgets a key.
//
// Deleting is not the same as writing a falsy value, which is why this exists:
// for the feature flags, "off" and "whatever the plugin ships as its default"
// are different intentions, and only an absent row can express the second.
func (s siteSettings) DeleteSetting(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM site_settings WHERE key = $1`, key)
	return err
}

// wireDonationsPlugin installs the SetDeps seams. Always called — the plugin
// registers at init and Provision fails loudly without deps — but every gate
// below returns false unless LOON_DONATIONS=1.
func wireDonationsPlugin(c *core.Core, w *web) error {
	db := storage.Wrap(c.Storage.DB())
	if db.Valid() {
		if err := w.data.MigrateDonations(); err != nil {
			return fmt.Errorf("donations migrate: %w", err)
		}
		// Restore the persisted toggle so a restart does not silently change
		// whether the site is accepting donations.
		if donationsEnabled {
			if v, err := (siteSettings{db}).GetSetting(context.Background(), "donate_enabled"); err == nil {
				donateToggle.Store(v == "1" || strings.EqualFold(v, "true"))
			}
		}
	}

	donations.SetDeps(donations.Deps{
		// RenderPage, not BaseData. The plugin owns both pages and kept
		// BaseData alive only so this repo would keep building mid-migration.
		// site_fragment.html rather than site_page.html: help_donate.html
		// opens with its own hero and the panel wrapper would print the title
		// again above it — see site_fragment.html.
		RenderPage: func(gc *gin.Context, status int, title string, body template.HTML) {
			w.renderStatus(gc, status, "site_fragment.html",
				map[string]any{"Title": title, "Fragment": body})
		},
		// error.html stays host-owned — it is the site-wide error surface, and
		// the plugin only needs to reach it. Rendered by name through gin's
		// set, exactly as the plugin's legacy path did.
		RenderError: func(gc *gin.Context, code int, title, msg string) {
			gc.HTML(code, "error.html", w.chromeData(gc, gin.H{
				"Code": code, "Title": title, "Message": msg,
			}))
		},
		CSRFToken:    middleware.Token,
		RelativeTime: relativeTime,
		// What this deployment calls itself. Without it the page reads "this
		// site" everywhere — true, but this host has a name.
		SiteName: w.siteName,
		Settings: siteSettings{db},
		// The env flag is the OUTER gate. Even with donate_enabled persisted
		// true, a deployment without the flag reports disabled — which is what
		// makes this dev-only rather than merely default-off.
		IsDonateEnabled: func() bool { return donationsEnabled && donateToggle.Load() },
		SetDonateEnabled: func(ctx context.Context, enabled bool) error {
			if !donationsEnabled {
				// Refuse rather than silently no-op: an admin who clicks
				// "enable" and sees nothing change would reasonably assume the
				// feature is broken instead of deliberately gated.
				return fmt.Errorf("donations are disabled on this deployment " +
					"(set LOON_DONATIONS=1 to allow the toggle)")
			}
			donateToggle.Store(enabled)
			if !db.Valid() {
				return nil
			}
			v := "0"
			if enabled {
				v = "1"
			}
			return siteSettings{db}.SetSetting(ctx, "donate_enabled", v)
		},
		LookupUsername: func(ctx context.Context, userID int) (string, bool) {
			u, err := w.store.ByID(ctx, int64(userID))
			if err != nil || u == nil {
				return "", false
			}
			return u.ToCore().Username, true
		},
		LookupUserID: func(ctx context.Context, username string) (int, bool) {
			// Case-insensitive to match the plugin's documented behaviour on
			// its origin host, so a manual attribution typed as "Alice" finds
			// alice rather than silently failing.
			u, err := w.store.ByUsername(ctx, strings.ToLower(strings.TrimSpace(username)))
			if err != nil || u == nil {
				return 0, false
			}
			return int(u.ToCore().ID), true
		},
	})
	return nil
}
