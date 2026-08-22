package handlers

import (
	"github.com/the-loon-clan/loon-site/internal/storage"

	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/the-loon-clan/loon-plugins/achievements"
	"github.com/the-loon-clan/loon-plugins/rewards"
	"github.com/the-loon-clan/loon/core"
)

// Contract audit — the half-wired seam, reported instead of discovered.
//
// docs/BACKLOG.md #1 counts the instances. Every one had the same shape: a
// contract with two halves, one half filled, and NOTHING anywhere saying the
// other was missing. The symptom was never an error. It was "nothing has
// happened yet":
//
//   - achievements never progressed (no rewards.metrics.<key> registered)
//   - the claim button did nothing  (no rewards.payout.achievement handler)
//   - rewards did not run at all    (plugin gated to the worker process)
//   - communities showed no avatars (joined users, not user_display)
//   - every avatar was blank        (user_display stubbed avatar_path to '')
//   - every reputation tier was 0   (same view, same stub)
//
// The last two hid the longest, and it is worth being precise about why: an
// empty avatar and tier 0 are also what a REAL new account looks like. There
// was no state the site could be in that looked wrong.
//
// So this asks the question none of them answered. It is deliberately
// DATA-DRIVEN rather than a list of the six above: it reads what the site is
// actually using — the payout kinds present in the rewards tables, the metrics
// the seeded achievements name, the columns the identity view exposes — and
// checks the other half is there. A hardcoded list of known bugs goes stale the
// day somebody adds the seventh.
//
// It reports. It does not fail boot. A site that will not start because an
// optional seam is unfilled is worse than one that starts and says so — most
// of these are legitimately optional, and the operator is the one who knows
// which.

// contractFinding is one unfilled half.
type contractFinding struct {
	// Area groups findings on the page: "rewards", "identity".
	Area string
	// What is broken, in the terms the operator sees it: "achievements never
	// progress", not "extension missing".
	Symptom string
	// Detail is the machine-side fact behind the symptom.
	Detail string
	// Fix is the specific thing to do. A finding without one is a complaint.
	Fix string
}

// auditContracts runs every check and returns what is unfilled.
//
// Never returns an error: a check that cannot run (the rewards schema is absent
// on a host that does not wire the plugin) is not a finding, it is a check that
// does not apply. Reporting "could not check" for every plugin a host chose not
// to install would bury the findings that matter.
func auditContracts(ctx context.Context, c *core.Core, db storage.Conn) []contractFinding {
	if !db.Valid() {
		return nil
	}
	registered := map[string]bool{}
	if c != nil {
		for _, n := range c.ExtensionNames() {
			registered[n] = true
		}
	}
	var out []contractFinding
	out = append(out, auditRewardPayouts(ctx, db, registered)...)
	out = append(out, auditAchievementMetrics(ctx, db, registered)...)
	out = append(out, auditUserDisplay(ctx, db)...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Area < out[j].Area })
	return out
}

// auditRewardPayouts finds payout kinds a reward promises that nothing can
// deliver.
//
// The rewards engine handles some kinds ITSELF and SKIPS any other kind with
// no registered handler — deliberately, so a points-only host still works. The
// cost is that a reward promising a medal on a host with no medal handler is
// indistinguishable from one that has not been claimed yet.
//
// The internally-handled set comes from rewards.InternalPayoutKinds() and is
// NOT a list kept here. It was points alone, hardcoded, and lootbox arrived as
// a second one — so every lootbox reward on this site was reported as a
// feature that could not be delivered. A false finding on the page whose whole
// value is that its findings are true, and the plugin is the only thing that
// can answer which kinds it delivers.
func auditRewardPayouts(ctx context.Context, db storage.Conn, registered map[string]bool) []contractFinding {
	var kinds []string
	if err := db.SelectContext(ctx, &kinds, `
		SELECT DISTINCT p.kind
		  FROM rewards.reward_payouts p
		  JOIN rewards.rewards r ON r.id = p.reward_id
		 WHERE r.enabled AND p.kind <> ALL($1)
		 ORDER BY 1`, pq.Array(rewards.InternalPayoutKinds())); err != nil {
		return nil // schema absent: the plugin is not wired here.
	}
	var out []contractFinding
	for _, k := range kinds {
		key := "rewards.payout." + k
		if registered[key] {
			continue
		}
		out = append(out, contractFinding{
			Area:    "rewards",
			Symptom: fmt.Sprintf("a reward promises a %q payout that cannot be delivered", k),
			Detail:  fmt.Sprintf("enabled rewards have %q payout lines, and no %s extension is registered", k, key),
			Fix: fmt.Sprintf("register a rewards.PayoutHandler under %q before core.Boot, "+
				"or disable the rewards that promise it", key),
		})
	}
	return out
}

