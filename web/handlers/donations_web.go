package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/donations"
)

// Donations (loon-plugins/donations) host wiring — UNIT3D's donation area.
//
// DEV-ONLY, BEHIND A FLAG. This plugin takes real money through BTCPay, so it
// is gated on LOON_DEMO_DONATIONS=1 and is OFF unless that is set. The gate is
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
var donationsEnabled = os.Getenv("LOON_DEMO_DONATIONS") == "1"

// donateToggle is the in-process half of the plugin's master toggle. The plugin
// wants a flip to take effect NOW without a restart, so the setting is mirrored
// here and read on every gate check.
var donateToggle atomic.Bool

// siteSettings is the demo's donations.Settings: a tiny key/value table. The
// plugin keeps donate_* config and BTCPay credentials here, which is why this
// is a real table and not an in-memory map — a restart must not silently
// discard a webhook secret and start accepting unverifiable callbacks.
type siteSettings struct{ db *sqlx.DB }

func (s siteSettings) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.GetContext(ctx, &v, `SELECT value FROM site_settings WHERE key = $1`, key)
	if err == sql.ErrNoRows {
		// Absent is not an error: the plugin reads keys that have never been
		// set and expects "" for them.
		return "", nil
	}
	return v, err
}

func (s siteSettings) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO site_settings (key, value) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value)
	return err
}

// donationsMigrate creates the four tables the plugin queries. Columns come
// from its own INSERT/SELECT lists and model db tags.
//
// site_settings is shared, not donation-specific — it is a plain key/value
// store the plugin keeps donate_* config and BTCPay credentials in. A restart
// must not silently discard a webhook secret and start accepting callbacks it
// can no longer verify, which is why it is a table rather than a map.
func donationsMigrate(db *sqlx.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS site_settings (
		    key   TEXT PRIMARY KEY,
		    value TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS site_costs (
		    id         SERIAL PRIMARY KEY,
		    label      TEXT NOT NULL,
		    category   TEXT NOT NULL DEFAULT 'other',
		    goal_group TEXT NOT NULL DEFAULT 'site',
		    period     TEXT NOT NULL DEFAULT 'monthly',
		    amount_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
		    notes      TEXT NOT NULL DEFAULT '',
		    sort_order INTEGER NOT NULL DEFAULT 0,
		    active     BOOLEAN NOT NULL DEFAULT true,
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS donation_packages (
		    id           BIGSERIAL PRIMARY KEY,
		    label        TEXT NOT NULL,
		    amount_usd   DOUBLE PRECISION NOT NULL DEFAULT 0,
		    stock_total  INTEGER NOT NULL DEFAULT 0,
		    reward       TEXT NOT NULL DEFAULT '',
		    description  TEXT NOT NULL DEFAULT '',
		    reset_period TEXT NOT NULL DEFAULT '',
		    sort_order   INTEGER NOT NULL DEFAULT 0,
		    active       BOOLEAN NOT NULL DEFAULT true,
		    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
		    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		// donor_user_id and package_id are NULLABLE on purpose: a tip-jar
		// donation has no claimed slot, and an admin fiat entry may have no
		// account to attribute to. The model types them as pointers for the
		// same reason — do not add NOT NULL here.
		`CREATE TABLE IF NOT EXISTS donations (
		    id            BIGSERIAL PRIMARY KEY,
		    asset         TEXT NOT NULL DEFAULT '',
		    txid          TEXT NOT NULL DEFAULT '',
		    amount_native DOUBLE PRECISION NOT NULL DEFAULT 0,
		    amount_usd    DOUBLE PRECISION NOT NULL DEFAULT 0,
		    donor_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
		    donor_label   TEXT NOT NULL DEFAULT '',
		    received_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
		    note          TEXT NOT NULL DEFAULT '',
		    overfunded    BOOLEAN NOT NULL DEFAULT false,
		    package_id    BIGINT REFERENCES donation_packages(id) ON DELETE SET NULL
		)`,
		// The webhook dedupes settlements by transaction id, so this is a
		// correctness index, not just a speed one — without it a retried
		// callback can double-credit.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_donations_txid
		     ON donations (txid) WHERE txid <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_donations_received ON donations (received_at DESC)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// wireDonationsPlugin installs the SetDeps seams. Always called — the plugin
// registers at init and Provision fails loudly without deps — but every gate
// below returns false unless LOON_DEMO_DONATIONS=1.
func wireDonationsPlugin(c *core.Core, w *web) error {
	db := c.Storage.DB()
	if db != nil {
		if err := donationsMigrate(db); err != nil {
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
		BaseData: func(gc *gin.Context, extra gin.H) gin.H { return w.chromeData(gc, extra) },
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
					"(set LOON_DEMO_DONATIONS=1 to allow the toggle)")
			}
			donateToggle.Store(enabled)
			if db == nil {
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
