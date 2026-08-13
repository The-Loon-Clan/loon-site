package site

import (
	"testing"
	"time"
)

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// The grid contract: always whole weeks, Sunday to Saturday, with the month's
// own days in the middle and padding either side. Everything the CSS assumes
// (a fixed 7-column table, no ragged last row) rests on this, and the cases
// that break it are the ones nobody hits until a particular month arrives —
// a month starting ON a Sunday needs no lead padding at all, and a 31-day
// month starting late in the week is the only shape that needs six rows.
func TestBuildCalendarAlwaysLaysOutWholeWeeks(t *testing.T) {
	for _, tc := range []struct {
		name      string
		year      int
		month     time.Month
		wantWeeks int
		wantFirst time.Time // top-left cell
		wantLast  time.Time // bottom-right cell
	}{
		{
			// Aug 2026 starts on a Saturday: one lead cell, 31 days, six rows.
			"month starting on a Saturday", 2026, time.August, 6,
			day(2026, time.July, 26), day(2026, time.September, 5),
		},
		{
			// Feb 2026 starts on a Sunday and has 28 days — exactly four rows,
			// the only shape with no padding anywhere.
			"28-day month starting on a Sunday", 2026, time.February, 4,
			day(2026, time.February, 1), day(2026, time.February, 28),
		},
		{
			// Leap day has to land in the grid, not fall off the end.
			"leap February", 2024, time.February, 5,
			day(2024, time.January, 28), day(2024, time.March, 2),
		},
		{
			// December rolls the year over on the trailing padding.
			"December rolls into next year", 2026, time.December, 5,
			day(2026, time.November, 29), day(2027, time.January, 2),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := buildCalendar(tc.year, tc.month, day(tc.year, tc.month, 15), nil)

			if len(m.Weeks) != tc.wantWeeks {
				t.Fatalf("weeks = %d, want %d", len(m.Weeks), tc.wantWeeks)
			}
			for i, w := range m.Weeks {
				if len(w) != 7 {
					t.Fatalf("week %d has %d days, want 7", i, len(w))
				}
			}
			first := m.Weeks[0][0]
			lastWeek := m.Weeks[len(m.Weeks)-1]
			last := lastWeek[6]
			if !first.Date.Equal(tc.wantFirst) {
				t.Errorf("first cell = %s, want %s",
					first.Date.Format(calDateFmt), tc.wantFirst.Format(calDateFmt))
			}
			if !last.Date.Equal(tc.wantLast) {
				t.Errorf("last cell = %s, want %s",
					last.Date.Format(calDateFmt), tc.wantLast.Format(calDateFmt))
			}
			if first.Date.Weekday() != time.Sunday || last.Date.Weekday() != time.Saturday {
				t.Errorf("grid runs %s..%s, want Sunday..Saturday",
					first.Date.Weekday(), last.Date.Weekday())
			}

			// Every day of the month appears exactly once and is not marked
			// outside; every other cell is.
			seen := map[int]int{}
			for _, w := range m.Weeks {
				for _, d := range w {
					inMonth := d.Date.Month() == tc.month && d.Date.Year() == tc.year
					if inMonth {
						seen[d.Day]++
					}
					if d.Outside == inMonth {
						t.Errorf("%s: Outside=%v, in-month=%v",
							d.Date.Format(calDateFmt), d.Outside, inMonth)
					}
				}
			}
			want := day(tc.year, tc.month, 1).AddDate(0, 1, -1).Day()
			if len(seen) != want {
				t.Errorf("saw %d distinct days of the month, want %d", len(seen), want)
			}
			for d, n := range seen {
				if n != 1 {
					t.Errorf("day %d appears %d times", d, n)
				}
			}
		})
	}
}

// A range event has to paint every day it covers, and stop exactly at its end.
// Off-by-one here is invisible on a one-day event and wrong on every boost.
func TestBuildCalendarPaintsARangeInclusively(t *testing.T) {
	ev := calEvent{
		Start: day(2026, time.August, 10),
		End:   day(2026, time.August, 14),
		Kind:  "boost",
	}
	m := buildCalendar(2026, time.August, day(2026, time.August, 6), []calEvent{ev})

	var painted []int
	for _, w := range m.Weeks {
		for _, d := range w {
			if len(d.Events) > 0 {
				painted = append(painted, d.Day)
			}
		}
	}
	if len(painted) != 5 {
		t.Fatalf("painted %v, want the 5 days 10..14", painted)
	}
	for i, d := range painted {
		if want := 10 + i; d != want {
			t.Errorf("painted[%d] = %d, want %d", i, d, want)
		}
	}
	if m.Count != 5 {
		t.Errorf("Count = %d, want 5", m.Count)
	}
}

