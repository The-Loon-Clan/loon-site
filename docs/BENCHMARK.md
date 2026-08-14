# How this project measures up

A comparison of `loon-site` against three established indexer/tracker
codebases, and what it implies for getting to a release people will trust.

The three were read, not skimmed for talking points: structure, test layout,
tooling, service topology, comment density, and feature surface. Where a number
appears below it was counted, and the command that counted it is stated so the
number can be disputed.

| | **loon-site** | **UNIT3D** | **NNTmux** | **NexusPHP** |
| --- | --- | --- | --- | --- |
| Language | Go 1.26 | PHP 8.4 / Laravel | PHP 8.x / Laravel | PHP 8 / Laravel |
| App LOC | 18,350 | 72,589 | 133,381 | 45,935 |
| Test LOC | 8,540 | 22,865 | 35,505 | 71 |
| Test cases | 239 | ~852 | ~902 | ~2 |
| Test : app LOC | **1 : 2.1** | 1 : 3.2 | 1 : 3.8 | 1 : 647 |
| Comment density | **31%** | 29% | 16% | 10% |
| Routes | 278 (with plugins) | 554 | 183 | 66 |
| CI workflows | 1 | 9 | 4 | 1 |
| Static analysis | golangci-lint, all checks | PHPStan L7 + baseline | PHPStan + baseline + Rector | none |
| CONTRIBUTING / SECURITY / CoC | all three | all three | CONTRIBUTING only | none |
| Coverage reported | yes, with a floor | no | no | no |

Counted with `find … -name '*.php' -exec cat {} + | wc -l`, `grep -c` for
comment lines (`^\s*(//|\*|/\*)`), and `grep -rhoE 'Route::(get|post|…)'` for
routes. loon-site's route count is from the running site's registration log, so
it includes the 24 plugins; the PHP counts are route files only.

## Where loon-site already leads

Worth stating plainly, because the rest of this document is about gaps.

- **Test ratio.** One line of test per 2.1 lines of code is the best of the
  four. NexusPHP has 71 lines of test for 46,000 lines of application — it is
  effectively untested, and it is a widely deployed tracker.
- **Coverage is measured and floored.** None of the three report coverage at
  all. Ours is low in absolute terms (23.4% with services) but it is known,
  published, and cannot fall.
- **Comment density, and the kind of comment.** 31% is the highest of the four,
  and the PHP figures are inflated by generated docblocks (`@param string $id`)
  that restate a signature. The comments here mostly record *why* — the
  decision, the failure it prevents, the bug that produced it.
- **Static analysis with nothing excluded.** Both PHP projects run PHPStan with
  a *baseline* file — a committed list of pre-existing violations agreed to be
  ignored. UNIT3D's is 811 lines; NNTmux's is 1,070. `make golint` has no
  baseline, no suppressed checks, and none of golangci-lint's default exclusion
  presets (one of which hides unchecked errors in tests). All five `//nolint`
  comments in the tree carry a stated reason, e.g. `crypto/sha1` because
  RFC 6238 specifies HMAC-SHA1.
- **The checks are runnable.** CI calls `make check` rather than its own list of
  steps, so what runs there is exactly what runs on a laptop.

## Where loon-site is behind

Ranked by what a prospective adopter would notice first.

### 1. One process does everything

Every one of the three separates the web process from the work:

| | Web | Worker | Scheduler | Search | Mail (dev) |
| --- | --- | --- | --- | --- | --- |
| UNIT3D | nginx + php | queue | — | meilisearch | mailpit |
| NNTmux | webapp | worker | scheduler | manticore + elasticsearch | mailpit |
| NexusPHP | openresty + php | queue | scheduler + cleanup | — | — |
| **loon-site** | **app** | **worker** | **worker** | — | — |

**Done.** `LOON_ROLE=web|worker` splits them; `compose.worker.yml` runs the
second container off the same image. The default stays `all`, so a plain
`docker compose up` is still one container that does everything.

