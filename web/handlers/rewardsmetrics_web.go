package handlers

import (
	"github.com/the-loon-clan/loon-site/internal/middleware"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-site/internal/storage"

	"context"
	"fmt"
	"log/slog"

	"github.com/the-loon-clan/loon-plugins/achievements"
	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon-plugins/rewards"
	"github.com/the-loon-clan/loon/blob"
	"github.com/the-loon-clan/loon/core"
)

// Achievement metrics — the HOST's half of the rewards contract.
//
// The plugin declares WHICH metrics exist (rewards.StockSources: login,
// posts.created, threads.created, comments.created, uploads.created,
// requests.created, requests.filled) and scores achievements against them. It
// does not know how to count any of them, because counting is a question about
// this host's tables. So each one arrives as an extension registered under
// "rewards.metrics.<key>", and a metric with no source simply never progresses.
//
// Only the three this site can answer HONESTLY are registered. The others are
// deliberately absent rather than stubbed at zero:
//
//	comments.created  no comments table on this host
//	uploads.created   releases come off Usenet, not from members — nobody
//	                  "uploads" here, so the number would be zero for everyone
//	                  forever and an achievement on it could never complete
//	requests.*        no request system
//
// A stub returning zero looks identical to a real counter for a member who has
// done nothing, which is exactly the confusion worth avoiding: an achievement
// nobody can ever earn should be impossible to configure, not quietly stuck.

// metricFunc adapts a query to rewards.MetricSource. One call returns every
// member's value, which is the contract — the scoring job must not make a round
// trip per member to discover that almost nobody moved.
type metricFunc func(ctx context.Context) (map[int64]int64, error)

// Values satisfies the rewards plugin's metric interface.
//
// The adapter that lets a plain function be a metric source, so a host adds one
// by writing a query rather than a type.
func (f metricFunc) Values(ctx context.Context) (map[int64]int64, error) { return f(ctx) }

// countBy runs a "user_id, count" query into the map the plugin wants.
// query is storage.SQL, not string: countBy takes a statement as a PARAMETER,
// which is the shape that turns into an injection the moment somebody passes a
// built one. Typed, only a constant can reach it.
func countBy(db storage.Conn, query storage.SQL) metricFunc {
	return func(ctx context.Context) (map[int64]int64, error) {
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		out := map[int64]int64{}
		for rows.Next() {
			var id, n int64
			if err := rows.Scan(&id, &n); err != nil {
				return nil, err
			}
			// user_id 0 is the anonymous/system row the login log keeps for a
			// failed attempt against a name that does not exist. It is not a
			// member and must not be scored as one.
			if id > 0 {
				out[id] = n
			}
		}
		return out, rows.Err()
	}
}

