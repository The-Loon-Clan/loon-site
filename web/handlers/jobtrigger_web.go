package handlers

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"

	"github.com/the-loon-clan/loon/schedule"
)

// Manual job runs, protected the way the scheduled ones already are.
//
// A job registered here has two ways to run and they were not equally safe.
// On its timer it goes through schedule.ServiceLoop, whose runTickProtected
// recovers a panic, writes it to the job's error stream and sets the job idle
// so the status pill does not stick on "Running". Pressed by an operator on
// the jobs page it went through `job.SetTrigger(func() { go run(...) })` --
// a bare goroutine, outside that protection, where a panic takes the PROCESS
// down rather than the run.
//
// So the same panic either logged neatly or killed the site, depending on
// which of the two paths reached it, and the fatal one was the path an
// operator takes deliberately while watching. Three jobs had it: the demo
// tracker swarm, the sitemap and the TV schedule.
//
// This mirrors what the loop does rather than inventing its own handling,
// because an operator reading the jobs page should not be able to tell which
// path a failure arrived by.
// It does NOT delegate to loon's newer schedule.SetTriggerAsync, and that is a
// decision about THIS host rather than a judgement on the framework. That
// helper runs the work through runTickProtected, which calls SetError and then
// SetIdle -- and SetIdle clears LastError deliberately, because that is what
// stops the pill sticking on "Running". The panic's durable home is meant to
// be PanicSink plus the OnRunEnd history. This host has no job-run history
// table at all, so on the framework's path a triggered panic would vanish from
// the jobs page a second after it happened, leaving only the log line. Calling
// SetError alone leaves the job reading "error" with the message on it until
// something runs it again, which is what an operator who just pressed the
// button needs to see.
//
// The sink is still called, so both paths report identically where it counts.
func triggerProtected(job *schedule.JobInfo, run func()) func() {
	return func() {
		go func() {
			defer func() {
				r := recover()
				if r == nil {
					return
				}
				msg := fmt.Sprintf("panic: %v\n%s", r, debug.Stack())
				job.SetError(msg)
				if schedule.PanicSink != nil {
					schedule.PanicSink(context.Background(), job.Name, errors.New(msg))
				}
			}()
			run()
		}()
	}
}