// An event dated into the padding must not draw there. The greyed-out tail of
// an adjacent month is not that month's grid, and a chip in it reads as a
// rendering fault rather than as information.
func TestBuildCalendarKeepsEventsOffThePadding(t *testing.T) {
	// 26 July 2026 is the top-left padding cell of the August grid.
	m := buildCalendar(2026, time.August, day(2026, time.August, 6),
		[]calEvent{{Start: day(2026, time.July, 26), Kind: "claim"}})

	if m.Count != 0 {
		t.Fatalf("Count = %d, want 0 — the event fell on a padding cell", m.Count)
	}
	if got := m.Weeks[0][0]; len(got.Events) != 0 {
		t.Errorf("padding cell %s carries %d events",
			got.Date.Format(calDateFmt), len(got.Events))
	}
}

// Exactly one cell is today, and only when today is in the month on screen.
func TestBuildCalendarMarksTodayOnlyInItsOwnMonth(t *testing.T) {
	today := day(2026, time.August, 6)

	count := func(m calMonth) int {
		n := 0
		for _, w := range m.Weeks {
			for _, d := range w {
				if d.Today {
					n++
				}
			}
		}
		return n
	}

	if got := count(buildCalendar(2026, time.August, today, nil)); got != 1 {
		t.Errorf("August: %d cells marked today, want 1", got)
	}
	// 31 August 2026 falls in SEPTEMBER's lead padding. Viewing September is
	// not viewing today, so nothing on that grid is marked.
	inPadding := day(2026, time.August, 31)
	sept := buildCalendar(2026, time.September, inPadding, nil)
	if got := count(sept); got != 0 {
		t.Errorf("September: %d cells marked today, want 0 — it is padding", got)
	}
	// The same date on its OWN month's grid is still today.
	if got := count(buildCalendar(2026, time.August, inPadding, nil)); got != 1 {
		t.Errorf("August: %d cells marked today, want 1", got)
	}
	if got := count(buildCalendar(2026, time.November, today, nil)); got != 0 {
		t.Errorf("November: %d cells marked today, want 0", got)
	}
}

// The month parameter comes off a URL, so its job is to refuse nonsense rather
// than pass it to time.Date and build a grid for the year one million.
func TestCalMonthParamRejectsOutOfRange(t *testing.T) {
	now := day(2026, time.August, 6)

	for _, tc := range []struct {
		name  string
		raw   string
		yr    int
		month time.Month
	}{
		{"empty falls back to now", "", 2026, time.August},
		{"a real month is honoured", "2025-03", 2025, time.March},
		{"next month is honoured", "2026-09", 2026, time.September},
		{"garbage falls back", "not-a-month", 2026, time.August},
		{"absurd year falls back", "999999-01", 2026, time.August},
		{"far past falls back", "1970-01", 2026, time.August},
		{"far future falls back", "2099-01", 2026, time.August},
		// A day-precision date is not the format; parsing must fail closed.
		{"wrong format falls back", "2026-08-06", 2026, time.August},
	} {
		t.Run(tc.name, func(t *testing.T) {
			y, m := calMonthParam(tc.raw, now)
			if y != tc.yr || m != tc.month {
				t.Errorf("calMonthParam(%q) = %d-%s, want %d-%s", tc.raw, y, m, tc.yr, tc.month)
			}
		})
	}
}

// spans is the whole event/day relationship. A zero End means one day — the
// case every single-day source relies on.
func TestEventSpans(t *testing.T) {
	single := calEvent{Start: day(2026, time.August, 10)}
	if !single.spans(day(2026, time.August, 10)) {
		t.Error("a zero End must cover its own start day")
	}
	if single.spans(day(2026, time.August, 11)) {
		t.Error("a zero End must not cover the day after")
	}

	rng := calEvent{Start: day(2026, time.August, 10), End: day(2026, time.August, 12)}
	for _, tc := range []struct {
		d    time.Time
		want bool
	}{
		{day(2026, time.August, 9), false},
		{day(2026, time.August, 10), true},
		{day(2026, time.August, 11), true},
		{day(2026, time.August, 12), true},
		{day(2026, time.August, 13), false},
	} {
		if got := rng.spans(tc.d); got != tc.want {
			t.Errorf("spans(%s) = %v, want %v", tc.d.Format(calDateFmt), got, tc.want)
		}
	}
}
