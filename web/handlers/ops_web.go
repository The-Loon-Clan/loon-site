package handlers

import (
	"context"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-site/internal/metrics"
	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon/schedule"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The operational endpoints: /healthz, /readyz, /versionz, /metrics.
//
// THE DISTINCTION THAT MATTERS is between the first two, and getting it wrong
// is how a blip becomes an outage.
//
//	/healthz  LIVENESS  — is this process wedged? Checks NOTHING external.
//	/readyz   READINESS — can this instance serve? Checks the database.
//
// An orchestrator RESTARTS on a failed liveness probe and REMOVES FROM THE LOAD
// BALANCER on a failed readiness probe. So a liveness probe that checks the
// database means a thirty-second database blip kills every container at once,
// and they all come back into a database that is still blipping. A readiness
// probe that checks the database means the same blip drains traffic and puts it
// back, which is the entire point.
//
// /healthz therefore stays a bare 200 and must not be "improved" into checking
// anything. That is the whole reason this comment exists next to it.

// ready reports whether this instance can serve requests.
//
// The DATABASE is the only dependency checked, because it is the only one
// whose absence makes every page an error. Redis is optional here by design,
// the scraper's upstreams are somebody else's site, and a plugin that has
// degraded reports that through its own health contract — none of those should
// take an instance out of rotation.
func (w *web) ready(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	if w.data == nil || !w.data.DB().Valid() {
		c.String(http.StatusServiceUnavailable, "no database")
		return
	}
	if err := w.data.DB().Raw().PingContext(ctx); err != nil {
		// The error is logged, not returned: a readiness endpoint is reachable
		// by whatever can reach the port, and a driver error carries host names
		// and sometimes credentials.
		w.log.Error("readiness ping failed", "err", err)
		c.String(http.StatusServiceUnavailable, "database unreachable")
		return
	}
	// Maintenance is a 503 too, and deliberately: an instance an operator has
	// deliberately closed should be drained rather than serving the
	// maintenance page to health checks and looking healthy while doing it.
	if core.SiteStateOf(ctx, w.registry()) == core.SiteMaintenance {
		c.String(http.StatusServiceUnavailable, "maintenance")
		return
	}
	c.String(http.StatusOK, "ready")
}

// versionInfo is what /versionz answers.
type versionInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Go      string `json:"go"`
	Process string `json:"process"`
	Started string `json:"started"`
	Uptime  string `json:"uptime_seconds"`
}

// startedAt is when this process came up, for the uptime figure.
var startedAt = time.Now()

// version answers /versionz as JSON.
//
// Separate from /healthz even though the build already rides in that endpoint's
// body, because they are read by different things: /healthz is scraped by an
// orchestrator that wants a status code and ignores the body, and this is read
// by a person or a deploy script asking "what is actually running there". A
// status code cannot carry a commit, and a deploy script should not be parsing
// "ok abc123".
func (w *web) version(c *gin.Context) {
	c.JSON(http.StatusOK, versionInfo{
		Version: Version,
		Commit:  Commit,
		Go:      runtime.Version(),
		Process: processRole(),
		Started: startedAt.UTC().Format(time.RFC3339),
		Uptime:  strconv.FormatInt(int64(time.Since(startedAt).Seconds()), 10),
	})
}

// httpDuration is the one distribution this site keeps about itself.
var httpDuration = metrics.NewHistogram(
	"loon_http_request_duration_seconds",
	"HTTP request duration by route template.",
	metrics.DefaultBuckets,
)

// measureRequests times every request into httpDuration.
//
// LABELLED BY ROUTE TEMPLATE, never by path. gin's FullPath() gives
// "/release/:id" where the URL is "/release/295823", and that difference is the
// difference between one series and one per release — 160,000 of them here,
// which does not make a richer metric, it makes an unusable one and takes the
// monitoring system with it.
//
// A request that matches no route reports "unmatched" rather than its path, for
// exactly the same reason: a 404 scanner would otherwise mint a series per URL
// it tried, which is a metrics system somebody else can fill up from outside.
func (w *web) measureRequests() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		httpDuration.Observe(map[string]string{
			"route":  route,
			"method": c.Request.Method,
			"status": strconv.Itoa(c.Writer.Status()),
		}, time.Since(start).Seconds())
	}
}

// metricsEndpoint serves /metrics in the Prometheus text format.
//
// GATED, and this is not optional: the payload names every job, every plugin,
// the member count, the size of the index and the exact build. That is a
// reconnaissance summary of the deployment, and it is the sort of endpoint
// people leave open because "it is just numbers".
func (w *web) metricsEndpoint(c *gin.Context) {
	ctx := c.Request.Context()
	c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	samples := w.buildSamples(ctx)
	if err := metrics.Write(c.Writer, samples); err != nil {
		w.log.Error("write metrics", "err", err)
		return
	}
	if err := httpDuration.Render(c.Writer); err != nil {
		w.log.Error("write metrics", "err", err)
	}
}

