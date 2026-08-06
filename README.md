<p align="center">
  <img src="img/logo.png" alt="loon" width="180">
</p>

<h1 align="center">loon-demo-site</h1>

<p align="center">A working reference site built on the <a href="https://github.com/The-Loon-Clan/loon">loon</a> plugin framework.</p>

---

A small but real site on loon: it wires every `core.Deps` seam, boots the plugin
runtime against Postgres, and serves a browsable, dark-themed **Usenet indexer** —
news / search / groups / NZB download, an admin dashboard, and a setup wizard.
`main.go` is what a HOST looks like; the plugins come from
[loon-plugins](https://github.com/The-Loon-Clan/loon-plugins).

## Run it

```
docker compose up --build
```

Open **http://localhost:8090/** and log in as **alice** (admin) or **bob** (user)
— each account's **password is the same as its username** (`alice`/`alice`,
`bob`/`bob`).

> Everything runs in Docker (Postgres + the app). The build pulls in `loon` and
> `loon-plugins` as sibling checkouts via BuildKit named contexts, so keep them
> checked out next to this repo. (That requirement drops once loon tags releases.)

### Working on the UI

Templates and stylesheets are compiled into the binary (`//go:embed`), which is
what lets the runtime image be distroless — but it also means a one-line CSS
tweak costs a full rebuild. For UI work, add the dev overlay:

```
docker compose -f docker-compose.yml -f compose.dev.yml up -d
```

That mounts `web/` and re-parses templates per request, so edits under
`web/templates/` and `web/static/` show up on refresh. See `compose.dev.yml`.

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

## What's wired

- **Auth** — username/password login (bcrypt-verified) over a signed session
  cookie; the login form is the only way in.
- **Admin** — `/admin/plugins` + `/admin/jobs` (both from loon) + the
  setup wizard inside `/admin/settings`.
- **Plugins** (from loon-plugins) — `usenet` (the indexer), `scraper`, `catalog`,
  `backups`, `stats`, `dailyreward`, `pointstore` — plus the local `guestbook`
  demo plugin.

## What to read

- `main.go` — the host: builds `core.Deps` from adapters, uses loon's scheduler
  (`schedule.CoreScheduler`), then `core.New` → `core.Boot`.
- `views.go` / `usenet_web.go` — the host-side pages, the session cookie, and the
  usenet capability wiring.
- `plugins/guestbook/` — the smallest possible plugin (own schema, routes, points,
  a job): the hello-world for writing your own.

The reference production instance (ameNZB) is private; this demo tracks the same
framework version via the sibling-checkout `replace` in `go.mod`.

## License

MIT