// registerAchievementMetrics publishes the counters. MUST be called BEFORE
// core.Boot: the rewards plugin collects these during its own Provision by
// scanning the registry, so one registered afterwards is never seen and its
// achievements sit at zero forever with nothing logged.
// achievementMetrics is every counter this host can answer, keyed by the
// plugin's metric name. One map so the registration below and the seed's
// definitions cannot disagree about which metrics exist — a definition naming
// an unregistered metric is INERT: it never progresses, and nothing says so.
func achievementMetrics(db storage.Conn) map[string]metricFunc {
	m := map[string]metricFunc{
		// Posts a member has written. Hidden posts still count: the member did
		// write them, and un-counting on moderation would walk an achievement
		// backwards, which the plugin has no way to represent.
		"posts.created": countBy(db,
			`SELECT user_id, COUNT(*) FROM forum_posts GROUP BY user_id`),
		// Threads started, which is a different thing from replies.
		"threads.created": countBy(db,
			`SELECT user_id, COUNT(*) FROM forum_threads GROUP BY user_id`),
		// SUCCESSFUL sign-ins only. Counting failures would let someone earn a
		// badge by typing the wrong password enough times.
		"login": countBy(db,
			`SELECT user_id, COUNT(*) FROM login_logs WHERE success GROUP BY user_id`),
	}
	// Donations, only on a deployment that takes them.
	//
	// Gated for the reason the absent metrics above are absent: with
	// LOON_DONATIONS unset nothing can ever move this, and a threshold nobody
	// can reach is worse than no threshold — it is configurable, looks healthy,
	// and never completes. Unlike comments.created this is deployment config
	// rather than a missing table, so it appears the moment the flag is set.
	//
	// WHOLE DOLLARS, floored. An achievement threshold is an integer and the
	// tiers people write are "$50", not "$50.00" — and floor rather than round
	// so $49.99 does not unlock the $50 tier.
	//
	// THE LIFETIME SUM, which is the entire point: ten payments of $5 reach $50.
	// donations/events.go argues the same thing from the other side — a donor
	// badge must not be scored on the donations.received event, because
	// counting settlements ranks fifty $1 tips above one $500 gift. This column
	// is that argument's "absolute metric path".
	//
	// The value is maintained by donations.CreateDonation, which could not
	// write it at all until the three columns it targets were added — see
	// MigrateDonations. Nothing read this figure before now.
	if donationsEnabled {
		m["donations.total_usd"] = countBy(db,
			`SELECT id, FLOOR(donation_total_usd)::bigint
			   FROM users WHERE donation_total_usd > 0`)
	}
	return m
}

// achievementSourceDefs is the dropdown an admin picks a metric from.
//
// IT WAS EMPTY. rewards seeds its catalogue from whatever the host registers
// under SourceCatalogExtension, this host registered nothing, and
// rewards.reward_sources therefore held zero rows — so the achievement admin
// offered no metric to choose and no achievement could be created through the
// UI at all. The plugin's own comment predicted exactly this: "That list is
// empty exactly when it is most needed — setting up the first one."
//
// Not rewards.StockSources(). That set names seven things a general site might
// count, four of which this host cannot answer (see the header) — and a source
// that Counts with no registered metric is inert: selectable, plausible, and
// permanently stuck at zero. So the catalogue is derived from what this host
// actually registers, and rewardsmetrics_web_test.go pins the two together in
// both directions.
func achievementSourceDefs() rewards.SourceCatalog {
	defs := rewards.SourceCatalog{
		{Key: "login", Label: "Logged in", Group: "Account",
			Fires: true, Counts: true, Unit: "login", Units: "logins"},
		{Key: "posts.created", Label: "Posts created", Group: "Forum",
			Fires: true, Counts: true, Unit: "post", Units: "posts"},
		{Key: "threads.created", Label: "Threads started", Group: "Forum",
			Fires: true, Counts: true, Unit: "thread", Units: "threads"},
	}
	// Behind the same flag as its counter, and the test that made this
	// conditional is the argument for it: the first version offered this row
	// unconditionally while achievementMetrics gated the metric, so on a
	// deployment without donations an admin could pick "Donated (USD)", set a
	// $50 threshold, and watch it sit at zero forever. That is the exact shape
	// of inertness this whole file is written against, and it took about a
	// minute to reintroduce.
	if !donationsEnabled {
		return defs
	}
	return append(defs,
		// Fires: FALSE, and it is not an oversight.
		//
		// donations.received exists, but it can never drive a per-member
		// threshold: it is declared EventSystem because the tip jar takes money
		// from nobody in particular, so Event.UserID is always 0 — and both of
		// rewards' subscribers return immediately on UserID 0. core refuses
		// Countable on a system event for the same reason (events.go:140).
		//
		// So this source only ever Counts, and the counting is the reconciling
		// job reading the column. Which is also better: the job SETS the
		// absolute total every tick, so a webhook that was dropped or retried
		// corrects itself instead of leaving a donor permanently short.
		rewards.SourceDef{Key: "donations.total_usd", Label: "Donated (USD)", Group: "Donations",
			Fires: false, Counts: true, Unit: "dollar", Units: "dollars"},
	)
}

