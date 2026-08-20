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
	storeCreditItems(db, log)
	newsSeed(db, log)
	// The tracker (demoseedtracker_web.go). Last because it is the only seeder
	// that reads another table to decide what to write — its torrents are made
	// from releases already in the index — and the only one gated on a feature
	// flag as well as an empty table.
	trackerSeed(db, log)
	// Where the comments widget goes. CONFIGURATION, not content — the line
	// this file draws is content an operator curates versus content members
	// generate, and a widget placement is squarely the first. It seeds nothing
	// anybody said; it decides where the box appears.
	//
	// Worth doing because the alternative is a release page with no comment
	// section until somebody opens the widget editor and knows to look for it,
	// which is a feature that ships switched off by accident.
	widgetSeed(db, log)
	// One poll, in the sidebar. Unlike the placement above this DOES write
	// content — a question — which is the line this file otherwise draws. It
	// is on the right side of it for the same reason the news posts are: an
	// operator writes it, nobody is quoted, and a widget whose whole claim is
	// "place a poll anywhere" is worth one you can actually see placed.
	pollSeed(db, log)
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
	// Name effects, in BOTH shapes the cosmetic type supports, because the
	// difference is the thing an operator has to see to understand the type: a
	// def with no reward_ref sells the catalogue and the buyer picks, a def
	// with one sells that effect alone. The pinned row is also the VIP shape —
	// a single effect on a clock — which is otherwise only describable in
	// prose.
	if _, err := db.Exec(`
		INSERT INTO store.items (name, description, points_cost, reward_type, reward_ref, reward_days, stock, active, sort_order)
		VALUES ('Name effect',
		        'Pick an effect for your username. Own several and switch whenever you like.',
		        400, 'cosmetic', '', 0, -1, true, 35),
		       ('Gold aura (30 days)',
		        'A warm gold halo on your name, for a month.',
		        150, 'cosmetic', 'glow-gold', 30, -1, true, 36)`); err != nil {
		log.Warn("store seed: cosmetics", "err", err)
	}
	// Flair, at the prices the pointstore's own shop charged before that page
	// was retired — the items moved, the economy did not.
	if _, err := db.Exec(`
		INSERT INTO store.items (name, description, points_cost, reward_type, reward_ref, reward_days, stock, active, sort_order)
		VALUES ('Supporter flair', 'A badge on your profile. Replaces whatever you wear now.', 10, 'flair', 'supporter', 0, -1, true, 40),
		       ('VIP flair',       'A badge on your profile. Replaces whatever you wear now.', 25, 'flair', 'vip',       0, -1, true, 50),
		       ('Legend flair',    'A badge on your profile. Replaces whatever you wear now.', 50, 'flair', 'legend',    0, -1, true, 60)`); err != nil {
		log.Warn("store seed: flair", "err", err)
	}
	log.Info("seeded demo store items")
}

