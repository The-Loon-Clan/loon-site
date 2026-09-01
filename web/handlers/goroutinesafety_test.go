package handlers

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/the-loon-clan/loon/schedule"
)

// Every `go func` in this package is an unrecovered panic away from taking the
// whole process down: gin's Recovery middleware wraps the goroutine serving a
// request, and these are not those. The framework draws the same line for its
// own background work -- loon's schedule.runTickProtected wraps every tick fn
// -- so a host spawning its own goroutines outside that protection is opting
// out of it, and should do so on purpose.
//
// This test does not demand a recover() in all of them. It demands that the
// set stays the size it was audited at, so a new one has to be looked at
// rather than merged unnoticed. It counts with the AST rather than by
// grepping "go func(", which is how the first pass of this audit missed nine
// of them: `go someCall(...)` and a `go` inside a closure argument are both
// goroutines and neither matches that pattern.
//
// The audited thirteen:
//
//	avatarsweep       filesystem deletion + DB on a forever ticker -- RECOVERED
//	jobtrigger        the one shared manual-run spawn -- RECOVERED, and it
//	                  replaced three bare ones (demo swarm, sitemap, TV schedule)
//	demoseedtracker   ServiceLoop, protected by the framework's runTickProtected
//	sitemap           ServiceLoop, same
//	tvschedule        ServiceLoop, same
//	seedpoints        ServiceLoop, same; its manual run goes through
//	                  triggerProtected like the other three
//	main              x3: StartRefresh / StartPoller / StartReporter, baseline-owned
//	presence          one-shot UPDATE, guarded by db.Valid() before the spawn
//	serve_wiring      x3: ListenAndServe, plus backfillCovers and runLocalLinks
func TestGoroutineCountIsAudited(t *testing.T) {
	const audited = 13

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	found := map[string]int{}
	total := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if _, ok := n.(*ast.GoStmt); ok {
				found[name]++
				total++
			}
			return true
		})
	}
	if total != audited {
		t.Errorf("this package spawns %d goroutines, audited at %d: %v\n"+
			"A new one needs a decision: it either cannot panic, or it needs a "+
			"recover, because an unrecovered panic here kills the site rather "+
			"than the request. Update the count and the note above once it has one.",
			total, audited, found)
	}
}

// The avatar sweep is the one that runs real code on a forever ticker, so its
// recover is not decoration. This pins the shape rather than the behaviour --
// the sweep closure has no seam to inject a panic through, and adding one
// purely for a test would be more machinery than the six lines it guards.
func TestAvatarSweepRecovers(t *testing.T) {
	b, err := os.ReadFile("avatarsweep_web.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "sweep := func() {")
	if i < 0 {
		t.Fatal("the avatar sweep closure has moved; re-check its panic protection")
	}
	body := src[i:]
	if j := strings.Index(body, "\n\t}\n"); j > 0 {
		body = body[:j]
	}
	if !strings.Contains(body, "recover()") {
		t.Error("the avatar sweep no longer recovers; a panic in it kills the process")
	}
}

// triggerProtected is the fix for the asymmetry the audit found: a job run
// from its timer went through ServiceLoop's recover, and the same job run from
// the operator's "run now" button went through a bare goroutine, where a panic
// killed the process. The fatal path was the one an operator takes on purpose,
// while watching.
func TestTriggerProtectedSurvivesAPanic(t *testing.T) {
	job := schedule.RegisterJob("test-trigger-protected", "unit test")

	done := make(chan struct{})
	trigger := triggerProtected(job, func() {
		defer close(done)
		panic("job body blew up")
	})
	trigger()
	<-done

	// The goroutine's deferred recover runs after close(done), so give it a
	// moment to record the error rather than racing it.
	var msg string
	for i := 0; i < 100; i++ {
		if snap := job.Snapshot(); snap.LastError != "" {
			msg = snap.LastError
			break
		}
		time.Sleep(time.Millisecond)
	}
	if msg == "" {
		t.Fatal("the panic was swallowed silently; an operator would see nothing")
	}
	if !strings.Contains(msg, "job body blew up") {
		t.Errorf("recorded error does not name the panic: %q", msg)
	}
}

// And the whole point: reaching this line at all means the panic did not take
// the test binary down with it.
func TestTriggerProtectedDoesNotKillTheProcess(t *testing.T) {
	job := schedule.RegisterJob("test-trigger-protected-2", "unit test")
	done := make(chan struct{})
	triggerProtected(job, func() { defer close(done); panic("boom") })()
	<-done
	time.Sleep(5 * time.Millisecond)
}

// A triggered panic must reach the panic sink as well as the job, because the
// two answer different questions: the job's error is what the operator who
// pressed the button sees now, and the sink is where it is still findable
// tomorrow. Wiring one and not the other was the state this host was in.
func TestTriggerProtectedReachesThePanicSink(t *testing.T) {
	prev := schedule.PanicSink
	t.Cleanup(func() { schedule.PanicSink = prev })

	type report struct {
		job string
		err error
	}
	got := make(chan report, 1)
	schedule.PanicSink = func(_ context.Context, jobName string, err error) {
		select {
		case got <- report{jobName, err}:
		default:
		}
	}

	job := schedule.RegisterJob("test-trigger-sink", "unit test")
	triggerProtected(job, func() { panic("sink me") })()

	select {
	case r := <-got:
		if r.job != "test-trigger-sink" {
			t.Errorf("sink got job %q", r.job)
		}
		if r.err == nil || !strings.Contains(r.err.Error(), "sink me") {
			t.Errorf("sink got err %v, which does not name the panic", r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the panic never reached the sink; it is only on the job, which the next run clears")
	}
}