Verified by running both: the worker executed eight jobs including the usenet
crawler, and the web process executed none. It also turned up two things that
only appear once the roles are split — "Run now" was triggering jobs locally,
so the button ran work inside the web process (or, for a worker-only plugin,
silently did nothing while redirecting as though it had); and the guestbook
example plugin declared no process kinds, so the hello-world was teaching the
pattern that puts a job loop in the web process.

The seam was already built and already honoured — the host was opting out of it. Plugins declare `Processes: []string{"web", "api"}`,
and `core.Boot` drops any plugin that does not run in the current process kind.
The host then passes `Process: "all"` ([main.go:353](../web/handlers/main.go)),
which `boot.go` treats as the explicit bypass:

```go
if c.Process != "" && c.Process != "all" {   // core/boot.go:56
```

That estimate held: the host reads the role from the environment and passes it
through, and the framework does the rest — it logs each plugin it skips, and
the plugins had already declared their kinds (the scraper worker-only, the
tracker web and api). The host-side background loops needed the same gate:
cover backfill, local-link matching, sitemap regeneration and the avatar sweep
are jobs in everything but name.

### 2. No release engineering

None of: tagged versions, a changelog, signed artefacts, a published image, or
an upgrade note. `make vuln` and CI are green, and there is still no way to say
"I am running 0.3.1" or to get from one version to the next.

UNIT3D is not better here in kind — it has no `CHANGELOG.md` either — but it
ships something this does not: a documentation book (`book/`, mdBook) covering
installation and upgrades, which is most of why people trust it enough to run
it. Documentation is the release artefact that matters, and ours is one README.

**Mostly done.** `CHANGELOG.md`, a release workflow publishing a versioned
multi-arch image to ghcr.io with notes lifted from that changelog, a version
stamped into the binary and reported by `/healthz` and the boot log, and
`docs/UPGRADING.md` stating the guarantee. What remains is pushing a tag, which
is a decision rather than a task.

The changelog carries the behaviour changes that need stating — the trusted-proxy default in particular, which
silently changes what a proxied deployment records until the operator sets
`LOON_TRUSTED_PROXIES`.

Writing the workflow found a bug in the workflow: the first version of the
release-notes extractor interpolated the version into an awk regex, and the
escaped brackets were read as a character class — so every `## ` heading
matched and the entire changelog, Unreleased section included, would have been
published as the notes for v0.1.0. Found by running it against a fixture rather
than by tagging something and reading the result.

### 3. Input validation is not a named thing

UNIT3D has **114 Form Request classes** — one per endpoint that accepts input,
each a named, separately testable unit holding that endpoint's rules. This is
the pattern most worth stealing.

Handlers here read `c.PostForm` inline and validate ad hoc. It works, and it
means the rules for a given endpoint cannot be read in one place or tested
without a request. The avatar pipeline is the counter-example that shows the
value: once `processAvatar` was a named function, its rules got twelve tests.

**Started.** `internal/request` now holds the interface and the shared checks,
and `web/handlers/inputs.go` holds one `…Input` struct per endpoint that has
adopted it — `registerPost` first, since its rules are genuinely conditional
(an invite code is required on an invite-only site and meaningless otherwise).

Two things enforce it, one of which the language does for free:

```go
func Validate[T Input](in T) Errors     // T constrained to Input
```

Passing a struct with no `Validate` method is a compile error
(`does not satisfy request.Input (missing method Validate)`), the same device as
`storage.SQL`. What the compiler cannot see is a struct declared, filled from a
form and then never passed to `Validate` at all — so `inputs_test.go` reads the
package's own declarations and fails on any type named `…Input` that has not
stated its rules.

Remaining: the other endpoints. Introduce one where a handler is already being
touched, rather than as a sweep.

### 4. Architecture decisions are not recorded