// storeCreditItems ENSURES the transfer-credit shelf — MaM's classic BON
// spends — into any catalogue, by name and outside storeSeed's
// only-when-empty guard: an existing shop gains them on upgrade, an
// operator's edits (price, stock, deactivation) survive every boot. All
// flavour=tracker: the shop hides them when the site's tracker half is off.
func storeCreditItems(db storage.Conn, log *slog.Logger) {
	items := []struct {
		Name string
		Cost int
		Kind string
		GB   string
		Sort int
	}{
		{"1.0 GB Uploaded", 200, "upload_gb", "1", 70},
		{"5.0 GB Uploaded", 900, "upload_gb", "5", 80},
		{"10.0 GB Uploaded", 1700, "upload_gb", "10", 90},
		{"100.0 GB Uploaded", 15000, "upload_gb", "100", 100},
		{"10.0 GB Downloaded", 1500, "download_gb", "10", 110},
		{"100.0 GB Downloaded", 13000, "download_gb", "100", 120},
	}
	for _, it := range items {
		desc := "Adds " + it.GB + " GB to your uploaded total."
		if it.Kind == "download_gb" {
			desc = "Wipes " + it.GB + " GB off your downloaded total."
		}
		if _, err := db.Exec(`
			INSERT INTO store.items (name, description, points_cost, reward_type, reward_ref, reward_days, stock, active, sort_order, flavour)
			SELECT $1, $2, $3, $4, $5, 0, -1, true, $6, 'tracker'
			 WHERE NOT EXISTS (SELECT 1 FROM store.items WHERE name = $1)`,
			it.Name, desc, it.Cost, it.Kind, it.GB, it.Sort); err != nil {
			log.Warn("store seed: credit item", "name", it.Name, "err", err)
		}
	}
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

// widgetSeed puts the widgets a fresh site should already have somewhere.
//
// Idempotent through PlaceWidget's ON CONFLICT DO NOTHING, and it does NOT
// re-add anything an operator removed on purpose — the placement row is gone
// in that case and this would put it back. So it is guarded on the region
// being untouched, the same "seed only when empty" rule every other seeder
// here follows.
func widgetSeed(db storage.Conn, log *slog.Logger) {
	if !db.Valid() {
		return
	}
	// SEED ONCE, not seed-if-empty. The first version guarded on the region
	// being empty and did nothing at all, because the tracker plugin already
	// places a swarm widget there — so the rule meant "only on a site with no
	// tracker", which is not a rule anybody chose.
	//
	// A marker instead. It also fixes the thing seed-if-empty gets wrong in
	// the other direction: an operator who removes this widget on purpose gets
	// it back on the next restart, because the placement row is gone and
	// "absent" is exactly what the guard was reading as "never seeded".
	var seeded string
	if err := db.Get(&seeded, `SELECT value FROM site_settings WHERE key = $1`, widgetSeedKey); err == nil && seeded != "" {
		return
	}
	if _, err := db.Exec(`
		INSERT INTO widget_placement (region, slug, position, enabled)
		VALUES ('release-main', 'comments', 0, TRUE)
		ON CONFLICT (region, slug) DO NOTHING`); err != nil {
		log.Warn("widget seed: comments", "err", err)
		return
	}
	if _, err := db.Exec(`
		INSERT INTO site_settings (key, value) VALUES ($1, '1')
		ON CONFLICT (key) DO UPDATE SET value = '1'`, widgetSeedKey); err != nil {
		log.Warn("widget seed: marker", "err", err)
	}
	log.Info("seeded widget placement", "region", "release-main", "slug", "comments")
}

// pollSeed gives the demo a poll to look at, in the sidebar where one belongs.
//
// The polls plugin ships no content of its own, correctly — a plugin that
// invented a question would be putting words in an operator's mouth. But this
// is the reference host, and a feature nobody can see is a feature nobody
// evaluates: the whole point of the widget is that a poll can be placed
// anywhere, and that claim is worth ONE placement somebody can actually look
// at.
//
// Its own marker rather than a bump of widgetSeedKey. Bumping that key would
// re-run the comments placement too, which is precisely the thing its comment
// warns about — an operator who removed that widget on purpose would get it
// back.
func pollSeed(db storage.Conn, log *slog.Logger) {
	if !db.Valid() {
		return
	}
	var seeded string
	if err := db.Get(&seeded, `SELECT value FROM site_settings WHERE key = $1`, pollSeedKey); err == nil && seeded != "" {
		return
	}
	// A question the demo can actually answer for itself, rather than a
	// stand-in: somebody clicking around this site has an opinion about what it
	// should index, and none of the alternatives is a wrong answer.
	if _, err := db.Exec(`
		WITH p AS (
			INSERT INTO polls.polls (slug, question, results)
			VALUES ('demo-what-next', 'What should a site like this index?', 'after_vote')
			ON CONFLICT (slug) DO NOTHING
			RETURNING id
		)
		INSERT INTO polls.poll_options (poll_id, ordinal, label)
		SELECT p.id, o.ordinal, o.label
		  FROM p, (VALUES
			(0, 'Usenet, and nothing else'),
			(1, 'Torrents, and nothing else'),
			(2, 'Both, with one page per release')
		  ) AS o(ordinal, label)`); err != nil {
		log.Warn("poll seed", "err", err)
		return
	}
	// Placed in the right sidebar, which is empty on a fresh site — so the
	// poll is the thing that makes the column exist rather than something
	// squeezed in beside an existing widget.
	if _, err := db.Exec(`
		INSERT INTO widget_placement (region, slug, position, enabled, config)
		VALUES ('sidebar-right', 'poll', 0, TRUE, 'demo-what-next')
		ON CONFLICT (region, slug) DO NOTHING`); err != nil {
		log.Warn("poll seed: placement", "err", err)
		return
	}
	if _, err := db.Exec(`
		INSERT INTO site_settings (key, value) VALUES ($1, '1')
		ON CONFLICT (key) DO UPDATE SET value = '1'`, pollSeedKey); err != nil {
		log.Warn("poll seed: marker", "err", err)
	}
	log.Info("seeded demo poll", "slug", "demo-what-next", "region", "sidebar-right")
}

// pollSeedKey marks that the demo poll and its placement have been laid down.
const pollSeedKey = "seeded.poll.v1"

// widgetSeedKey marks that the default placements have been laid down. Its
// value is never read beyond "is it set" — what matters is that it survives an
// operator deleting the placement it created.
const widgetSeedKey = "seeded.widgets.v1"
