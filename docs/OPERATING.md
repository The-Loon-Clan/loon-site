# Operating the indexer

Running notes for a live crawl: getting one started, and what to do when it
stops. Written from a deployment that hit each of these, so the numbers are
real rather than illustrative.

### Index some Usenet

1. Log in as **alice** → **Usenet** (`/admin/p/usenet` — providers, indexing,
   newsgroups, crawler dashboard, per-job status, filters, on its own tabs).
2. Enter an NNTP server → **Test connection** → **Fetch group list**.
3. Enable a low-volume group → **Crawl now**.
4. Watch **Jobs** (`/admin/jobs`), then **Search** for a title and download the `.nzb`.

The indexer keeps only the last few days of posts (configurable), assembles
multi-file releases into a single NZB, and parses quality tags
(resolution / source / codec / audio / language) shown as badges in search.

#### When the crawler stops

The symptom is every job idle with `crawl paused: staging N% full` and
`backfill paused: staging pressure N%` in the job logs, and the builder
reporting `built 0 of N candidate set(s) — N incomplete`.

That gauge is **`usenet.articles` row count / `staging_max_rows`**, and the row
count is `pg_class.reltuples` — the *planner's estimate*, not a `COUNT(*)`
(`maintenance_store.go`: an exact count of a multi-million-row table on every
pass is not worth it). On a table this write-heavy the estimate drifts high, so
check it against reality before changing any knob:

```
select reltuples::bigint from pg_class where oid = to_regclass('usenet.articles');
select count(*) from usenet.articles;
```

Estimate far above the count (it was 12.4M against 6.8M here) means the crawler
is pausing on a table that is not actually full. `VACUUM (ANALYZE, PARALLEL 0)
usenet.articles` corrects it immediately. To stop it recurring, make autovacuum
keep up with the churn:

```sql
ALTER TABLE usenet.articles SET (
    autovacuum_vacuum_scale_factor  = 0.02,   -- default 0.2
    autovacuum_analyze_scale_factor = 0.02,   -- default 0.1
    autovacuum_vacuum_cost_delay    = 0
);
```

Only once the estimate is honest is the soft cap worth judging —
**Usenet → Indexing → Staging soft cap (rows)** (default 2,000,000; this deploy
runs 30,000,000, about 26 GB of table).

Size it against the **pending sets**, not against the article count. The gauge
is a deadlock, not a queue: a set only completes when *more* of its articles
arrive, so a cap that pauses the crawl while large sets are still assembling
removes the very supply they are waiting on, and the builder then has nothing to
drain. That is what a permanent stall looks like — every job idle, article and
NZB counts frozen, and the builder reporting `built 0 of N — N incomplete`.
The worker telemetry names the sets that are short:

```
select value::json->'pending' from usenet.settings where key = 'worker_telemetry';
```

Entries like `have=113 need=511` over 47k segments are the ones setting the
floor. 2,000,000 was far under it here, and even 10,000,000 re-wedged at 95%;
30,000,000 leaves the room those sets need to finish. Watch for `crawl paused`
turning into `pass budget reached` and the NZB count climbing again.

Note that **Redis is not involved**: `USENET_STAGING` defaults to `pg`, so Redis
holds the page cache only and sits around a megabyte with no `maxmemory` set.
Growing it does nothing for a staging stall.

## Profiling (pprof)

Off unless `LOON_PPROF_TOKEN` is set. An unset token does not mean "no auth" —
the listener never starts, so forgetting to configure it cannot leave it open.

Set it in `.env` (see `.env.example`, which compose reads automatically):

    LOON_PPROF_TOKEN=<512-bit key>      # openssl rand -hex 64
    LOON_PPROF_ADDR=127.0.0.1:6060      # default

**A token under 64 characters is refused and the listener does not start**, with
the generating command in the error. That floor admits every reasonable encoding
of a strong key — 512 bits is 128 hex characters or 88 base64 — and refuses the
thing people type in a hurry. Refusing rather than warning, because a warning at
boot is read once and then lives in a log nobody greps, and a guessable token in
front of heap dumps is worse than no profiling at all.

Three layers, and the first is the one that matters:

1. **Bind it somewhere only you can reach.** `127.0.0.1` or a Tailscale
   address. The two checks below are the second and third layers.
2. **An IP allowlist** — loopback, RFC1918, and Tailscale's CGNAT range. The
   private ranges are needed because Docker's userland proxy rewrites the
   source address to the bridge gateway, so a request that genuinely arrived
   over a private interface presents as `172.18.0.1`.
3. **The token**, as `Authorization: Bearer` or `?token=`, compared in constant
   time.

A refusal is a **404**, never a 403 — a 403 confirms there is something there
worth attacking. Every refusal is logged, so a scan is visible.

    go tool pprof 'http://127.0.0.1:6060/debug/pprof/profile?seconds=30&token=TOKEN'
    curl -H "Authorization: Bearer TOKEN" http://127.0.0.1:6060/debug/pprof/heap -o heap.out

Why a separate listener rather than a route on the site: pprof on the main
engine is one middleware-ordering mistake from being public, and it is the most
useful thing to hand an attacker — heap dumps carry live data, and
`/debug/pprof/profile` is a CPU denial of service anyone can start.
