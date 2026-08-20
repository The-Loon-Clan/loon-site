# Observability

What this site exposes, what to alert on, and the decisions behind both.

Written because "do we have a clear outline of what metrics should be set up"
had no answer on 20 Aug 2026: `/healthz` existed, nothing else did, and the
checklist section called "Metrics & observability" was about log keys.

---

## The four endpoints

| | Answers | Checks | Who reads it |
|---|---|---|---|
| `/healthz` | Is this process wedged? | **nothing** | orchestrator liveness probe |
| `/readyz` | Can this instance serve? | the database, site state | load balancer / readiness probe |
| `/versionz` | What is running here? | — | a person, a deploy script |
| `/metrics` | Everything else | jobs, plugins, tables | a scraper. **Gated.** |

### Why healthz and readyz are different endpoints

This is the decision that matters and the one most often got wrong.

An orchestrator **restarts** a container on a failed liveness probe and
**removes it from the load balancer** on a failed readiness probe.

So a liveness probe that checks the database means a thirty-second database
blip kills every container at once — and they all come back into a database
that is still blipping. A readiness probe that checks the database means the
same blip drains traffic and puts it back when it clears.

`/healthz` therefore returns a bare 200 and must never be "improved" into
checking anything. There is a comment saying so next to it in `ops_web.go`.

`/readyz` checks the **database only**. Redis is optional here by design, the
scraper's upstreams are somebody else's site, and a plugin that has degraded
reports that through its own health contract — none of those should take an
instance out of rotation.

### Why /metrics is behind the admin gate

The payload names every job, every plugin and its health, the member count and
the exact build. That is a reconnaissance summary of the deployment, and it is
exactly the sort of endpoint left open because "it is just numbers".

A deployment that wants an unauthenticated scrape should bind a second listener
on an internal interface rather than opening this one.

---

## What is exported

### Build and process

```
loon_build_info{version,commit,go,process}   always 1
loon_uptime_seconds
```

A version is not a number, so it travels as **labels on a constant 1**. That
looks odd and is the standard convention: it lets a query join any other metric
to the build that produced it.

### Jobs — the highest-value family here

```
loon_job_runs_total{job}
loon_job_last_duration_seconds{job}
loon_job_last_run_timestamp_seconds{job}
loon_job_failing{job}
loon_job_paused{job}
```

Nearly free: `schedule.JobInfo` already holds all of it in memory, so there is
no query and nothing that can drift from what `/admin/jobs` shows.

**Alert on the AGE of `loon_job_last_run_timestamp_seconds`, not on
`loon_job_failing`.** A job that fails is noisy and visible. A job that silently
stops being scheduled never fails again — its failure gauge sits at 0 forever —
and age is the only signal that notices. This site has had that failure.

```promql
# a job that has not run in six hours
time() - loon_job_last_run_timestamp_seconds > 6 * 3600
```

### Plugins

```
loon_plugin_health{plugin,state}    1 for the state it reports
<plugin>_<thing>_total              whatever the plugin exports
```

One series per state valued 0/1, rather than one series with a number meaning
ok/degraded/failing. A numbered state cannot be queried without a decoder ring,
and an alert on it says "plugin health is 2".

`degraded` is the state worth alerting on and the reason the contract exists:
every plugin degrades gracefully because the checklist requires it, which means
every plugin is by construction able to be **silently useless**.

```promql
loon_plugin_health{state="degraded"} == 1
```

### HTTP

```
loon_http_request_duration_seconds_bucket{route,method,status,le}
loon_http_request_duration_seconds_sum / _count
```

Labelled by **route template** — `/release/:id`, never `/release/295823`. The
path form would be 160,000 series on this index. A request matching no route
reports `unmatched` for the same reason: otherwise a 404 scanner mints a series
per URL it tries, which is a metrics system somebody else can fill from outside.

### The site

```
loon_members
```

Read on scrape rather than counted incrementally, because an incremental count
drifts from the table it claims to describe.

---

## Adding a metric to a plugin

Implement `pluginapi.MetricSource` and register under
`metrics.source.<plugin>`. Rules, all of them in CHECKLIST §12:

- **Bounded labels.** A job name, an outcome, a source. Never a user id, a
  release id or a path.
- **Base units.** Seconds, not milliseconds. A dashboard cannot tell from the
  wire which you meant.
- **Cheap on scrape.** Read counters already in memory. A `SELECT count(*)`
  here runs every fifteen seconds forever against a table that only grows.
- **Prefer counters.** Let the query compute the rate; absolute totals belong
  on an admin page.

`mediainfo` is the worked example, and its third metric is the point of the
whole seam: `mediainfo_screenshot_fetch_failures_total` rising against a flat
success count means members are being handed a link error they cannot act on,
and nothing outside that plugin can see it.

---

## Why this is hand-written and not client_golang

Almost everything here is a **snapshot** of state the process already holds —
job status from the scheduler, plugin health from a contract, row counts from
the database. A pull-based exporter over existing state is a formatter, not a
metrics system. The one thing that genuinely accumulates is request duration,
and a fixed-bucket histogram is forty lines.

The other side of that trade, stated so nobody has to rediscover it: a
hand-written exposition is a place to get label escaping and histogram
cumulativeness subtly wrong, so both are pinned by tests in
`internal/metrics/metrics_test.go` rather than reasoned about — including that
one unescaped quote makes a whole scrape unparseable, which reports the target
as **down** rather than as "one bad label".

The swap is contained if it is ever worth making: plugins contribute through
`pluginapi.MetricSource`, the endpoint formats, and nothing in between knows
which library did it.

---

## Not done

- **No alerting rules shipped.** The PromQL above is the outline, not a
  deployed alert; there is no Alertmanager config in this repo.
- **No dashboards.** Same reason.
- **Worker-process metrics are unexposed.** `/metrics` is a web route, so a
  worker-only process publishes nothing. Its jobs are visible from the web
  process only if they share a registry, which they do not in split mode.
- **No tracing.** Nothing correlates a slow page with the queries under it.
