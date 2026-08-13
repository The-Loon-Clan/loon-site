package handlers

import (
	"github.com/the-loon-clan/loon-site/internal/storage"

	"log/slog"
)

// Demo data for the plugins that show nothing until somebody puts something in
// them.
//
// docs/BACKLOG.md #5: on a site whose PURPOSE is showing what the framework
// does, "unseeded" and "broken" are indistinguishable to a visitor, and both
// read as "this feature does not work". Achievements looked dead and were
// merely unseeded; ranks and the store were the same.
//
// SEED ONLY WHEN EMPTY, in the shape forumSeed and achievementsSeed already
// use. The check is on the table this function owns, so an operator who deleted
// the demo rows on purpose does not get them back on the next restart, and one
// who added their own never sees these at all.
//
// Deliberately NOT seeded, because seeding them would be dishonest rather than
// helpful:
//
//   - usenet.nzbs. The releases are the one thing this site is actually about,
//     and inventing them would make every figure on the home page, every
//     listing and every stat a fabrication. An empty index is the truthful
//     state of a demo with no news server configured, and the wizard says so.
//   - playlists, bookmarks, grabs. All of them point AT releases; a playlist of
//     nothing is worse than no playlist.
//   - support tickets, DMs, communities, forum threads beyond the existing
//     seed. These are records of people talking to each other. Fabricating a
//     support conversation puts words in a member's mouth, and the demo
//     accounts are the only mouths available.
//
// The line is content an OPERATOR curates (a rank ladder, a shop, an
// announcement) versus content MEMBERS generate. Seeding the first is
// configuration; seeding the second is fiction.

// demoSeed runs every seeder. Order matters where one references another.
func demoSeed(db storage.Conn, log *slog.Logger) {
	if !db.Valid() {
		return
	}
	// Ranks first: the store's rank items reference a rank by id.
	ranksSeed(db, log)
	storeSeed(db, log)
	newsSeed(db, log)
}

// ranksSeed creates the rank ladder.
//
// A ladder is CONFIGURATION — every tracker ships one and an operator edits it
// — so an empty ranks table is not "no data yet", it is a feature that has not
// been set up. Three kinds because the plugin has three and a demo that shows
// one teaches the wrong shape: earned by activity, bought with points, given by
// staff.
func ranksSeed(db storage.Conn, log *slog.Logger) {
	var n int
	if err := db.Get(&n, `SELECT COUNT(*) FROM ranks.groups`); err != nil || n > 0 {
		return
	}
	// Two things the plugin's own vocabulary decides, both of which the first
	// version of this seed got wrong:
	//
	//   color is a Bootstrap NAME, not a hex string. The template renders
	//   class="badge bg-{{.Color}}", so "#3fb618" produces bg-#3fb618 and no
	//   colour at all, which looks like the field being ignored.
	//
	//   duration_days is NOT NULL with CHECK (>= 1), so there is no way to say
	//   "permanent" -- the admin form clamps anything under 1 to 30 and the
	//   value is simply unused for earned and assigned ranks. Seeding 30
	//   everywhere is exactly what creating these through the form would store.
	if _, err := db.Exec(`
		INSERT INTO ranks.groups (slug, name, kind, visible, color, title_color, cost_points, duration_days, sort_order) VALUES
		  ('newcomer',    'Newcomer',    'earned',   true, 'secondary', '', 0,    30, 10),
		  ('regular',     'Regular',     'earned',   true, 'info',      '', 0,    30, 20),
		  ('contributor', 'Contributor', 'earned',   true, 'success',   '', 0,    30, 30),
		  ('supporter',   'Supporter',   'paid',     true, 'warning',   '', 500,  30, 40),
		  ('patron',      'Patron',      'paid',     true, 'primary',   '', 2000, 90, 50),
		  ('veteran',     'Veteran',     'assigned', true, 'danger',    '', 0,    30, 60)
	`); err != nil {
		log.Warn("ranks seed", "err", err)
		return
	}
	log.Info("seeded demo ranks", "groups", 6)
}

