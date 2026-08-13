package handlers

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// The calendar — one month grid that renders whatever dated things a member
// has, from any number of independent sources.
//
// Built as a component rather than an attendance page because attendance is
// the least of what belongs on it: release follows, subscription expiries and
// temporary-boost windows are all "a thing, on a day", and each one arriving
// as its own page with its own grid is how five slightly different calendars
// end up in a codebase. The grid math, the month navigation and the markup are
// here once; a source contributes events and knows nothing about layout.
//
// The event model carries a RANGE, not a point. A boost that runs for a week
// is one event covering seven cells, not seven events — collapsing that to a
// point is the decision that makes durations unrepresentable later, and it
// costs nothing to allow now.

// calDateFmt is the civil-date key used throughout. Dates here are civil, not
// instants: "the 6th" is the same cell regardless of the reader's clock, and
// carrying zone-bearing timestamps into a grid is how an event lands one cell
// early for half the world.
const calDateFmt = "2006-01-02"

// calEvent is one dated thing. Start and End are inclusive civil dates; a
// single-day event leaves End zero.
type calEvent struct {
	Start time.Time
	End   time.Time
	// Kind groups and colours the event — it becomes a CSS modifier, so it is
	// a short slug ("claim", "release", "expiry", "boost").
	Kind  string
	Icon  string // sprite id, drawn in the cell
	Label string
	Href  string
}

// spans reports whether the event covers a civil day.
func (e calEvent) spans(day time.Time) bool {
	if day.Before(e.Start) {
		return false
	}
	end := e.End
	if end.IsZero() {
		end = e.Start
	}
	return !day.After(end)
}

// calSource contributes events for a window. It is given the window the page
// is about to draw so a source can scope its query instead of returning
// everything and being filtered — the difference between a bounded read and a
// full scan once any of these tables is large.
//
// A source that fails returns nothing. One broken contributor should cost its
// own events, not the page.
type calSource struct {
	Name string
	Fn   func(ctx context.Context, userID int64, from, to time.Time) []calEvent
}

// calDay is one cell.
type calDay struct {
	Date    time.Time
	Day     int
	Outside bool // padding from an adjacent month
	Today   bool
	Events  []calEvent
}

// calMonth is a rendered grid.
type calMonth struct {
	Year     int
	Month    time.Month
	Title    string
	Weekdays []string
	Weeks    [][]calDay
	Key      string // "YYYY-MM" of THIS month, so a caller can compare it
	Prev     string // "YYYY-MM" of the previous month
	Next     string
	Count    int // events shown, for the panel heading
}

// calWeekdays labels the columns. Sunday-first, matching the trackers this
// site is modelled on.
var calWeekdays = []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}

// civil truncates an instant to its UTC civil date.
func civil(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// buildCalendar lays out one month.
//
// The grid always starts on a Sunday and ends on a Saturday, so it holds 35 or
// 42 cells — never a ragged final row, which is what makes the cells a clean
// CSS grid rather than a flex wrap with a hole in it.
func buildCalendar(year int, month time.Month, today time.Time, events []calEvent) calMonth {
	first := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1)
	lead := int(first.Weekday()) // Sunday = 0
	start := first.AddDate(0, 0, -lead)

	cells := lead + last.Day()
	if r := cells % 7; r != 0 {
		cells += 7 - r
	}

	today = civil(today)
	m := calMonth{
		Year:     year,
		Month:    month,
		Title:    first.Format("January 2006"),
		Weekdays: calWeekdays,
		Key:      first.Format("2006-01"),
		Prev:     first.AddDate(0, -1, 0).Format("2006-01"),
		Next:     first.AddDate(0, 1, 0).Format("2006-01"),
	}

	var week []calDay
	for i := 0; i < cells; i++ {
		d := start.AddDate(0, 0, i)
		cell := calDay{
			Date:    d,
			Day:     d.Day(),
			Outside: d.Month() != month,
			// Today, but only when it belongs to the month on screen. Paging
			// to next month and finding a highlighted cell in the greyed-out
			// lead padding reads as "today is here" on a grid that is not
			// about today; the honest answer for another month is no mark.
			Today: d.Equal(today) && d.Month() == month && d.Year() == year,
		}
		for _, e := range events {
			if e.spans(d) {
				cell.Events = append(cell.Events, e)
			}
		}
		// Padding cells carry no events. A boost that runs across a month
		// boundary is real on both months' own grids; painting it into the
		// greyed-out tail of the previous one just reads as a rendering bug.
		if cell.Outside {
			cell.Events = nil
		}
		m.Count += len(cell.Events)
		week = append(week, cell)
		if len(week) == 7 {
			m.Weeks = append(m.Weeks, week)
			week = nil
		}
	}
	return m
}

