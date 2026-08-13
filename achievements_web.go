package site

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/rewards"
)

// Achievements — docs/MOCKS.md M2, the last mock standing.
//
// The page shape is UNIT3D's: Unlocked and Pending down the main column, a
// Statistics panel in the aside. What was missing here was never the layout —
// it was that a host could not ASK the rewards plugin where a member stood,
// so the profile card had a hardcoded em dash. `rewards.achievements` is that
// question; this file is the page that asks it.
//
// LOCKED achievements are counted but NOT listed. UNIT3D lists them, and on a
// site with 53 of them that is a wall of things you do not have; the count in
// the statistics panel says the same thing without the page being mostly
// absence. Revisit if the catalogue ever gets descriptions worth browsing.

// achievementView is one badge as the template needs it.
type achievementView struct {
	Name  string
	Slug  string
	Icon  string
	When  string
	State rewards.AchievementState
}

// achievementIcon picks the sprite for a state.
//
// Host-side on purpose: the plugin owns whether a badge is held, this owns
// what that looks like. An icon column on the plugin would be one the host
// could not override.
func achievementIcon(s rewards.AchievementState) string {
	switch s {
	case rewards.AchievementUnlocked:
		return "verified"
	case rewards.AchievementPending:
		return "clock"
	}
	return "lock"
}

// achievementsPage serves /achievements.
func (w *web) achievementsPage(c *gin.Context) {
	u, ok := w.currentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}

	data := map[string]any{
		"Title":    "Achievements",
		"Unlocked": []achievementView{},
		"Pending":  []achievementView{},
	}

	// No plugin, no data. The page still renders — with an empty state rather
	// than a 404 — because the account nav links here unconditionally and a
	// dead link reads worse than an honest "nothing yet".
	if w.achievements == nil {
		data["Unavailable"] = true
		w.render(c, "achievements.html", data)
		return
	}

	list, err := w.achievements(c.Request.Context(), u.ID)
	if err != nil {
		w.log.Error("read achievements", "user", u.ID, "err", err)
		data["Unavailable"] = true
		w.render(c, "achievements.html", data)
		return
	}

	unlocked, pending := []achievementView{}, []achievementView{}
	for _, a := range list {
		v := achievementView{
			Name:  a.Name,
			Slug:  a.Slug,
			Icon:  achievementIcon(a.State),
			State: a.State,
		}
		if !a.EarnedAt.IsZero() {
			v.When = a.EarnedAt.Format("2 Jan 2006")
		}
		switch a.State {
		case rewards.AchievementUnlocked:
			unlocked = append(unlocked, v)
		case rewards.AchievementPending:
			pending = append(pending, v)
		}
	}

	nUnlocked, nPending, nLocked := rewards.AchievementCounts(list)
	data["Unlocked"] = unlocked
	data["Pending"] = pending
	data["CountUnlocked"] = nUnlocked
	data["CountPending"] = nPending
	data["CountLocked"] = nLocked
	data["CountTotal"] = len(list)
	// A catalogue with nothing in it is a different message from a member who
	// has earned nothing, and the page says so rather than showing two empty
	// panels and letting the reader guess which.
	data["NoCatalogue"] = len(list) == 0

	w.render(c, "achievements.html", data)
}

// profileAchievements is how many badges the profile card shows. Enough to
// read as a row, few enough that a decorated member does not push the rest of
// the profile off the screen.
const profileAchievements = 6

// achievementSummary is the profile card's version: how many, and the most
// recent few. Returned to the profile page rather than the page above, so a
// member's standing shows where UNIT3D shows it too.
type achievementSummary struct {
	Unlocked int
	Total    int
	Recent   []achievementView
}

// recentAchievements reads the summary for the profile card. Best effort: a
// failure here must not take down a profile, so it reports ok=false and the
// card falls back to saying nothing.
func (w *web) recentAchievements(c *gin.Context, userID int64, n int) (achievementSummary, bool) {
	if w.achievements == nil || userID <= 0 {
		return achievementSummary{}, false
	}
	list, err := w.achievements(c.Request.Context(), userID)
	if err != nil {
		return achievementSummary{}, false
	}
	unlocked, _, _ := rewards.AchievementCounts(list)
	sum := achievementSummary{Unlocked: unlocked, Total: len(list)}

	// Most recently earned first. Only unlocked ones: a profile card is a
	// display of what someone HAS.
	var earned []rewards.Achievement
	for _, a := range list {
		if a.State == rewards.AchievementUnlocked && !a.EarnedAt.IsZero() {
			earned = append(earned, a)
		}
	}
	sortByEarnedDesc(earned)
	for i, a := range earned {
		if i >= n {
			break
		}
		sum.Recent = append(sum.Recent, achievementView{
			Name: a.Name, Slug: a.Slug,
			Icon: achievementIcon(a.State),
			When: a.EarnedAt.Format("2 Jan 2006"),
		})
	}
	return sum, true
}

// sortByEarnedDesc orders newest first. An insertion sort because the input is
// one member's achievements — tens of items, not thousands.
func sortByEarnedDesc(as []rewards.Achievement) {
	for i := 1; i < len(as); i++ {
		for j := i; j > 0 && as[j].EarnedAt.After(as[j-1].EarnedAt); j-- {
			as[j], as[j-1] = as[j-1], as[j]
		}
	}
}