// storeSeed fills the points shop.
//
// Every item is REAL: each one resolves through a reward type the store plugin
// actually implements, against a rank that actually exists or the host's own
// invite balance. A shop full of items that error on purchase is a worse demo
// than an empty one, because the failure arrives after the points are spent.
func storeSeed(db storage.Conn, log *slog.Logger) {
	var n int
	if err := db.Get(&n, `SELECT COUNT(*) FROM store.items`); err != nil || n > 0 {
		return
	}
	// The rank items point at ids, not slugs (see grantReward), so they are
	// resolved from the rows just inserted rather than hardcoded.
	if _, err := db.Exec(`
		INSERT INTO store.items (name, description, points_cost, reward_type, reward_ref, reward_days, stock, active, sort_order)
		SELECT 'Supporter rank (30 days)',
		       'Wear the Supporter colour for a month.',
		       500, 'rank', g.id::text, 30, -1, true, 10
		  FROM ranks.groups g WHERE g.slug = 'supporter'`); err != nil {
		log.Warn("store seed: supporter", "err", err)
	}
	if _, err := db.Exec(`
		INSERT INTO store.items (name, description, points_cost, reward_type, reward_ref, reward_days, stock, active, sort_order)
		SELECT 'Patron rank (90 days)',
		       'Three months of Patron, and the colour that goes with it.',
		       2000, 'rank', g.id::text, 90, -1, true, 20
		  FROM ranks.groups g WHERE g.slug = 'patron'`); err != nil {
		log.Warn("store seed: patron", "err", err)
	}
	// stock -1 is unlimited for the ranks; the invite is capped so the shop
	// demonstrates a limited item too, which is the case an operator most wants
	// to see working before they trust it.
	if _, err := db.Exec(`
		INSERT INTO store.items (name, description, points_cost, reward_type, reward_ref, reward_days, stock, active, sort_order)
		VALUES ('One invite', 'An invite code to hand to somebody you trust.',
		        1000, 'invite', '1', 0, 25, true, 30)`); err != nil {
		log.Warn("store seed: invite", "err", err)
	}
	log.Info("seeded demo store items")
}

// newsSeed writes the announcements.
//
// Two, not ten. The point is that /news renders a post rather than an empty
// panel, and a wall of invented announcements is its own kind of dishonesty —
// it implies a history the site does not have. Both say plainly that this is a
// demonstration, because a visitor reading a seeded announcement written as if
// it were real has been misled by the demo itself.
func newsSeed(db storage.Conn, log *slog.Logger) {
	var n int
	if err := db.Get(&n, `SELECT COUNT(*) FROM news_posts`); err != nil || n > 0 {
		return
	}
	if _, err := db.Exec(`
		INSERT INTO news_posts (title, slug, body, published) VALUES
		  ('Welcome to the demo',
		   'welcome-to-the-demo',
		   E'This is a demonstration of the **loon** indexer framework, not a running tracker.\n\n' ||
		   E'Everything you can click is real code: the forum, the store, ranks, achievements, ' ||
		   E'communities and the Newznab API all come from plugins, and the site around them is ' ||
		   E'the reference host.\n\n' ||
		   E'What is *not* real is the index itself. No news server is configured, so there are no ' ||
		   E'releases to browse — see the admin wizard if you want to point it at one.',
		   true),
		  ('How the demo data works',
		   'how-the-demo-data-works',
		   E'Some tables are seeded on first boot so the features they drive are visible: the rank ' ||
		   E'ladder, the points shop, the forum categories, and this post.\n\n' ||
		   E'Seeding only ever happens when a table is **empty**, so anything you add or delete ' ||
		   E'stays that way across restarts.\n\n' ||
		   E'Releases, playlists, tickets and messages are deliberately left alone. Those are ' ||
		   E'records of people doing things, and inventing them would put words in somebody''s mouth.',
		   true)
	`); err != nil {
		log.Warn("news seed", "err", err)
		return
	}
	log.Info("seeded demo news posts", "posts", 2)
}
