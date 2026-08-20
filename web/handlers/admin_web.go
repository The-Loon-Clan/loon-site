package handlers

import (
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-site/internal/config"
	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon/schedule"
)

// The demo renders /admin/jobs and /admin/plugins in its own base layout (nav +
// footer) instead of loon's self-contained inline pages, so every admin page
// looks consistent. The DATA still comes from loon (schedule snapshots + the
// plugin runtime); this is just the host rendering its own chrome.

type jobRow struct {
	Name        string
	Description string
	Status      string
	LastRun     string
	NextRun     string
	Interval    string
	Activity    string
	Runs        int64
	Triggerable bool
	Paused      bool
	HasConfig   bool
}

func (w *web) adminJobs(c *gin.Context) {
	groups := groupJobs(schedule.GetAllSnapshots())
	// Plugin overrides: a SlotJobsWidget whose Anchor matches a group name
	// replaces that group's default table ("list the basics, allow a custom
	// override"). Render errors fall back to the default.
	for i := range groups {
		if v, ok := w.jobsWidgets[groups[i].Name]; ok {
			if frag, err := v.Render(c); err == nil {
				groups[i].Override = frag
			} else {
				w.log.Error("jobs widget", "anchor", groups[i].Name, "err", err)
			}
		}
	}
	w.render(c, "admin_jobs.html", map[string]any{"Title": "Jobs", "Groups": groups})
}

type jobGroup struct {
	Name     string
	Jobs     []jobRow
	Running  int
	Override template.HTML // plugin-supplied card body (SlotJobsWidget)
}

// groupJobs buckets snapshots by the leading token of the job name, so
// "NZB Builder/Tag Fill/Prune" collapse under "NZB", "Backup" stands alone, etc.
func groupJobs(snaps []schedule.JobSnapshot) []jobGroup {
	idx := map[string]int{}
	var groups []jobGroup
	for _, s := range snaps {
		g := jobGroupName(s.Name)
		i, ok := idx[g]
		if !ok {
			i = len(groups)
			idx[g] = i
			groups = append(groups, jobGroup{Name: g})
		}
		groups[i].Jobs = append(groups[i].Jobs, toJobRow(s))
		if s.Status == "running" || s.ElapsedSecs > 0 {
			groups[i].Running++
		}
	}
	return groups
}

func jobGroupName(name string) string {
	for i, r := range name {
		if r == ' ' || r == ':' {
			return name[:i]
		}
	}
	return name
}

func toJobRow(s schedule.JobSnapshot) jobRow {
	r := jobRow{
		Name: s.Name, Description: s.Description, Status: s.Status,
		Runs: s.RunCount, Triggerable: s.Triggerable, Paused: s.Paused,
		HasConfig: s.HasConfig,
		LastRun:   fmtJobTime(s.LastRun), NextRun: fmtJobTime(s.NextRun), Interval: "—",
	}
	if s.IntervalMin > 0 {
		r.Interval = fmt.Sprintf("%dm", s.IntervalMin)
	}
	if s.LastError != "" {
		r.Activity = s.LastError
	} else if len(s.Logs) > 0 {
		r.Activity = s.Logs[len(s.Logs)-1]
	}
	return r
}

func fmtJobTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("2006-01-02 15:04:05")
}

// adminJobsControl applies a manual control and redirects back (loon's
// JobsControlHandler returns JSON, which is wrong for a browser form).
func (w *web) adminJobsControl(c *gin.Context) {
	in, _ := readJobControlInput(c)
	name := in.Name
	switch in.Action {
	case "trigger":
		// "Run now" has to reach the process that OWNS the job, which is not
		// necessarily this one.
		//
		// This used to call TriggerJob unconditionally, and that was correct
		// while one process did everything. Split the roles and it quietly
		// stops being: the admin page lives in the web process, so a local
		// trigger either runs the job THERE — the work landing in the process
		// meant to be answering requests — or, for a worker-only plugin whose
		// job was never registered here, does nothing at all while still
		// redirecting back with every appearance of success.
		//
		// So a process that does not run jobs enqueues instead, and the worker's
		// poller drains it. Found by splitting the roles and pressing the
		// button: the redirect said 303 and the guestbook job ran in the web
		// process.
		if config.RunsJobs() {
			schedule.TriggerJob(name)
		} else if w.jobQueue != nil {
			if err := w.jobQueue.Request(c.Request.Context(), name); err != nil {
				w.log.Error("enqueue job trigger", "job", name, "err", err)
			} else {
				w.log.Info("job trigger enqueued for the worker", "job", name)
			}
		} else {
			w.log.Warn("run now requested with no worker queue available", "job", name)
		}
	case "pause":
		schedule.PauseJob(name)
	case "resume":
		schedule.ResumeJob(name)
	case "stop":
		schedule.StopJob(name)
	}
	c.Redirect(http.StatusSeeOther, "/admin/jobs")
}

type pluginRow struct {
	Name        string
	Version     string
	Description string
	Requires    string
	// Flavours is which half of the site this plugin belongs to, or empty for
	// the majority that belong to both.
	Flavours string
	// Running says whether it actually booted. A plugin compiled into this
	// binary and skipped by the site flavour is the whole point of the page
	// showing it at all — "it is not here" and "it is switched off" look
	// identical from the outside, and only one of them is fixed by a setting.
	Running bool
}

func (w *web) adminPlugins(c *gin.Context) {
	var rows []pluginRow
	booted := map[string]bool{}
	if w.rt != nil {
		for _, p := range w.rt.Plugins() {
			md := p.Metadata()
			booted[md.Name] = true
			rows = append(rows, pluginRow{
				Name: md.Name, Version: md.Version,
				Description: md.Description, Requires: strings.Join(md.Requires, ", "),
				Flavours: strings.Join(md.Flavours, ", "), Running: true,
			})
		}
	}
	// Everything compiled in but NOT booted. Named only — the metadata lives
	// on an instance and skipped plugins were never constructed, so a name is
	// genuinely all there is. That is still the useful half: an operator
	// looking for the tracker's admin page needs to know it is off rather than
	// missing.
	var skipped []pluginRow
	for _, name := range core.RegisteredNames() {
		if !booted[name] {
			skipped = append(skipped, pluginRow{Name: name})
		}
	}
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].Name < skipped[j].Name })

	w.render(c, "admin_plugins.html", map[string]any{
		"Title":   "Plugins",
		"Plugins": rows,
		"Skipped": skipped,
		// What the site is, so the page can say WHY something is off rather
		// than only that it is.
		"Flavour": siteFlavour(),
	})
}