NNTmux keeps `docs/adr/` — numbered Architecture Decision Records with
Status / Context / Decision / Consequences. This project has the *reasoning*
in exceptional quantity, but it is distributed across comments and commit
messages, so a decision can only be found by someone who already knows where to
look.

**Done.** [docs/adr/](adr/) holds the four, retrofitted from the arguments
already in the code. Writing 0003 turned up a claim that was not true yet — it
said a test asserts the module root holds nothing but the embed, and no such
test existed. Rather than soften the sentence, the test now exists: the root is
allowed exactly one non-test file, no functions but `init`, and an embedded tree
containing only `web/templates` and `web/static`.

### 5. No git hooks, no editor config

NNTmux uses CaptainHook for pre-commit and commit-msg. Nothing here stops an
unformatted or unlinted commit before CI sees it, and there is no
`.editorconfig`.

`.editorconfig` now exists, so an editor set to spaces no longer turns a
one-line change into a whole-file diff.

**Remaining:** an opt-in `make hooks` installing a pre-commit that runs
`make fmt lint`. Opt-in because a hook nobody asked for gets bypassed with
`--no-verify` and then ignored forever.

### 6. One CI workflow doing everything

UNIT3D has nine workflows; this has one.

Two claims in the first draft of this section were wrong, and CI disproved them
within the day. `govulncheck` **is** on the critical path — it runs with no
`continue-on-error` and failed a build over seven standard-library advisories
in Go 1.26.5, on a push that touched none of the affected code. And there **is**
a scheduled run: the workflow carries `cron: "0 6 * * 1"`, so the same checks
run weekly whether or not anybody pushed. That weekly run is the one that would
have caught those advisories anyway.

~~What is genuinely missing: a release workflow (below), CodeQL or an equivalent
SAST pass, and automated dependency updates.~~ All three now exist. One workflow doing everything is
also a single point of failure for required-check configuration, but that is a
convenience argument rather than a correctness one.

## Structure and Go standards

The layout is already idiomatic and better-argued than the comparators':
`cmd/`, `internal/` (compiler-enforced, not the meaningless `pkg/`), a root
package holding only the embed because `//go:embed` cannot reach a parent
directory. Nothing here needs rearranging.

Three specific observations:

- **`views.go` was 1,750 lines** and held the `web` struct, the template set,
  the gates, the helpers and several page handlers — the one file where "what
  is this file about" had no single answer. Split along its own section
  comments into `tmplfuncs.go` (the template FuncMap and the pure helpers
  behind it) and `auth_web.go` (the pages reachable without a session), leaving
  1,149. Pure movement: 51 functions before, 51 after.
- **Interfaces are correctly placed but rare.** Go convention is that the
  consumer declares the interface, and `bookmarkList` and `bookmarkedRows` now
  do exactly that. Two handlers follow the pattern; the rest take the concrete
  `*storage.Store`. That is the direct cause of `web/handlers` sitting at 19%.
- **No `Store` interface, and there should not be one.** `*storage.Store` has
  ~97 methods. An interface mirroring it would be implementable by exactly one
  type. The narrow-consumer-interface pattern is the right answer and is
  already established.

## Testing

The ratio is good; the distribution is not.

| Package | Coverage |
| --- | --- |
| `internal/config` | 100% |
| `internal/request` | 100% |
| `internal/sanitize` | 93% |
| `internal/middleware` | 87% |
| `internal/markdown` | 88% |
| `internal/storage` | 38% (3.6% without a database) |
| `web/handlers` | 31.0% (22.1% without a database) |
| **total** | **34.2% with services, 23.0% without** |

Both PHP projects separate `tests/Unit` from `tests/Feature`, and NNTmux adds
`tests/Integration` and `tests/Install`. Here, unit and integration tests live
in the same package and are separated by whether an environment variable is
set — which works, and is why the "fail rather than skip in CI" guard had to be
added.

