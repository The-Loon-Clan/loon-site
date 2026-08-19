package handlers

// Routes that need a role, and the admin surface behind them.
//
// The largest remaining phase of boot and the last one that lifts cleanly: it
// declares four locals and lets none of them escape, so everything it builds
// is consumed by the route registrations at its end.
//
// It reads as a single subject too — this is where the site decides who may
// reach what. Moderation is RoleUser to enter and RoleAdmin or RoleMod per
// action; the admin group is admin-only; and wireViews mounts every plugin
// slot generically underneath. Spread through a boot function it was a run of
// route calls among unrelated setup; on its own it is the access-control map.

import (
	"context"
	"log/slog"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"

	"github.com/the-loon-clan/loon-baseline/account"
	"github.com/the-loon-clan/loon-baseline/adminusers"
	"github.com/the-loon-clan/loon-baseline/apikey"
	"github.com/the-loon-clan/loon-baseline/heartbeat"
	"github.com/the-loon-clan/loon-baseline/loginlog"
	"github.com/the-loon-clan/loon-baseline/notify"
	"github.com/the-loon-clan/loon-baseline/password"
	"github.com/the-loon-clan/loon-baseline/profile"
	"github.com/the-loon-clan/loon-plugins/achievements"
	"github.com/the-loon-clan/loon-plugins/dailyreward"
	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon/schedule"

	"github.com/the-loon-clan/loon-site/internal/storage"
)

