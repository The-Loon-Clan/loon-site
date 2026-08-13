<p align="center">
  <img src="img/logo.png" alt="loon" width="180">
</p>

<h1 align="center">loon-site</h1>

<p align="center">
  A working Usenet indexer, and the reference host for the
  <a href="https://github.com/The-Loon-Clan/loon">loon</a> plugin framework.
</p>

<p align="center">
  <a href="https://github.com/The-Loon-Clan/loon-site/actions/workflows/ci.yml">
    <img src="https://github.com/The-Loon-Clan/loon-site/actions/workflows/ci.yml/badge.svg" alt="CI">
  </a>
  <img src="https://img.shields.io/badge/go-1.26-00ADD8" alt="Go 1.26">
  <img src="https://img.shields.io/badge/license-MIT-blue" alt="MIT">
</p>

---

## What this is

A real site, not a skeleton. It crawls NNTP, assembles multi-part posts into
NZBs, parses release names into quality badges, fetches cover art, and serves a
browsable dark-themed indexer with search, a forum, a wiki, communities, an
admin area and a Newznab API that Sonarr and Radarr can consume.

It is also the **reference host** for [loon](https://github.com/The-Loon-Clan/loon):
every seam a plugin can use is wired here, so the code doubles as the worked
example of how to build one.

## Run it

```sh
git clone https://github.com/The-Loon-Clan/loon-site.git
cd loon-site
docker compose up --build
```

Open **http://localhost:8090/** and sign in as **alice** (admin) or **bob**
(member) — each password is the same as its username.

That is the whole setup. No sibling checkouts, no configuration file, no
external services: Postgres and the app both come up in Docker, the schema
migrates itself on first boot, and the site seeds two accounts and some starter
content so there is something to look at.

> **Nothing is indexed yet.** The site is a working indexer with an empty
> catalogue until you point it at an NNTP server — see
> [docs/OPERATING.md](docs/OPERATING.md).

## Configuration

Everything is off by default and switched on with an environment variable.
Nothing below is required to run the site.

| Variable | Effect |
| --- | --- |
| `LOON_DSN` | Postgres connection string. Defaults to the compose database. |
| `LOON_SITE_NAME` | The name shown in the header and in authenticator apps. |
| `LOON_SITE_URL` | Absolute base baked into generated `.torrent` files. |
| `LOON_SESSION_SECRET` | Session signing key. **Set this for anything reachable.** |
| `LOON_TRUSTED_PROXIES` | Whose `X-Forwarded-For` to believe. Empty (the default) trusts none. |
| `REDIS_ADDR` | Use Redis for the page cache and sessions instead of memory. |
| `LOON_TRACKER` | Run the BitTorrent tracker (announce endpoints, passkeys, ratio). |
| `LOON_CHEATCHECK` | Judge tracker readings and raise flags for staff review. |
| `LOON_HITRUN` | Enforce hit-and-run rules over the tracker's accounting. |
| `LOON_SEEDLOCK` | One host per torrent per member. Needs `REDIS_ADDR`. |
| `LOON_DEV` | Re-read templates per request, so UI edits show on refresh. |
| `TMDB_API_KEY` | Cover art for films and TV. Without it, keyless sources are used. |
| `TPDB_API_KEY`, `ANIDB_CLIENT` | Metadata for XXX and anime. |
| `TURNSTILE_SITEKEY`, `TURNSTILE_SECRET` | Cloudflare captcha on login and register. |

The tracker and its rules are off by default deliberately: switching one on
starts keeping ratio accounting the moment it is reachable, which is not
something a checkout should do to you.

## How it is laid out

```
cmd/loonsite/        the binary
assets.go            //go:embed of web/ — the only thing at the module root
internal/
  config/            the operator switches above, in one place
  markdown/          the site's one prose renderer
  middleware/        CSRF
  sanitize/          the HTML allowlist policy
  storage/           every SQL statement, and the row types it scans into
web/
  handlers/          HTTP: routing, auth gates, view models, templates
  templates/  static/
plugins/guestbook/   the smallest possible plugin, as a worked example
```

Two rules hold the shape:

- **No SQL in a handler.** `web/handlers` decides what to show and who may see
  it; `internal/storage` decides how to ask the database. This is enforced by
  review, and the properties each statement depends on (atomic claims, ownership
  checks in the `WHERE`) are documented beside the SQL that implements them.
- **The root package holds only the embed.** `//go:embed` cannot reference a
  parent directory, and the runtime image is distroless with no `web/` directory
  in it at all — so the package declaring those directives has to sit above
  `web/`, and everything else moved out from under it.

Start reading at [`web/handlers/main.go`](web/handlers/main.go) (what a host
does at boot), then [`plugins/guestbook/`](plugins/guestbook/) (own schema,
routes, points, a job — the hello-world for writing your own).

## Development

```sh
make check      # gofmt, build, vet, golangci-lint, sqllint, tests, coverage floor
make golint     # golangci-lint alone (slow the first time, seconds after)
make vuln       # known vulnerabilities in code this project actually calls
make itest      # storage tests against a throwaway Postgres
make run        # the site, detached
make clean      # stop it — KEEPS the volumes
```

The Go toolchain runs **in a container** by default (`scripts/go.sh`), so
nothing unsigned is written to your machine. That is not ceremony: on Windows an
anti-virus quarantines freshly built test binaries, and the symptom is a
toolchain reporting `no such tool "compile"` rather than anything that reads
like a security product. Pass `GO=go` to any target to use the host toolchain
instead.

CI runs `make check` rather than its own list of steps, so what runs there is
exactly what you can run before pushing.

**Working on loon or loon-plugins at the same time?** Copy `go.work.example` to
`go.work` and your sibling checkouts take over from the pinned versions. The
file is gitignored, and the container build sets `GOWORK=off`, so a workspace
cannot leak into an image or into CI.

**Behind a load balancer?** Sessions live in Redis when `REDIS_ADDR` is set, so
no request is tied to a process and replicas need no sticky rule:

```sh
docker compose -f docker-compose.yml -f compose.lb.yml up --build -d --scale app=3
```

nginx takes port 8090 and the app containers are reachable only from inside the
network. The one setting that matters is `LOON_TRUSTED_PROXIES`, and the overlay
pins nginx to a fixed address so it can name exactly one: gin walks
`X-Forwarded-For` from the right and stops at the first address it does not
trust, so trusting only the proxy makes an invented header harmless, while
trusting the whole subnet walks straight past the real peer into the invented
part — putting a proxy in front and restoring the spoofing it was meant to end.

**Working on the UI?** Templates and CSS are compiled into the binary, which is
what lets the image be distroless — but it means a one-line CSS change would
otherwise cost a rebuild:

```sh
docker compose -f docker-compose.yml -f compose.dev.yml up -d
```

## What we check, and what we do not

Trust is easier to give when the gaps are stated, so:

- **`make sql`** refuses SQL assembled from anything but constants — the way
  parameterisation gets lost is always a concatenated or formatted fragment, so
  that is what it looks for. It is tested against a real injection, not only
  against a clean tree. An exception needs `// sqllint:allow <reason>`, and the
  reason is part of the syntax.
- **`make golint`** runs golangci-lint with every check enabled and nothing
  excluded — a long linter list with half of it suppressed teaches people to
  read failures as noise. Its first run found 21 issues, and the interesting
  ones were not the unchecked errors: three doc comments described symbols that
  had been renamed or had moved to another package during a restructure, and
  one of them documented a handler that no longer lived in that file at all.
- **Coverage is 17.6%**, with a floor in CI so it cannot fall. That is low, and
  the shape is lopsided: `config` is at 100% and `sanitize` at 93%, `storage` at
  3.6% — and that last number is the misleading one. The storage tests exist and
  run the real SQL, but they skip unless `LOON_TEST_DSN` is set, so the layer
  where a mistake corrupts data is covered only when somebody runs `make itest`.
  The tests that exist are mostly regression tests for bugs that actually
  happened, plus the security-critical paths — whether a private site is
  private, and what the avatar pipeline accepts from a stranger. Broad coverage
  of the handler layer is not there yet.
- **CI proves the README's first claim** by cloning the repository and running
  `docker compose up` with nothing else present, then failing if the first boot
  logs a single error. That check exists because a first-boot bug did ship: a
  shared settings table was created only when an unrelated plugin was enabled,
  so a default deployment logged two errors and silently fell back to defaults.
- **`make vuln` reports vulnerabilities in code this project actually calls**,
  not everything in the module graph — that distinction matters. Its first run
  found seven reachable ones, four of them in `golang.org/x/net/html`, the
  parser `internal/sanitize` is built on. All are fixed; the scan runs in CI.
- **`X-Forwarded-For` is not believed by default.** gin trusts every proxy
  unless told otherwise, so `ClientIP()` returned whatever the caller put in the
  header — and two logins from one machine with two invented values recorded two
  different addresses in `login_logs`, which is the page a member opens to check
  whether somebody else has been in their account. It now trusts nothing until
  `LOON_TRUSTED_PROXIES` names a proxy. Verified both ways: three spoofed values
  from one source collapse to one address, and two genuinely different sources
  stay two.
- **No signed releases yet.** Worth having; not there.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security reports: [SECURITY.md](SECURITY.md).

## License

MIT — see [LICENSE](LICENSE).
