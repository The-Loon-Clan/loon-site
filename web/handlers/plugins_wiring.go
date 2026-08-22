package handlers

// Plugin seams: the host capabilities each gin-template plugin renders through.
//
// This is the part of boot that shows what "host" means in this framework —
// every plugin here is inert until the site hands it a way to render a page,
// read a user, or write a file. It is also order-dependent in ways a reader
// should not have to infer from a thousand-line function: communities is
// wired last of the template plugins because its joins need columns the
// messages and points work adds.
//
// Nothing declared here escapes the phase — the block binds no locals at all,
// which is what a pure wiring step looks like.

import (
	"log/slog"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/the-loon-clan/loon/core"
)

// wirePluginSeams installs the host side of every gin-template plugin.
//
// Fatal on failure throughout: a plugin whose seams are missing does not
// degrade, it panics on the first request to a page nobody tested.
func wirePluginSeams(c *core.Core, wsrv *web, engine *gin.Engine, logger *slog.Logger) {
	// Forum plugin seams + its gin-side templates (forum_web.go). Before
	// Boot: SetDeps is checked at Provision. wsrv is passed so the plugin's
	// pages get the host's chrome data (nav, theme, viewer tiles) from the
	// SAME function render() uses — see chromeData.
	if err := wsrv.wireForumPlugin(c, engine); err != nil {
		logger.Error("forum wiring", "err", err)
		os.Exit(1)
	}

	// News plugin seams (news_web.go). Renders host templates through the same
	// gin HTML set the forum uses — pluginTemplates() parses both dirs — so it
	// needs no engine argument, only the chrome closure and the host's HTML
	// sanitization policy.
	if err := wireNewsPlugin(c, wsrv); err != nil {
		logger.Error("news wiring", "err", err)
		os.Exit(1)
	}

	// Wiki plugin seams (wiki_web.go). Needs the engine as well as the chrome
	// closure: it serves admin image uploads off a static route.
	if err := wireWikiPlugin(c, engine, wsrv); err != nil {
		logger.Error("wiki wiring", "err", err)
		os.Exit(1)
	}

	// Communities plugin seams (communities_web.go) — user-owned sub-forums at
	// /c/*. Wired LAST of the gin-template plugins because its joins need
	// users.avatar_path, users.points and users.reputation_tier, which the
	// messages and points work added.
	if err := wireCommunitiesPlugin(c, wsrv); err != nil {
		logger.Error("communities wiring", "err", err)
		os.Exit(1)
	}

	// Messages plugin seams (messages_web.go) — threaded DMs + admin
	// announcements at /inbox. Distinct from /p/inbox, which is the baseline's
	// NOTIFICATION inbox.
	if err := wireMessagesPlugin(c, wsrv); err != nil {
		logger.Error("messages wiring", "err", err)
		os.Exit(1)
	}

	// Hit-and-run seams (hitrun_web.go). Messages only: the plugin detects,
	// the host punishes, and the punishing half is the middleware installed
	// further down.
	wireHitRunPlugin(c, wsrv, logger)

	// Perks wallet seams (hitrun_web.go). The plugin registers its own tracker
	// multiplier, so this is only the page a member spends a token on.
	wirePerksPlugin(wsrv)

	// Seed-lock claims page (hitrun_web.go). The plugin installs its own
	// announce guard; this is the page its refusal message points at.
	wireSeedLockPlugin(wsrv)

	// Tracker plugin seams (tracker_web.go). Always wired, even when the
	// tracker is off: SetDeps runs before Boot and the plugin decides for
	// itself whether to mount anything, so an unused seam costs nothing while a
	// missing one is a 500 on a page a member opened. The plugin refuses to
	// boot rather than defer if a seam is absent.
	wsrv.wireTrackerPlugin()

	// Store plugin seams (store_web.go). No error return: it self-migrates and
	// its only seams are the chrome closure plus two pagination helpers.
	wireStorePlugin(wsrv)

	// Playlists plugin seams (playlists_web.go). Self-migrating, so no DDL
	// here; its two lookup seams resolve release and user ids the plugin
	// deliberately does not join to itself.
	if err := wirePlaylistsPlugin(wsrv); err != nil {
		logger.Error("playlists wiring", "err", err)
		os.Exit(1)
	}

	// Tickets plugin seams (tickets_web.go) — the helpdesk at /support.
	if err := wireTicketsPlugin(c, wsrv); err != nil {
		logger.Error("tickets wiring", "err", err)
		os.Exit(1)
	}

	// Donations plugin seams (donations_web.go). DEV-ONLY: gated on
	// LOON_DONATIONS=1, and OFF without it regardless of the persisted
	// admin toggle — this plugin takes real money through BTCPay.
	if err := wireDonationsPlugin(c, wsrv); err != nil {
		logger.Error("donations wiring", "err", err)
		os.Exit(1)
	}
	logger.Info("donations", "enabled", donationsEnabled)
}
