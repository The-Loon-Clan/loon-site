package handlers

import (
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Job log lines are mirrored to stdout so a running deployment is legible from
// `docker logs`. That breaks down when a job logs the SAME line in a loop.
//
// The case that motivated this: the usenet builder is kicked opportunistically
// by the backfill and the crawl — "ensure a build is running", and the plugin's
// own comments say the redundant kick is a no-op by design. But the no-op path
// logs, and the backfill kicks once per productive round, so while a builder
// catch-up pass holds its lock the log fills with one identical
// "build already running — skipping overlap" per second. 156 of 200 lines, at
// which point the log is not a record of anything.
//
// Collapsing consecutive duplicates fixes the symptom for EVERY job rather than
// that one line, and needs no plugin change. It is deliberately not a filter:
// nothing is dropped, the repeats are counted and reported, so a line repeating
// 500 times still says so.
type jobLogDedup struct {
	mu  sync.Mutex
	log *slog.Logger
	// PER JOB, not one global "previous line". The first version tracked a
	// single last-line and collapsed almost nothing in practice: jobs run
	// concurrently, so a single Backfill line landing between two Builder
	// repeats ended the Builder's run and started a fresh one. Live, that
	// turned ~300 lines into 61 instead of a handful. The jobs are a small
	// fixed set, so the map does not grow.
	seen   map[string]*jobLogState
	period time.Duration
	now    func() time.Time
}

type jobLogState struct {
	line  string
	count int       // repeats suppressed since the last emit
	last  time.Time // when this line was last printed
}

// dedupHeartbeat is how long a repeating line may stay silent. Without it a job
// stuck repeating one line would print nothing at all, and "no output" reads as
// "nothing happening" when the truth is the opposite.
const dedupHeartbeat = 2 * time.Minute

func newJobLogDedup(log *slog.Logger) *jobLogDedup {
	return &jobLogDedup{
		log:    log,
		seen:   map[string]*jobLogState{},
		period: dedupHeartbeat,
		now:    time.Now,
	}
}

// Log records one job line, collapsing an immediate repeat of the previous one.
// Safe for concurrent callers: schedule.LogSink is called from every job's
// goroutine.
func (d *jobLogDedup) Log(job, line string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	t := d.now()

	st := d.seen[job]
	if st == nil {
		st = &jobLogState{}
		d.seen[job] = st
	}

	if stampless(line) == stampless(st.line) {
		st.count++
		// Heartbeat: say it is still happening, and how often, without
		// printing every occurrence.
		if t.Sub(st.last) >= d.period {
			d.log.Info("job", "name", job, "line", line, "repeated", st.count)
			st.count, st.last = 0, t
		}
		return
	}

	// A different line for THIS job: report what the previous one did before
	// moving on, so the count is never silently discarded.
	d.flushLocked(job, st)
	st.line, st.count, st.last = line, 0, t
	d.log.Info("job", "name", job, "line", line)
}

// stampless drops the "[15:04:05] " that loon's scheduler prepends to every
// job line (schedule/registry.go). Comparing the raw strings is what the first
// version of this did, and it collapsed NOTHING in production: consecutive
// repeats differ by a second in the prefix, so they are never byte-equal. The
// full line is still what gets logged — only the comparison ignores the clock.
func stampless(line string) string {
	if len(line) > 11 && line[0] == '[' && line[9] == ']' && line[10] == ' ' {
		hhmmss := line[1:9]
		if len(strings.Trim(hhmmss, "0123456789:")) == 0 {
			return line[11:]
		}
	}
	return line
}

func (d *jobLogDedup) flushLocked(job string, st *jobLogState) {
	if st.count > 0 {
		d.log.Info("job", "name", job, "line", st.line, "repeated", st.count)
		st.count = 0
	}
}
