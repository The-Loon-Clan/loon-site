package handlers

import (
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
func triggerProtected(job *schedule.JobInfo, run func()) func() {
	return func() {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					job.SetError(fmt.Sprintf("panic: %v\n%s", r, debug.Stack()))
				}
			}()
			run()
		}()
	}
}
