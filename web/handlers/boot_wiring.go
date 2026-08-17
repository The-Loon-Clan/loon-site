package handlers

// Boot phases: the stores and tables that must exist before anything is served.
//
// These were the first 120 lines of Main, which ran to 1,091 — nine times the
// next-longest function in the codebase. The production indexer keeps this
// kind of setup in cmd/*_wiring.go, one file per concern; the split is the
// same, and it lives here for the reason cmd/loonsite documents.
//
// Two phases rather than one, because they answer to different owners: the
// first is loon-baseline's stores and their migrations, the second is tables
// and settings THIS site owns. Mixing them is how it became unclear which
// migrations a host copying this file actually needs.

import (
	"context"
	"log/slog"

	"github.com/the-loon-clan/loon-baseline/apikey"
	"github.com/the-loon-clan/loon-baseline/authtoken"
	"github.com/the-loon-clan/loon-baseline/heartbeat"
	"github.com/the-loon-clan/loon-baseline/jobsettings"
	"github.com/the-loon-clan/loon-baseline/jobtrigger"
	"github.com/the-loon-clan/loon-baseline/loginlog"
	"github.com/the-loon-clan/loon-baseline/maintenance"
	"github.com/the-loon-clan/loon-baseline/users"
	"github.com/the-loon-clan/loon-site/internal/storage"

	"fmt"
	"github.com/the-loon-clan/loon/schedule"
)

// baselineStores is what the first phase hands to the rest of boot.
//
// Only the stores used LATER are fields. jobSettings, maintStore and apiSvc
// are set up here and never referenced again, so they stay locals — a struct
// field for them would imply a lifetime they do not have.
type baselineStores struct {
	sessionSecret []byte
	users         *users.PGStore
	loginLog      *loginlog.PGStore
	tokens        *authtoken.PGStore
	apiKeys       *apikey.PGStore
	maint         *maintenance.Controller
	jobTriggers   *jobtrigger.PGStore
	heartbeat     *heartbeat.PGStore
}

// wireBaselineStores migrates and returns loon-baseline's stores.
//
// Every failure here is fatal on purpose: a host that cannot create its own
// users table has nothing to serve, and continuing would surface as confusing
// per-request errors much later.
// wireBaselineStores builds the loon-baseline stores this host owns.
//
// It RETURNS its errors rather than exiting, and that is not a style
// preference: every step below is a migration or a store construction that a
// test wants to run, and a helper that calls os.Exit cannot be called from a
// test at all — the process dies and takes the run with it. Main does the
// exiting, which is the one place that should.
func wireBaselineStores(db storage.Conn, logger *slog.Logger) (baselineStores, error) {
	var st baselineStores
	st.sessionSecret = []byte(getenvDefault("LOON_SESSION_SECRET", "dev-insecure-demo-secret-change-me"))
	// User store: loon-baseline's Postgres reference impl (a real host implements
	// users.Store over its own table). Migrate the reference table + seed the two
	// demo accounts (password == username).
	st.users = users.NewPGStore(db.Raw().DB)
	// Host-owned reads for /staff and /stats — see w.data.DB() in pages_web.go.
	if err := st.users.Migrate(context.Background()); err != nil {
		return st, fmt.Errorf("users migrate: %w", err)
	}
	// Login-attempt audit (loon-baseline): the host records each attempt at its
	// login handler; the store + views live in the baseline.
	st.loginLog = loginlog.NewPGStore(db.Raw().DB)
	if err := st.loginLog.Migrate(context.Background()); err != nil {
		return st, fmt.Errorf("loginlog migrate: %w", err)
	}
	// Password-reset + email-verification token store (loon-baseline).
	st.tokens = authtoken.NewPGStore(db.Raw().DB)
	if err := st.tokens.Migrate(context.Background()); err != nil {
		return st, fmt.Errorf("authtoken migrate: %w", err)
	}
	// Admin-editable job/service settings (loon-baseline). This is the
	// persistence behind loon's schedule config vars. We register the "Search
	// API" read tier as a REMOTE service: its run loop lives in loon-api (a
	// separate process against this same DB), but declaring it here — with
	// MarkRemote — surfaces its cache-TTL settings on this web admin's config
	// page. Edit here; loon-api reads the same job_settings rows. That's the
	// cross-process settings path from LOON-DISTRIBUTED.
	jobSettings := jobsettings.NewPGStore(db.Raw().DB)
	if err := jobSettings.Migrate(context.Background()); err != nil {
		return st, fmt.Errorf("jobsettings migrate: %w", err)
	}
	// Newznab API keys (loon-baseline): one per user, shown + regenerated on the
	// self-service /p/api-key page. loon-api (against this same DB) validates the
	// ?apikey= a client sends against this table.
	st.apiKeys = apikey.NewPGStore(db.Raw().DB)
	if err := st.apiKeys.Migrate(context.Background()); err != nil {
		return st, fmt.Errorf("apikey migrate: %w", err)
	}
	// Planned-maintenance mode (loon-baseline): a persisted flag + a self-
	// contained 503 page + an admin toggle. Restore the last state on boot so a
	// restart mid-maintenance stays in maintenance. The API tier (loon-api)
	// deliberately does NOT install this middleware, so it stays up.
	maintStore := maintenance.NewPGStore(db.Raw().DB)
	if err := maintStore.Migrate(context.Background()); err != nil {
		return st, fmt.Errorf("maintenance migrate: %w", err)
	}
	st.maint = maintenance.NewController(maintStore)
	if err := st.maint.Restore(context.Background()); err != nil {
		logger.Error("maintenance restore", "err", err)
	}
	// Cross-process "run now" queue (loon-baseline). This demo is the WORKER
	// side: it drains the queue and runs the job locally. In a split deployment
	// the WEB process installs schedule.RemoteTrigger to enqueue instead —
	// never both in one process, or a triggerless job re-enqueues itself, so
	// the demo (single process) only polls.
	st.jobTriggers = jobtrigger.NewPGStore(db.Raw().DB)
	if err := st.jobTriggers.Migrate(context.Background()); err != nil {
		return st, fmt.Errorf("jobtrigger migrate: %w", err)
	}
	// Process presence (loon-baseline): this instance beats a heartbeat on an
	// interval; the admin "Services online" view lists who's checked in. One
	// "all" process here; a split deployment would show web + worker rows.
	st.heartbeat = heartbeat.NewPGStore(db.Raw().DB)
	if err := st.heartbeat.Migrate(context.Background()); err != nil {
		return st, fmt.Errorf("heartbeat migrate: %w", err)
	}

	apiSvc := schedule.RegisterService("Search API", "Newznab/Torznab read tier (runs in loon-api)")
	apiSvc.DeclareConfig(jobSettings,
		schedule.JobConfigVar{Key: "cache_ttl_secs", Label: "Search cache TTL (seconds)", Type: schedule.JobConfigInt, Default: "3600",
			Description: "How long search/tvsearch/movie/rss responses stay cached in the API tier. Safe to keep long: an ingest invalidates the namespace."},
		schedule.JobConfigVar{Key: "caps_ttl_secs", Label: "Caps cache TTL (seconds)", Type: schedule.JobConfigInt, Default: "3600",
			Description: "How long the caps (category tree) response stays cached — nearly static."},
		schedule.JobConfigVar{Key: "rate_per_min", Label: "Requests per minute", Type: schedule.JobConfigInt, Default: "60",
			Description: "Per-API-key (or IP) request cap per minute in the API tier — burst protection. 0 disables."},
		schedule.JobConfigVar{Key: "rate_per_day", Label: "Requests per day", Type: schedule.JobConfigInt, Default: "10000",
			Description: "Per-API-key (or IP) request cap per day in the API tier — the daily quota. 0 disables."},
		schedule.JobConfigVar{Key: "rate_contributor_mult", Label: "Contributor limit multiplier", Type: schedule.JobConfigInt, Default: "3",
			Description: "Contributors get this multiple of the base API limits; mods/admins are exempt entirely."},
	)
	apiSvc.MarkRemote() // its loop lives in loon-api; here it's a config stub
	return st, nil
}