// auditAchievementMetrics finds achievements counting something nothing counts.
//
// This is the one that shipped: five achievements were seeded against metrics
// whose counters the host had not registered, so every member sat at 0/N
// forever and the page looked like a member who had not done anything yet.
func auditAchievementMetrics(ctx context.Context, db storage.Conn, registered map[string]bool) []contractFinding {
	var metrics []string
	if err := db.SelectContext(ctx, &metrics, `
		SELECT DISTINCT metric FROM rewards.achievements
		 WHERE enabled AND metric <> '' ORDER BY 1`); err != nil {
		return nil
	}
	var out []contractFinding
	for _, m := range metrics {
		key := achievements.MetricSourcePrefix + m
		if registered[key] {
			continue
		}
		out = append(out, contractFinding{
			Area:    "rewards",
			Symptom: fmt.Sprintf("achievements counting %q never progress", m),
			Detail:  fmt.Sprintf("enabled achievements use metric %q, and no %s extension is registered", m, key),
			Fix: fmt.Sprintf("register a rewards.MetricSource under %q before core.Boot "+
				"(rewardsmetrics_web.go), or disable those achievements", key),
		})
	}
	return out
}

// constantExpr matches a SELECT expression that is a literal rather than a
// column: ” , ”::text , 0 , 0::smallint , NULL::text .
var constantExpr = regexp.MustCompile(`^(?i)('[^']*'|-?\d+(\.\d+)?|NULL)(::[a-z0-9_ ]+)?$`)

// auditUserDisplay finds identity columns the plugin-facing view throws away.
//
// user_display is the contract every plugin reads a member through, and
// loon-baseline builds it with avatar_path and reputation_tier stubbed to
// constants until a host fills them in. Two facets landed on this host and the
// view was never updated, so both columns were real, populated, and discarded
// on the way out — for weeks, with nothing reporting it.
//
// Checked by reading the view's own definition rather than by sampling its
// rows. Sampling cannot tell a stubbed column from a real one that happens to
// hold the same value for everybody, which on a small site is most of them.
func auditUserDisplay(ctx context.Context, db storage.Conn) []contractFinding {
	var def string
	if err := db.GetContext(ctx, &def,
		`SELECT pg_get_viewdef('user_display'::regclass, true)`); err != nil {
		return nil
	}
	// Which columns the backing table actually has. A view column with no
	// counterpart on users is not a stub — it is derived, like role.
	var realCols []string
	if err := db.SelectContext(ctx, &realCols, `
		SELECT column_name FROM information_schema.columns
		 WHERE table_name = 'users' AND table_schema = current_schema()`); err != nil {
		return nil
	}
	have := map[string]bool{}
	for _, c := range realCols {
		have[c] = true
	}

	var out []contractFinding
	for _, line := range strings.Split(def, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ","))
		i := strings.LastIndex(strings.ToUpper(line), " AS ")
		if i < 0 {
			continue
		}
		expr := strings.TrimSpace(line[:i])
		col := strings.TrimSpace(line[i+4:])
		if !have[col] || !constantExpr.MatchString(expr) {
			continue
		}
		out = append(out, contractFinding{
			Area:    "identity",
			Symptom: fmt.Sprintf("every plugin reads %s as %s for every member", col, expr),
			Detail: fmt.Sprintf("users.%s exists and holds real data, but user_display selects the constant %s AS %s",
				expr, col, col),
			Fix: fmt.Sprintf("select users.%s in the user_display view (migrateUserDisplay, avatar_web.go) — "+
				"the baseline stubs it deliberately and expects the host to replace it", col),
		})
	}
	return out
}

// reportContracts logs the audit at boot.
//
// WARN, not Info: these are wiring bugs, and the whole reason they survive is
// that they produce no output at all. One line each, with the fix in it, so the
// operator does not have to already know what "extension" means to act.
func reportContracts(ctx context.Context, c *core.Core, db storage.Conn, log logger) []contractFinding {
	found := auditContracts(ctx, c, db)
	for _, f := range found {
		log.Warn("unfilled contract", "area", f.Area, "symptom", f.Symptom, "fix", f.Fix)
	}
	if len(found) == 0 {
		log.Info("contract audit", "unfilled", 0)
	}
	return found
}

// logger is the slice of *slog.Logger this file needs, so the audit is testable
// without one.
type logger interface {
	Warn(msg string, args ...any)
	Info(msg string, args ...any)
}

// contractsDB and contractsCore are held for the admin page, which re-runs the
// audit live rather than showing what boot found. A finding fixed by a restart
// should disappear when the page is refreshed, not persist until somebody
// remembers the log is stale.
var (
	contractsDB   *sqlx.DB
	contractsCore *core.Core
)

// adminContracts serves GET /admin/contracts.
func (w *web) adminContracts(c *gin.Context) {
	found := auditContracts(c.Request.Context(), contractsCore, storage.Wrap(contractsDB))
	// The event directory sits on the same page: an extension is something you
	// call, an event is something that happens to you, and an author needs
	// both. See eventdir_web.go on why it was not shown anywhere until now.
	events, orphans := eventDirectory(contractsCore)
	w.render(c, "admin_contracts.html", map[string]any{
		"Title":    "Contracts",
		"Findings": found,
		"Events":   events,
		"Orphans":  orphans,
		// Shown even when empty, and that is the point: a page that only
		// appears when something is wrong cannot be used to check that nothing
		// is.
		"Checks": auditDescriptions(),
	})
}

// auditDescriptions is what the audit looks at, listed on the page so the empty
// state means something. "No findings" from an audit whose scope nobody can see
// is indistinguishable from an audit that checks nothing.
func auditDescriptions() []string {
	return []string{
		"Every payout kind an enabled reward promises has a registered handler.",
		"Every metric an enabled achievement counts has a registered source.",
		"No column in user_display is a constant where users has real data.",
	}
}