What is genuinely strong: almost every test here was verified against the
mutation it exists for. That is unusual and worth keeping as a stated norm —
a test that has never been seen to fail is a claim, not a check.

**What to do:** keep extending the consumer-interface pattern handler by
handler. `admin_views.go` (`canView`, `siteGate`, `homeWidgets`) is next on
merit — it is gate logic, and gate logic is where the two real findings of the
last session came from.

## Features

278 routes against UNIT3D's 554. The gap is real but narrower than it looks,
because 24 plugins carry a large part of the surface: forums, wiki,
communities, playlists, messages, news, tickets, donations, points, ranks,
rewards, achievements, backups, tracker, hit-and-run, seedlock, usenet,
catalog, scraper, stats, store, events.

Genuinely absent, in rough order of how often a tracker is asked for them:

1. **Requests / bounties** — `Request`, `RequestFill`, `Bounty`, `Claim`. A
   core social loop of a private tracker: members ask for content and put
   points behind the asking. The points ledger and the gifting mechanics needed
   to build it already exist.
2. **Polls** — small, self-contained, and the natural second worked example
   for a plugin author after the guestbook.
3. **Subtitles** — attachments against a release.
4. **Similar / related releases** — the catalog cross-ids that would drive it
   are already populated.
5. **Yearly overview, top-ten pages** — cheap given the stats plugin.
6. **Reports on content** — a members-facing report route into the moderation
   queue that already exists.

## What could move into a plugin

The host is thinner than it first appears. `forum_web.go`, `usenet_web.go` and
`catalog_web.go` are *not* duplicated features — they are host-side wiring
(templates the host owes the plugin, tables, `SetDeps` seams, lazy capability
resolution), which is exactly where that code belongs.

Two real candidates:

- **`communitymod_web.go` (399 lines)** — the community moderation queue is a
  feature, not wiring, and communities is already a plugin. Its own comment
  says the voting is built and gated pending a decision, which makes it a
  clean thing to move while that decision is still open.
- **`avatar_web.go` (365 lines)** — the upload/crop/re-encode pipeline is
  self-contained and now well tested. A host needs *an* avatar; it does not
  need this specific pipeline compiled in.

Against moving anything else: the access map, the gates, the session wiring and
the storage layer are what a host *is*. Pushing them into plugins would make the
reference host stop demonstrating what a host has to implement.

## Getting to a release

Ordered so each step makes the next one safe.

**Before a 0.1.0 tag**
1. ~~`CHANGELOG.md`, `docs/UPGRADING.md`, and a version the binary reports~~
   (done). Remaining: push the first tag — a decision, not a task.
2. ~~A release workflow publishing a versioned image to ghcr.io.~~ (done —
   it also verifies the tag with the same service containers CI uses, and
   lifts release notes from the changelog.)
3. ~~`LOON_ROLE=web|worker` so a scaled deployment does not run N schedulers.~~ (done)
4. ~~`make vuln` on the critical path in CI, plus a scheduled run.~~ — both
   were already true when this list was written; the claim they were missing
   was wrong, and CI disproved it by failing a build over the Go 1.26.5
   advisories.

**Before calling it production-ready**
5. `web/handlers` coverage past 40%. 19.5% -> 31.0%, most of it from ONE
   change: a harness that boots a real web against a real database, which is
   where the uncovered mass always was. The remaining gap is the /admin and
   /moderation trees, wired with the plugin runtime.
6. Input validation as named, tested units — the mechanism is in place; the remaining endpoints are not.
7. ~~`docs/adr/` for the decisions already argued in comments.~~ (done)
8. ~~Split `views.go`.~~ (done — 1,750 → 1,149)
9. Requests/bounties, as the largest missing feature loop.

**Ongoing**
10. Keep verifying every new test against the bug it exists for. It is the
    practice that produced every real finding here, and it is the one thing on
    this page none of the three comparators do at all.