// calMonthParam reads ?m=YYYY-MM, falling back to the current month.
//
// Clamped to a window around today: the query is a URL parameter, and without
// a bound "?m=999999-01" is a request to build a grid for the year one
// million. The range is wide enough for any real navigation and small enough
// that no source is ever asked for a window it cannot answer.
func calMonthParam(raw string, now time.Time) (int, time.Month) {
	now = civil(now)
	if raw == "" {
		return now.Year(), now.Month()
	}
	t, err := time.Parse("2006-01", raw)
	if err != nil {
		return now.Year(), now.Month()
	}
	if t.Before(now.AddDate(-20, 0, 0)) || t.After(now.AddDate(5, 0, 0)) {
		return now.Year(), now.Month()
	}
	return t.Year(), t.Month()
}

// calendarPage serves /calendar.
func (w *web) calendarPage(c *gin.Context) {
	ctx := c.Request.Context()
	u, ok := w.viewer(c)
	if !ok {
		return
	}

	now := time.Now().UTC()
	year, month := calMonthParam(c.Query("m"), now)

	// Sources see the whole drawn grid, padding included: an event that starts
	// in the previous month and runs into this one has to be returned to be
	// clipped, and a source given only the calendar month would never mention
	// it.
	first := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	from := first.AddDate(0, 0, -int(first.Weekday()))
	to := first.AddDate(0, 1, 6)

	var events []calEvent
	for _, s := range w.calSources {
		func() {
			// A source is ordinary code reading a database; a panic in one is
			// still only that source's failure.
			defer func() {
				if r := recover(); r != nil {
					w.log.Error("calendar source panicked", "source", s.Name, "err", r)
				}
			}()
			events = append(events, s.Fn(ctx, u.ID, from, to)...)
		}()
	}
	// Stable order within a cell: longest-running first, so a multi-day boost
	// sits above the single-day things that fall inside it.
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].End.Sub(events[i].Start) > events[j].End.Sub(events[j].Start)
	})

	data := map[string]any{
		"Title":     "Calendar",
		"Calendar":  buildCalendar(year, month, now, events),
		"ThisMonth": now.Format("2006-01"),
	}
	// The daily-reward card. It used to be the first thing on the home page,
	// which is the site's front page rather than yours; this is the page about
	// what you did on which day, and the grid below already plots the claims
	// this card takes. Absent when the plugin is not wired, which is why the
	// template guards on the key rather than assuming it.
	if card, ok := w.siteWidget(c, dailyRewardWidget); ok {
		data["DailyCard"] = card
	}
	w.render(c, "calendar.html", data)
}

// ── Sources ──────────────────────────────────────────────────────────────
//
// Each is a closure over what it needs and nothing else, which is what keeps
// them addable without touching the page.

// calAttendance marks the days of a member's CURRENT daily-reward streak.
//
// The plugin stores one row per member — last claim, streak length — not one
// row per claim, so the exact history is not recoverable and this does not
// pretend otherwise: it draws the run that is still live and says nothing
// about earlier ones. Deriving the days is sound because a streak is by
// definition consecutive, so the claimed days are the Streak days ending on
// LastClaim.
func (w *web) calAttendance() calSource {
	return calSource{
		Name: "attendance",
		Fn: func(ctx context.Context, userID int64, from, to time.Time) []calEvent {
			if w.dailyStatus == nil {
				return nil
			}
			st, err := w.dailyStatus(ctx, userID)
			if err != nil {
				return nil
			}
			run := st.LiveStreak(time.Now())
			if run <= 0 {
				return nil
			}
			end, err := time.Parse(calDateFmt, st.LastClaim)
			if err != nil {
				return nil
			}
			var out []calEvent
			for i := 0; i < run; i++ {
				d := end.AddDate(0, 0, -i)
				if d.Before(from) || d.After(to) {
					continue
				}
				out = append(out, calEvent{
					Start: d, Kind: "claim", Icon: "star",
					Label: "Daily reward claimed",
				})
			}
			return out
		},
	}
}

// calBookmarks puts the releases a member follows on the day they were posted.
//
// Bounded twice over: by the bookmark cap and again by the window, because a
// member with hundreds of saved releases would otherwise cost one lookup each
// to render a month in which most of them do not appear.
func (w *web) calBookmarks() calSource {
	return calSource{
		Name: "bookmarks",
		Fn: func(ctx context.Context, userID int64, from, to time.Time) []calEvent {
			if w.usenet == nil {
				return nil
			}
			var out []calEvent
			for _, id := range w.data.BookmarkedIDs(ctx, userID, calBookmarkScan) {
				detail, found, err := w.usenet.ReleaseByID(ctx, id)
				if err != nil || !found {
					continue // retention removed it; a saved pointer outlives its target
				}
				d := civil(detail.Release.Posted)
				if d.Before(from) || d.After(to) {
					continue
				}
				out = append(out, calEvent{
					Start: d, Kind: "release", Icon: "download",
					Label: detail.Release.Title,
					Href:  "/release/" + strconv.FormatInt(detail.Release.ID, 10),
				})
			}
			return out
		},
	}
}

// calBookmarkScan caps how many saved releases are considered for one month.
const calBookmarkScan = 200
