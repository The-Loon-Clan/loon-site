package handlers

import (
	"github.com/the-loon-clan/loon-site/internal/storage"

	"context"
	"fmt"
	"log/slog"

	"github.com/the-loon-clan/loon-plugins/rewards"
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
		defer rows.Close()
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
	return map[string]metricFunc{
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
}

// registerAchievementMetrics publishes every counter above, plus the payout
// handler that lets an earned achievement actually be claimed.
func registerAchievementMetrics(c *core.Core, db storage.Conn) error {
	for key, src := range achievementMetrics(db) {
		if err := c.Register(rewards.MetricSourcePrefix+key, src); err != nil {
			return fmt.Errorf("register metric %q: %w", key, err)
		}
	}
	// Same before-Boot rule as the metrics: the plugin looks this up during its
	// own Provision, so one registered afterwards is never seen.
	if err := c.Register("rewards.payout."+string(rewards.PayoutAchievement),
		achievementPayoutHandler()); err != nil {
		return fmt.Errorf("register achievement payout handler: %w", err)
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
func achievementsSeed(db storage.Conn, log *slog.Logger) {
	var n int
	if err := db.Get(&n, `SELECT COUNT(*) FROM rewards.achievements`); err != nil || n > 0 {
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
		if _, err := db.Exec(`
			INSERT INTO rewards.achievements
			    (slug, name, description, reward_id, metric, threshold, ordinal, enabled)
			VALUES ($1, $2, $3, $4, $5, $6, $7, true)
			ON CONFLICT (slug) DO NOTHING`,
			d.Slug, d.Name, d.Desc, rewardID, d.Metric, d.Threshold, d.Ordinal); err != nil {
			log.Error("achievements seed", "slug", d.Slug, "err", err)
			return
		}
	}
	log.Info("seeded the achievements catalogue")
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
func achievementPayoutHandler() rewards.PayoutHandler {
	return func(ctx context.Context, g rewards.Grant, p rewards.Payout) error {
		return nil
	}
}
