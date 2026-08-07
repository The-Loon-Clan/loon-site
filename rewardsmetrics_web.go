package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon-plugins/rewards"
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

func (f metricFunc) Values(ctx context.Context) (map[int64]int64, error) { return f(ctx) }

// countBy runs a "user_id, count" query into the map the plugin wants.
func countBy(db *sqlx.DB, query string) metricFunc {
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
func achievementMetrics(db *sqlx.DB) map[string]metricFunc {
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

// registerAchievementMetrics publishes every counter above.
func registerAchievementMetrics(c *core.Core, db *sqlx.DB) error {
	for key, src := range achievementMetrics(db) {
		if err := c.Register(rewards.MetricSourcePrefix+key, src); err != nil {
			return fmt.Errorf("register metric %q: %w", key, err)
		}
	}
	return nil
}

// achievementDef is one seeded achievement.
type achievementDef struct {
	Slug, Name, Desc, Metric string
	Threshold                int
	RewardSlug               string
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
	{"first-post", "First Post", "Reply to a thread.", "posts.created", 1, "forum-regular", 0},
	{"forum-regular", "Forum Regular", "Write 5 posts.", "posts.created", 5, "forum-regular", 1},
	{"thread-starter", "Thread Starter", "Start a thread of your own.", "threads.created", 1, "night-owl", 2},
	{"familiar-face", "Familiar Face", "Sign in 10 times.", "login", 10, "early-adopter", 3},
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
func achievementsSeed(db *sqlx.DB, log *slog.Logger) {
	var n int
	if err := db.Get(&n, `SELECT COUNT(*) FROM rewards.achievements`); err != nil || n > 0 {
		return
	}
	for _, d := range achievementSeeds {
		var rewardID int64
		if err := db.Get(&rewardID,
			`SELECT id FROM rewards.rewards WHERE slug = $1`, d.RewardSlug); err != nil {
			// The reward catalogue is the rewards plugin's to seed. If it has
			// not run yet there is nothing to attach to, and a half-seeded
			// catalogue is worse than none — stop rather than insert rows
			// pointing at an id that does not exist.
			log.Warn("achievements seed: reward missing, skipping", "reward", d.RewardSlug)
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