// registerAchievementMetrics publishes every counter above, plus the payout
// handler that lets an earned achievement actually be claimed.
func registerAchievementMetrics(c *core.Core, db storage.Conn) error {
	for key, src := range achievementMetrics(db) {
		if err := c.Register(achievements.MetricSourcePrefix+key, src); err != nil {
			return fmt.Errorf("register metric %q: %w", key, err)
		}
	}
	// The dropdown itself. Same before-Boot rule as the metrics: rewards seeds
	// its catalogue during its own Provision, so registering afterwards leaves
	// the table empty and the admin with nothing to pick.
	if err := c.Register(rewards.SourceCatalogExtension, achievementSourceDefs()); err != nil {
		return fmt.Errorf("register source catalogue: %w", err)
	}
	// Same before-Boot rule as the metrics: the plugin looks this up during its
	// own Provision, so one registered afterwards is never seen.
	if err := c.Register("rewards.payout."+string(rewards.PayoutMedal),
		medalPayoutHandler(c)); err != nil {
		return fmt.Errorf("register medal payout: %w", err)
	}
	if err := c.Register("rewards.payout."+string(rewards.PayoutAchievement),
		achievementPayoutHandler(c)); err != nil {
		return fmt.Errorf("register achievement payout handler: %w", err)
	}
	// The claim card's CSRF token, host-minted like every other form seam —
	// the token the card embeds must be the one csrf.go checks. Without it the
	// card's Claim button 403'd for every member, invisibly: the card only
	// renders when a member holds an unclaimed grant, so no crawl saw the form.
	if err := c.Register(rewards.CSRFExtension,
		func(gc *gin.Context) string { return middleware.Token(gc) }); err != nil {
		return fmt.Errorf("register rewards csrf: %w", err)
	}
	// The achievements definition page's two optional extras. Files shares the
	// wiki's upload root, so /uploads/* is already statically served; icons is
	// the sprite subset that makes sense on a badge — offering the whole sheet
	// would put #logo and #chevron-down in a picker where they mean nothing.
	if err := c.Register("achievements.files", blob.Store(blob.NewLocal(uploadRoot, uploadURL))); err != nil {
		return fmt.Errorf("register achievements files: %w", err)
	}
	// CURATED, then checked against the sheet. The curation is the point — the
	// whole catalogue would put #logo and #chevron-down in a picker where they
	// mean nothing — but a curated name is still a name, and one that has been
	// renamed out of the sprite sheet would sit in the dropdown offering an
	// empty badge. Only ids the site can actually draw are published, and a
	// curated one that has gone missing is said out loud rather than dropped
	// silently, because the fix is to re-curate rather than to shrink.
	wanted := []string{
		"star", "verified", "shield", "coin", "film", "tv", "music", "book",
		"comment", "users", "download", "server", "globe", "calendar",
	}
	var icons []string
	for _, id := range wanted {
		if drawableIcon(id) {
			icons = append(icons, id)
			continue
		}
		c.Logger.Warn("achievement icon curated but not in the sprite sheet", "icon", id)
	}
	if err := c.Register("achievements.icons", icons); err != nil {
		return fmt.Errorf("register achievements icons: %w", err)
	}
	return nil
}

// achievementDef is one seeded achievement, WITH the badge reward it grants.
//
// Its own reward, never a shared one. Rewards are one_off: a member can hold
// each at most once, so two achievements pointing at the same reward means
// whichever completes first takes it and the other can never complete for that
// member — silently, showing as permanently in-progress. Reusing a reward that
// already has grants is the same trap with a head start.
type achievementDef struct {
	Slug, Name, Desc, Metric string
	Threshold                int
	Ordinal                  int
}