// buildSamples collects everything except the HTTP histogram.
//
// Four families, in the order somebody debugging reads them: what this build
// is, what the jobs are doing, what the plugins say about themselves, and how
// big the site is.
func (w *web) buildSamples(ctx context.Context) []metrics.Sample {
	var out []metrics.Sample

	// Build info as a labelled 1. The convention looks odd and is the standard
	// one: a version is not a number, so it travels as labels on a constant,
	// which lets a query join any other metric to the build that produced it.
	out = append(out, metrics.Sample{
		Name: "loon_build_info", Kind: metrics.Gauge, Value: 1,
		Help: "Build and process identity; the value is always 1.",
		Labels: map[string]string{
			"version": Version, "commit": Commit,
			"go": runtime.Version(), "process": processRole(),
		},
	})
	out = append(out, metrics.Sample{
		Name: "loon_uptime_seconds", Kind: metrics.Gauge,
		Help:  "Seconds since this process started.",
		Value: time.Since(startedAt).Seconds(),
	})

	out = append(out, w.jobSamples()...)
	out = append(out, w.pluginSamples(ctx)...)
	out = append(out, w.siteSamples(ctx)...)
	return out
}

// jobSamples turns the scheduler's registry into metrics.
//
// Nearly free, because schedule.JobInfo already holds all of it in memory — no
// query, no counters to maintain, nothing that can drift from what /admin/jobs
// shows.
//
// loon_job_last_success_timestamp_seconds is the one to alert on, and the
// reason is worth stating: alert on AGE, not on failure. A job that fails is
// noisy and visible. A job that silently stops being scheduled never fails
// again — its failure counter simply stops moving — and age is the only signal
// that notices.
func (w *web) jobSamples() []metrics.Sample {
	var out []metrics.Sample
	for _, j := range schedule.GetAllJobs() {
		l := map[string]string{"job": j.Name}
		out = append(out,
			metrics.Sample{
				Name: "loon_job_runs_total", Kind: metrics.Counter, Labels: l,
				Help: "Times a job has run since this process started.", Value: float64(j.RunCount),
			},
			metrics.Sample{
				Name: "loon_job_last_duration_seconds", Kind: metrics.Gauge, Labels: l,
				Help: "How long the most recent run took.", Value: float64(j.LastDurationMs) / 1000,
			},
			metrics.Sample{
				Name: "loon_job_paused", Kind: metrics.Gauge, Labels: l,
				Help: "1 when an operator has paused the job.", Value: boolValue(j.Paused),
			},
			metrics.Sample{
				Name: "loon_job_failing", Kind: metrics.Gauge, Labels: l,
				Help: "1 when the most recent run ended in an error.", Value: boolValue(j.Status == "error"),
			},
		)
		if !j.LastRun.IsZero() {
			out = append(out, metrics.Sample{
				Name: "loon_job_last_run_timestamp_seconds", Kind: metrics.Gauge, Labels: l,
				Help:  "When the job last ran, as a unix timestamp. ALERT ON THE AGE OF THIS: a job that stops being scheduled never fails again.",
				Value: float64(j.LastRun.Unix()),
			})
		}
	}
	return out
}

// pluginSamples turns each plugin's own health report into a metric.
//
// One series per plugin per STATE, valued 0 or 1, rather than one series with a
// number meaning ok/degraded/failing. A numbered state cannot be queried
// without a decoder ring, and an alert on it says "plugin health is 2".
func (w *web) pluginSamples(ctx context.Context) []metrics.Sample {
	reg := w.registry()
	if reg == nil {
		return nil
	}
	var out []metrics.Sample
	for _, h := range pluginapi.PluginHealth(reg) {
		got := h.Value.Health(ctx)
		for _, state := range []pluginapi.HealthState{
			pluginapi.HealthOK, pluginapi.HealthDegraded, pluginapi.HealthFailing,
		} {
			out = append(out, metrics.Sample{
				Name: "loon_plugin_health", Kind: metrics.Gauge,
				Help:   "1 for the state a plugin reports itself in.",
				Labels: map[string]string{"plugin": h.Key, "state": string(state)},
				Value:  boolValue(got.State == state),
			})
		}
	}

	// Whatever the plugins themselves measure (pluginapi.MetricSource). The
	// host does not know what these are and does not need to: it validates the
	// shape and passes them through.
	for _, src := range pluginapi.MetricSources(reg) {
		for _, m := range src.Value.Metrics(ctx) {
			if m.Name == "" {
				continue
			}
			kind := metrics.Gauge
			if m.Kind == pluginapi.MetricCounter {
				kind = metrics.Counter
			}
			out = append(out, metrics.Sample{
				Name: m.Name, Help: m.Help, Kind: kind, Labels: m.Labels, Value: m.Value,
			})
		}
	}
	return out
}

// siteSamples are the domain figures — how big the site is.
//
// Read on SCRAPE rather than counted per request, because they are answers to
// "how much of this is there" and a count kept incrementally is a count that
// drifts from the table it claims to describe.
func (w *web) siteSamples(ctx context.Context) []metrics.Sample {
	if w.data == nil || !w.data.DB().Valid() {
		return nil
	}
	var out []metrics.Sample
	if n, ok := w.data.CountUsers(ctx); ok {
		out = append(out, metrics.Sample{
			Name: "loon_members", Kind: metrics.Gauge,
			Help: "Registered accounts.", Value: float64(n),
		})
	}
	return out
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// processRole is which leg this process is: web, worker or all.
//
// Read from the environment rather than plumbed from main, because /metrics and
// /versionz are wired before the role variable is in scope and the answer is a
// constant for the life of the process either way.
func processRole() string {
	if r := os.Getenv("LOON_ROLE"); r != "" {
		return r
	}
	return "all"
}