// wireAdminAndViews mounts the moderation and admin groups and every plugin
// view slot.
func wireAdminAndViews(
	c *core.Core,
	wsrv *web,
	engine *gin.Engine,
	data *storage.Store,
	db storage.Conn,
	st baselineStores,
	rt *core.Runtime,
	inbox notify.InboxStore,
	ctx context.Context,
	stop context.CancelFunc,
	logger *slog.Logger,
	// The shared client, or nil when REDIS_ADDR is unset — the admin status
	// page reports whether it is in play.
	redisClient *goredis.Client,
) {
	// Moderation is not administration — different gate, different nav — and
	// keeping it here made the whole surface unreachable from any test. See
	// moderation_wiring.go.
	mountModeration(engine, wsrv)

	admin := engine.Group("/admin", wsrv.auth.Require(core.RoleAdmin)...)
	// Access modes + the page map (accessadmin_web.go).
	admin.GET("/contracts", wsrv.adminContracts)
	admin.GET("/access", wsrv.adminAccess)
	admin.POST("/access", wsrv.adminAccessSave)
	// The message catalogue behind the achievements localization dropdowns
	// (i18nadmin_web.go).
	admin.GET("/i18n", wsrv.adminI18n)
	admin.POST("/i18n", wsrv.adminI18nSave)
	// The site's prose pages (pagesadmin_web.go).
	admin.GET("/pages", wsrv.adminPages)
	admin.POST("/pages", wsrv.adminPagesSave)
	admin.POST("/pages/delete", wsrv.adminPagesDelete)
	// The menu editor (navadmin_web.go).
	admin.GET("/nav", wsrv.adminNavEditor)
	admin.POST("/nav", wsrv.adminNavSave)
	// Where cover art comes from (coversadmin_web.go + covermode_web.go).
	admin.GET("/covers", wsrv.adminCovers)
	admin.POST("/covers", wsrv.adminCoversSave)
	// Who vouched for whom (invitesadmin_web.go). A READ of columns invite
	// codes have always carried — no table, no migration.
	admin.GET("/invites", wsrv.adminInvites)
	// The page editor: where an operator puts registered widgets
	// (widgetsadmin_web.go). One region at a time, chosen by ?region=.
	mountWidgetsAdmin(admin, wsrv)
	admin.GET("/plugins", wsrv.adminPlugins)
	admin.GET("/jobs", wsrv.adminJobs)
	admin.POST("/jobs/control", wsrv.adminJobsControl)
	// Per-job/-service settings — loon's bundled config form (self-contained
	// page). The demo's jobs table links here via a Config button for any job
	// that declares settings (HasConfig).
	admin.GET("/jobs/config", schedule.JobConfigHandler(nil))
	admin.POST("/jobs/config", schedule.JobConfigSaveHandler(nil))

	// Wire the usenet plugin's READ capability into the public pages — the
	// plugin publishes it on the extension registry during Provision.
	if v, ok := c.Lookup(pluginapi.UsenetIndexName); ok {
		wsrv.usenet, _ = v.(pluginapi.UsenetIndex)
	}
	// Shows rather than releases (pluginapi.SeriesIndex): the read behind
	// /series. Absent on a host whose indexer does not offer it, which simply
	// has no series pages — the nav entry is gated on it.
	if v, ok := c.Lookup(pluginapi.SeriesIndexName); ok {
		wsrv.series, _ = v.(pluginapi.SeriesIndex)
	}
	if v, ok := c.Lookup(pluginapi.UsenetNewznabName); ok {
		wsrv.usenetAPI, _ = v.(pluginapi.UsenetNewznab)
	}
	// Which releases the tracker ALSO carries (pluginapi.TorrentMirrors), so a
	// listing row can offer the NZB and the torrent together. Absent on a pure
	// indexer and on an idle tracker; mirrors_web.go then falls back to this
	// site's own read of the tracker schema, and to nothing beyond that.
	if v, ok := c.Lookup(pluginapi.TorrentMirrorsName); ok {
		wsrv.mirrors, _ = v.(pluginapi.TorrentMirrors)
	}
	// And the write side: turning a release into a torrent on demand, which is
	// how this site mirrors without pre-hashing 160,000 releases it has no
	// bytes for. Absent = the release page offers no button.
	if v, ok := c.Lookup(pluginapi.TorrentMirrorMakerName); ok {
		wsrv.mirrorMaker, _ = v.(pluginapi.TorrentMirrorMaker)
	}
	// Daily reward: the plugin owns the once-per-day rule and the streak; the
	// host only asks whether a claim is available, so the stat strip can offer
	// a compact button and hide it once taken. Absent extension = no button,
	// which is what a host without the plugin should get.
	if v, ok := c.Lookup(dailyreward.StatusExtension); ok {
		wsrv.dailyStatus, _ = v.(dailyreward.StatusFunc)
	}

	// Catalog plugin: its service also implements the sink + cover store the
	// scraper writes to and the release page reads.
	if v, ok := c.Lookup(pluginapi.CatalogName); ok {
		if cat, ok := v.(pluginapi.Catalog); ok {
			wsrv.catalog = cat // taxonomy + names for the /browse page
			wsrv.catalogSink, _ = cat.(pluginapi.CatalogSink)
			wsrv.catalogCovers, _ = cat.(pluginapi.CatalogCovers)
		}
	}
	// Rewards: the plugin owns what is earnable and who has earned it; the
	// host owns the page that shows it. Absent extension = the page says
	// so, rather than 404ing a link the account nav always renders.
	if v, ok := c.Lookup(achievements.ListExtension); ok {
		wsrv.achievements, _ = v.(achievements.ListFunc)
		// Seed the catalogue here rather than beside the other seeds up top:
		// rewards.achievements is the PLUGIN's table, created by its own
		// migration during Boot, so nothing could be inserted into it before
		// this point. Guarded on the table being empty, so it runs once.
		achievementsSeed(db, wsrv, logger)
	}

	// The rest of the demo seed (demoseed_web.go). Here for the same reason
	// achievementsSeed is: ranks.groups and store.items are PLUGIN tables,
	// created by their own migrations during Boot, so nothing could be
	// inserted into them any earlier. Each seeder checks its own table is
	// empty, so this runs once and never fights an operator's own data.
	demoSeed(db, logger)

	// Calendar sources. Registered AFTER the capability lookups above because
	// each source closes over one of them; a source whose dependency is absent
	// contributes nothing and the grid simply has fewer chips on it, which is
	// the whole reason the page reads a slice instead of naming its sources.
	wsrv.calSources = []calSource{
		wsrv.calAttendance(),
		wsrv.calBookmarks(),
	}

	// Newznab / Torznab API (Sonarr/Radarr/Prowlarr consume these).
	engine.GET("/api", wsrv.newznabAPI)
	engine.GET("/rss", wsrv.newznabAPI)

	// sitemap.xml, from loon-baseline/sitemap. Wired AFTER the usenet lookup
	// above: its releases Source reads through that capability, and a demo
	// without the plugin configured simply publishes a static-only sitemap.
	// See sitemap_web.go — the host supplies the Sources, the schedule and the
	// routes; the package does the XML, the paging and the index.
	wsrv.wireSitemap(engine, getenvDefault("LOON_BASE_URL", "http://localhost:8090"))

	// loon-baseline's batteries-included admin views (user management) plug
	// into the SAME view system the plugins use — the host just registers
	// them on the Core after Boot and wireViews mounts them at /admin/p/users.
	// This is the reusable admin chrome a real host adopts instead of
	// hand-rolling a users page.
	if bviews, err := adminusers.Views(st.users, password.Hasher{}); err != nil {
		logger.Error("adminusers.Views", "err", err)
	} else {
		for _, v := range bviews {
			if err := c.RegisterView(v); err != nil {
				logger.Error("register admin view", "slug", v.Slug, "err", err)
			}
		}
	}
	// loon-baseline self-service account page (profile + change password) —
	// same view-system path, mounted at /p/account for any logged-in user.
	// Closes the loop on authflow.ChangePassword (logic existed; this is its UI).
	if aviews, err := account.Views(wsrv.flow, wsrv.currentUser); err != nil {
		logger.Error("account.Views", "err", err)
	} else {
		for _, v := range aviews {
			if err := c.RegisterView(v); err != nil {
				logger.Error("register account view", "slug", v.Slug, "err", err)
			}
		}
	}
	// loon-baseline "Services online" view: /admin/p/services lists process
	// instances that have beaten a heartbeat recently (kind, uptime, last seen).
	if hviews, err := heartbeat.Views(st.heartbeat); err != nil {
		logger.Error("heartbeat.Views", "err", err)
	} else {
		for _, v := range hviews {
			if err := c.RegisterView(v); err != nil {
				logger.Error("register services view", "slug", v.Slug, "err", err)
			}
		}
	}
	// loon-baseline maintenance toggle: /admin/p/maintenance (begin/end). Turning
	// it on shows the 503 page to visitors; /admin + /api stay reachable.
	if mviews, err := st.maint.Views(); err != nil {
		logger.Error("maintenance.Views", "err", err)
	} else {
		for _, v := range mviews {
			if err := c.RegisterView(v); err != nil {
				logger.Error("register maintenance view", "slug", v.Slug, "err", err)
			}
		}
	}
	// loon-baseline self-service API key page: /p/api-key shows the user's
	// Newznab key (created on first visit) + a Regenerate button. loon-api
	// validates the key against the same table.
	if kviews, err := apikey.Views(st.apiKeys, wsrv.currentUser, wsrv.apiUsage); err != nil {
		logger.Error("apikey.Views", "err", err)
	} else {
		for _, v := range kviews {
			if err := c.RegisterView(v); err != nil {
				logger.Error("register apikey view", "slug", v.Slug, "err", err)
			}
		}
	}
	// loon-baseline login audit views. Two are offered: /admin/p/login-log
	// (every attempt, with a user column) and /p/sign-ins (your own).
	//
	// Only the ADMIN one is registered. The member page is the same table minus
	// the user column, and its one distinguishing column is "IP fingerprint" —
	// a hash, because the raw address is deliberately never stored. A row
	// reading "fc73e44eeb75…" cannot tell a member whether a sign-in was
	// theirs, which is the only question the page exists to answer; it closed
	// by asking them to change their password if they saw one they did not
	// recognise, which they had no way to determine.
	//
	// Staff keep the whole log, where the hash does earn its place: it groups
	// attempts by origin without the site holding anyone's address.
	if lviews, err := loginlog.Views(st.loginLog, wsrv.currentUser); err != nil {
		logger.Error("loginlog.Views", "err", err)
	} else {
		for _, v := range lviews {
			if v.Slot == core.SlotSitePage {
				continue // the member-facing "Sign-ins" page — see above
			}
			if err := c.RegisterView(v); err != nil {
				logger.Error("register loginlog view", "slug", v.Slug, "err", err)
			}
		}
	}
	// loon-baseline profile summary (SlotUserWidget on /u/<name>). Resolves the
	// profile subject by id off the user store.
	if pviews, err := profile.Views(func(ctx context.Context, id int64) (*core.User, bool) {
		u, err := st.users.ByID(ctx, id)
		if err != nil {
			return nil, false
		}
		return u.ToCore(), true
	}); err != nil {
		logger.Error("profile.Views", "err", err)
	} else {
		for _, v := range pviews {
			if err := c.RegisterView(v); err != nil {
				logger.Error("register profile view", "slug", v.Slug, "err", err)
			}
		}
	}
	// Notification inbox page (/p/inbox). The navbar bell reads UnreadCount.
	if nviews, err := notify.InboxViews(inbox, wsrv.currentUser); err != nil {
		logger.Error("notify.InboxViews", "err", err)
	} else {
		for _, v := range nviews {
			if err := c.RegisterView(v); err != nil {
				logger.Error("register inbox view", "slug", v.Slug, "err", err)
			}
		}
	}

	// Plugin views (loon's view system): plugins render their settings
	// sections, admin/status pages, public pages, and widgets as fragments;
	// the demo mounts every slot generically and wraps the fragments in its
	// layout. Zero plugin-specific UI code host-side.
	wsrv.wireViews(c, engine, admin)

	serve(engine, wsrv, rt, ctx, stop, logger, redisClient)
}