// achievementSeeds are the demo's starting catalogue. Every Metric here must be
// a key of achievementMetrics — a definition naming an unregistered metric is
// inert forever, and the test pins that.
//
// Thresholds show BOTH states on a fresh demo: the low ones are already met by
// the seeded forum activity, the high ones sit in progress. A page where
// everything is unlocked demonstrates as little as one where nothing is.
var achievementSeeds = []achievementDef{
	{"first-post", "First Post", "Reply to a thread.", "posts.created", 1, 0},
	{"forum-regular", "Forum Regular", "Write 5 posts.", "posts.created", 5, 1},
	{"thread-starter", "Thread Starter", "Start a thread of your own.", "threads.created", 1, 2},
	{"conversationalist", "Conversationalist", "Write 25 posts.", "posts.created", 25, 3},
	{"familiar-face", "Familiar Face", "Sign in 10 times.", "login", 10, 4},
}

// achievementsSeed gives the demo a working achievements page on first boot.
//
// Runs only when the catalogue is EMPTY, the same guard forumSeed uses: an
// operator who has configured their own achievements never has them touched.
//
// Each definition points at a metric registered above, so every one is
// scoreable. The rewards plugin already ships five badge rewards, and two of
// them fit a metric this host can count — Forum Regular is a post counter by
// name, Night Owl and Early Adopter are login-triggered. The other two are
// grab-triggered, and nothing here counts per-member grabs, so they get no
// achievement rather than a metric that does not mean what the name says.
//
// Thresholds are chosen to show BOTH states on a fresh demo: the low ones are
// already earned by the seeded forum activity, the high ones sit in progress.
// A page where everything is unlocked demonstrates as little as one where
// nothing is.
func achievementsSeed(db storage.Conn, w *web, log *slog.Logger) {
	var n int
	if err := db.Get(&n, `SELECT COUNT(*) FROM achievements.achievements`); err != nil || n > 0 {
		return
	}
	for _, d := range achievementSeeds {
		// One badge reward per achievement, created here rather than borrowed
		// from the plugin's own catalogue — see achievementDef.
		var rewardID int64
		if err := db.Get(&rewardID, `
			INSERT INTO rewards.rewards (slug, name, kind, trigger, delivery, enabled)
			VALUES ($1, $2, 'one_off', '', 'auto', true)
			ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name
			RETURNING id`, d.Slug, d.Name); err != nil {
			log.Error("achievements seed: reward", "slug", d.Slug, "err", err)
			return
		}
		// The payout IS the badge: kind 'achievement' targeting this slug.
		if _, err := db.Exec(`
			INSERT INTO rewards.reward_payouts (reward_id, kind, target, amount, ordinal)
			VALUES ($1, 'achievement', $2, 0, 0)
			ON CONFLICT DO NOTHING`, rewardID, d.Slug); err != nil {
			log.Error("achievements seed: payout", "slug", d.Slug, "err", err)
			return
		}
		// The definition lives in the achievements plugin's schema now, and
		// names its payment by SLUG through the cross-plugin granter — the
		// reward_id foreign key died with the shared schema. Each seeded
		// definition also names its message-catalogue slugs, so the demo's
		// badges are translatable at /admin/i18n out of the box.
		if _, err := db.Exec(`
			INSERT INTO achievements.achievements
			    (slug, name, description, reward_slug, metric, threshold, ordinal,
			     title_slug, description_slug, enabled)
			VALUES ($1, $2, $3, $1, $4, $5, $6, $7, $8, true)
			ON CONFLICT (slug) DO NOTHING`,
			d.Slug, d.Name, d.Desc, d.Metric, d.Threshold, d.Ordinal,
			"ach."+d.Slug+".title", "ach."+d.Slug+".desc"); err != nil {
			log.Error("achievements seed", "slug", d.Slug, "err", err)
			return
		}
	}
	// The catalogue texts behind those slugs, through the SAME seed-only path
	// pluginapi.I18nDeclarer gives plugins — so the demo exercises the seam's
	// semantics (an operator's cell is never overwritten) rather than keeping
	// a private insert that could drift from them.
	defaults := map[string]string{}
	for _, d := range achievementSeeds {
		defaults["ach."+d.Slug+".title"] = d.Name
		defaults["ach."+d.Slug+".desc"] = d.Desc
	}
	if err := w.declareI18n(context.Background(), defaults); err != nil {
		log.Error("achievements seed: catalogue defaults", "err", err)
		return
	}
	log.Info("seeded the achievements catalogue")
}

