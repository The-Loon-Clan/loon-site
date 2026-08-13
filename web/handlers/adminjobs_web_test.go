package handlers

import (
	"strings"
	"testing"
	"time"

	"github.com/the-loon-clan/loon/schedule"
)

// The jobs page is where an operator looks when they suspect something has
// stopped running. Everything it shows is derived here, and none of it was
// tested — so the page could be confidently wrong about which jobs are alive,
// which is the one question it exists to answer.

func TestAJobThatHasNeverRunSaysSoInsteadOfYearOne(t *testing.T) {
	// The bug this is really about: time.Time's zero value formats perfectly
	// happily as "0001-01-01 00:00:00", so a job that has NEVER run would
	// appear in the table with a timestamp — an operator reading that has been
	// told the job ran, in the year 1, which reads as a rendering quirk rather
	// than as "this has never fired".
	if got := fmtJobTime(time.Time{}); got != "—" {
		t.Errorf("a never-run job shows %q, want an em dash", got)
	}
	if strings.Contains(fmtJobTime(time.Time{}), "0001") {
		t.Error("the zero time leaked into the page as a date")
	}
}

func TestARealRunTimeIsShownToTheSecond(t *testing.T) {
	// Seconds matter here: two runs a minute apart are the normal case when
	// somebody is triggering a job by hand to see whether it works.
	when := time.Date(2026, 8, 13, 19, 30, 56, 0, time.UTC)
	if got := fmtJobTime(when); got != "2026-08-13 19:30:56" {
		t.Errorf("fmtJobTime = %q, want 2026-08-13 19:30:56", got)
	}
}

func TestAJobWithNoIntervalShowsADashNotZero(t *testing.T) {
	// A manual-only job has no interval. Rendering "0m" would say it runs every
	// zero minutes, which is both wrong and alarming.
	row := toJobRow(schedule.JobSnapshot{Name: "Backup"})
	if row.Interval != "—" {
		t.Errorf("a job with no interval shows %q, want an em dash", row.Interval)
	}

	row = toJobRow(schedule.JobSnapshot{Name: "Backup", IntervalMin: 15})
	if row.Interval != "15m" {
		t.Errorf("interval = %q, want 15m", row.Interval)
	}
}

func TestTheLastErrorWinsOverTheLastLogLine(t *testing.T) {
	// A job that failed and then logged something ordinary must still show the
	// failure. Showing the log line instead is how a broken job looks healthy
	// on the one page somebody checks.
	row := toJobRow(schedule.JobSnapshot{
		Name:      "Tag Fill",
		LastError: "connection refused",
		Logs:      []string{"[19:00] started", "[19:01] 0 rows"},
	})
	if row.Activity != "connection refused" {
		t.Errorf("activity = %q, want the error", row.Activity)
	}
}

func TestWithNoErrorTheMostRecentLogLineIsShown(t *testing.T) {
	// The LAST line, not the first: the first is always "started", which tells
	// an operator nothing they did not already know.
	row := toJobRow(schedule.JobSnapshot{
		Name: "Tag Fill",
		Logs: []string{"[19:00] started", "[19:01] 12 rows", "[19:02] done"},
	})
	if row.Activity != "[19:02] done" {
		t.Errorf("activity = %q, want the most recent line", row.Activity)
	}
}

func TestJobNamesGroupOnTheirLeadingToken(t *testing.T) {
	for in, want := range map[string]string{
		"NZB Builder":       "NZB",
		"NZB Prune":         "NZB",
		"guestbook: stats":  "guestbook",
		"Backup":            "Backup",
		"Cover Art Fetcher": "Cover",
	} {
		if got := jobGroupName(in); got != want {
			t.Errorf("jobGroupName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGroupingKeepsJobsTogetherAndInOrder(t *testing.T) {
	// Order is the declared one, not alphabetical: the scheduler lists jobs in
	// the order they were registered, and reordering them on screen makes the
	// page disagree with every other view of the same list.
	groups := groupJobs([]schedule.JobSnapshot{
		{Name: "NZB Builder"},
		{Name: "Backup"},
		{Name: "NZB Prune"},
		{Name: "guestbook: stats"},
		{Name: "NZB Tag Fill"},
	})

	if len(groups) != 3 {
		t.Fatalf("got %d groups, want 3 (NZB, Backup, guestbook)", len(groups))
	}
	if groups[0].Name != "NZB" || groups[1].Name != "Backup" || groups[2].Name != "guestbook" {
		t.Fatalf("groups came out as %s/%s/%s, want NZB/Backup/guestbook",
			groups[0].Name, groups[1].Name, groups[2].Name)
	}
	if len(groups[0].Jobs) != 3 {
		t.Errorf("the NZB group has %d jobs, want 3", len(groups[0].Jobs))
	}
	for i, want := range []string{"NZB Builder", "NZB Prune", "NZB Tag Fill"} {
		if groups[0].Jobs[i].Name != want {
			t.Errorf("NZB job %d = %q, want %q", i, groups[0].Jobs[i].Name, want)
		}
	}
}

func TestTheRunningCountCountsOnlyWhatIsRunning(t *testing.T) {
	// The badge an operator reads as "something is happening right now". A job
	// that has finished must not be counted, or the page permanently claims
	// work is in progress and the badge stops meaning anything.
	groups := groupJobs([]schedule.JobSnapshot{
		{Name: "NZB Builder", Status: "running", ElapsedSecs: 4},
		{Name: "NZB Prune", Status: "idle", LastRun: time.Now(), RunCount: 90},
		{Name: "NZB Tag Fill", Status: "idle"},
	})

	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	if groups[0].Running != 1 {
		t.Errorf("Running = %d, want 1 — a finished job is being counted as "+
			"in progress, and the badge is then always on", groups[0].Running)
	}
}

func TestAnIdleJobWithElapsedTimeIsTreatedAsRunning(t *testing.T) {
	// ElapsedSecs is documented upstream as ">0 only while running", and
	// groupJobs uses it as a second opinion in case Status has not caught up.
	//
	// Pinned deliberately rather than left implicit: if that field ever came to
	// mean "how long the last run took", every job that had ever run would count
	// as running and the badge would read as permanent activity. This test says
	// which meaning the page depends on.
	groups := groupJobs([]schedule.JobSnapshot{
		{Name: "Backup", Status: "", ElapsedSecs: 12},
	})
	if groups[0].Running != 1 {
		t.Error("a job reporting elapsed time is not counted as running")
	}
}

func TestNoJobsIsNoGroupsRatherThanOneEmptyOne(t *testing.T) {
	// The template ranges over groups and draws a card per group. One empty
	// group would render a headed card with nothing in it.
	if got := groupJobs(nil); len(got) != 0 {
		t.Errorf("groupJobs(nil) returned %d groups, want none", len(got))
	}
}

func TestTheRowCarriesTheControlsTheTemplateNeeds(t *testing.T) {
	// Triggerable and Paused drive which buttons the page draws. Dropping them
	// silently removes an operator's ability to run a job by hand, and the page
	// still looks complete.
	row := toJobRow(schedule.JobSnapshot{
		Name: "Backup", Description: "nightly", Status: "idle",
		RunCount: 7, Triggerable: true, Paused: true, HasConfig: true,
	})
	if !row.Triggerable || !row.Paused || !row.HasConfig {
		t.Errorf("controls lost in translation: %+v", row)
	}
	if row.Runs != 7 || row.Description != "nightly" || row.Status != "idle" {
		t.Errorf("row = %+v, want the snapshot's own values", row)
	}
}