// migrateSiteTables creates the tables and loads the settings this site owns,
// as opposed to loon-baseline's. Runs after the user seed, because the forum's
// starter threads attribute to the demo accounts.
// migrateSiteTables runs the host's own migrations and seeds. Returns its
// errors for the same reason as wireBaselineStores above.
func migrateSiteTables(data *storage.Store, logger *slog.Logger, users *users.PGStore) error {
	seedDemoUsers(users, logger)
	// Forum tables + starter content (forum_web.go). After the user seed so
	// the starter threads can attribute to the demo accounts.
	if err := data.MigrateForum(); err != nil {
		return fmt.Errorf("forum migrate: %w", err)
	}
	forumSeed(data.DB(), logger)
	// Site access modes + invite codes (access_web.go, invitecodes_web.go).
	if err := data.MigrateInviteCodes(); err != nil {
		return fmt.Errorf("invite codes migrate: %w", err)
	}
	// Before the two loads below: they read this table, and it used to be
	// created only when the donations plugin was enabled.
	if err := data.MigrateSiteSettings(); err != nil {
		return fmt.Errorf("site settings migrate: %w", err)
	}
	if err := loadAccessSettings(context.Background(), data.DB()); err != nil {
		logger.Error("load access settings", "err", err)
	}
	// The site flavour (flavour_web.go). Before the plugin config snapshot is
	// built: it decides which of the two content plugins boot.
	if err := loadSiteFlavour(context.Background(), data.DB()); err != nil {
		logger.Error("load site flavour", "err", err)
	}
	// Where cover art comes from — see covermode_web.go. Loaded before the
	// scraper can run, so the first matched cover already obeys the setting.
	if err := loadCoverMode(context.Background(), data.DB()); err != nil {
		logger.Error("load cover mode", "err", err)
	}
	logger.Info("cover art", "mode", coverMode(), "meaning", coverModeLabel(coverMode()))
	// The profile's free-text block (profilebio_web.go). A users column, so it
	// migrates with the other host-owned users work rather than in a plugin.
	if err := data.MigrateProfileBio(); err != nil {
		return fmt.Errorf("profile bio migrate: %w", err)
	}
	// The message catalogue (i18nadmin_web.go / storage/i18n.go).
	if err := data.MigrateI18n(); err != nil {
		return fmt.Errorf("i18n migrate: %w", err)
	}
	// Editable site pages (pagesadmin_web.go / storage/sitepages.go).
	if err := data.MigrateSitePages(); err != nil {
		return fmt.Errorf("site pages migrate: %w", err)
	}
	return nil
}