// medalPayoutHandler settles a payout of kind "medal" — the kind that was
// declared in rewards with no implementation anywhere, until the medals
// plugin gave it a cabinet to land in. Same shape as the achievement
// handler below and lazy for the same reason: the granter is a sibling
// plugin's registration, resolved at settle time so order never matters,
// and a medal-less host settles medal payouts as the historical no-op.
func medalPayoutHandler(c *core.Core) rewards.PayoutHandler {
	return func(ctx context.Context, g rewards.Grant, p rewards.Payout) error {
		v, ok := c.Lookup(pluginapi.MedalGranterName)
		if !ok {
			return nil
		}
		granter, ok := v.(pluginapi.MedalGranter)
		if !ok {
			return nil
		}
		// Idempotent per the granter's contract; unknown slugs are ITS no-op.
		_ = granter.GrantMedal(ctx, g.UserID, p.Target)
		return nil
	}
}

// achievementPayoutHandler settles a payout of kind "achievement".
//
// Without one, an earned achievement can never be claimed: ClaimGrant refuses
// with `no handler for payout kind "achievement"`, the grant stays pending, and
// the plugin reports the achievement as Pending forever. Earning worked, the
// badge appeared, and the loop simply never closed — the claim button returned
// a redirect and changed nothing.
//
// It hands over nothing, and that is correct HERE rather than lazy. The badge
// is not a thing this host stores: the plugin already records completion in its
// own user_achievements table, and every surface that shows a badge — the
// achievements page, the profile card — reads it back through the
// rewards.achievements extension. There is no second place for a handler to
// write. What the handler is FOR is letting the grant settle, which is what
// moves the achievement from Pending to Unlocked.
//
// A host that keeps its own medal cabinet would write to it here instead, and
// the Grant carries what that needs: g.UserID for whose, p.Target for which.
//
// Idempotent by construction, which the contract requires: running twice for
// the same (user, payout) does the same nothing.
//
// Deliberately NOT validating that p.Target names a known achievement. The
// rewards plugin ships demo rewards whose payouts target badge slugs with no
// achievement row (first-grab, night-owl, completionist, early-adopter), and
// refusing those would turn old pending grants into permanently unclaimable
// ones — a stricter handler that breaks existing data to catch a
// misconfiguration nobody has.
// It can DO something now, which it could not before the split: the
// achievements plugin publishes pluginapi.AchievementGranter, so a reward
// whose payout targets a REAL achievement slug marks it earned. The old
// tolerance survives — the demo ships payouts targeting badge slugs with no
// achievement row (first-grab, night-owl, ...), and refusing those would turn
// old pending grants permanently unclaimable, so an unknown slug settles as
// the historical no-op rather than erroring.
func achievementPayoutHandler(c *core.Core) rewards.PayoutHandler {
	return func(ctx context.Context, g rewards.Grant, p rewards.Payout) error {
		v, ok := c.Lookup(pluginapi.AchievementGranterName)
		if !ok {
			return nil // no achievements plugin: the historical no-op
		}
		granter, ok := v.(pluginapi.AchievementGranter)
		if !ok {
			return nil
		}
		// Best-effort and idempotent per the granter's contract; an unknown
		// slug is ITS no-op, matching the tolerance above.
		_ = granter.GrantAchievement(ctx, g.UserID, p.Target)
		return nil
	}
}
