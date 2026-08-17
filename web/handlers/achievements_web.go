package handlers

import (
	"sort"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/achievements"
)

// Achievements — docs/MOCKS.md M2, the last mock standing.
//
// The page shape is UNIT3D's: Unlocked and Pending down the main column, a
// Statistics panel in the aside. What was missing here was never the layout —
// it was that a host could not ASK the rewards plugin where a member stood,
// so the profile card had a hardcoded em dash. `rewards.achievements` is that
// question; this file is the page that asks it.
//
// LOCKED achievements split two ways, and the line between them is progress.
// One WITH progress is work underway — it renders in the In Progress panel
// with a bar, because this page became the only home for those bars when the
// profile stopped showing the rewards widget (profile.html keeps one
// achievements panel, and the in-progress half lives here). One with NO
// progress is pure absence, and stays a count: UNIT3D lists all of them, and
// on a site with 53 that is a wall of things you do not have. The original
// note here said "revisit if"; the revisit happened when the bars lost their
// other home.

// achievementView is one badge as the template needs it.
type achievementView struct {
	Name string
	Slug string
	Icon string
	// Image is the operator-uploaded badge art URL (rewards migration 006),
	// which wins over Icon when set — and is blanked by the builder for
	// anything not yet earned, so hidden badges cannot leak their art.
	Image string
	When  string
	State achievements.AchievementState
	// The in-progress figures. Percent is computed here rather than in the
	// template because html/template arithmetic on two int64s is where a page
	// silently truncates — and clamped to 99: a bar reading full beside a
	// badge that has not unlocked is a contradiction the member will report.
	Progress  int64
	Threshold int64
	Percent   int
}

// achievementIcon picks the sprite for a state.
//
// Host-side on purpose: the plugin owns whether a badge is held, this owns
// what that looks like. An icon column on the plugin would be one the host
// could not override.
func achievementIcon(s achievements.AchievementState) string {
	switch s {
	case achievements.AchievementUnlocked:
		return "verified"
	case achievements.AchievementPending:
		return "clock"
	}
	return "lock"
}

// achievementsPage serves /achievements.
func (w *web) achievementsPage(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
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

	unlocked, pending, inProgress := []achievementView{}, []achievementView{}, []achievementView{}
	for _, a := range list {
		v := achievementView{
			Name:      w.localizedAchName(c, a),
			Slug:      a.Slug,
			Icon:      achievementIcon(a.State),
			Image:     a.ImagePath,
			State:     a.State,
			Progress:  a.Progress,
			Threshold: a.Threshold,
		}
		// The operator's look applies to what is EARNED. Locked and pending
		// keep the state icons — a padlock says "not yet" in a way custom art
		// cannot, and a hidden-until-earned badge must not leak its image.
		if a.State != achievements.AchievementUnlocked {
			v.Image = ""
		} else if a.Icon != "" {
			v.Icon = a.Icon
		}
		if !a.EarnedAt.IsZero() {
			v.When = a.EarnedAt.Format("2 Jan 2006")
		}
		switch a.State {
		case achievements.AchievementUnlocked:
			unlocked = append(unlocked, v)
		case achievements.AchievementPending:
			pending = append(pending, v)
		default:
			if a.Progress > 0 && a.Threshold > 0 {
				v.Percent = int(a.Progress * 100 / a.Threshold)
				if v.Percent > 99 {
					v.Percent = 99
				}
				inProgress = append(inProgress, v)
			}
		}
	}
	// Nearest completion first: the panel is a to-do list, and the item a
	// member is three posts from matters more than the one they are 22 from.
	sort.SliceStable(inProgress, func(i, j int) bool {
		return inProgress[i].Percent > inProgress[j].Percent
	})

	nUnlocked, nPending, nLocked := achievements.AchievementCounts(list)
	data["Unlocked"] = unlocked
	data["Pending"] = pending
	data["InProgress"] = inProgress
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
	unlocked, _, _ := achievements.AchievementCounts(list)
	sum := achievementSummary{Unlocked: unlocked, Total: len(list)}

	// Most recently earned first. Only unlocked ones: a profile card is a
	// display of what someone HAS.
	var earned []achievements.Achievement
	for _, a := range list {
		if a.State == achievements.AchievementUnlocked && !a.EarnedAt.IsZero() {
			earned = append(earned, a)
		}
	}
	sortByEarnedDesc(earned)
	for i, a := range earned {
		if i >= n {
			break
		}
		sum.Recent = append(sum.Recent, achievementView{
			Name: w.localizedAchName(c, a), Slug: a.Slug,
			Icon: achievementIcon(a.State),
			When: a.EarnedAt.Format("2 Jan 2006"),
		})
	}
	return sum, true
}

// localizedAchName resolves a badge's display title for this viewer: the
// message-catalogue slug when the definition names one, the plain Name
// otherwise. Same resolution the plugin's own profile widget does through the
// achievements.l10n.resolve seam — one catalogue, two renderers.
func (w *web) localizedAchName(c *gin.Context, a achievements.Achievement) string {
	if a.TitleSlug != "" {
		if t, ok := w.resolveI18n(c, a.TitleSlug); ok && t != "" {
			return t
		}
	}
	return a.Name
}

// sortByEarnedDesc orders newest first. An insertion sort because the input is
// one member's achievements — tens of items, not thousands.
func sortByEarnedDesc(as []achievements.Achievement) {
	for i := 1; i < len(as); i++ {
		for j := i; j > 0 && as[j].EarnedAt.After(as[j-1].EarnedAt); j-- {
			as[j], as[j-1] = as[j-1], as[j]
		}
	}
}
