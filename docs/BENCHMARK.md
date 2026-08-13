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
  all. Ours is low in absolute terms (22.6% with services) but it is known,
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
| **loon-site** | **app (all of it)** | — | — | — | — |

Jobs run in-process here. That is a defensible choice for a reference host and
an indefensible one for an indexer that crawls NNTP: a long assembly job and a
page request compete for the same process, and `docker compose up --scale app=3`
now runs the scheduler three times. The load-balancer overlay added this session
makes that concrete rather than theoretical.

**What to do:** the seam for this is already built and already honoured — and
the host opts out of it. Plugins declare `Processes: []string{"web", "api"}`,
and `core.Boot` drops any plugin that does not run in the current process kind.
The host then passes `Process: "all"` ([main.go:353](../web/handlers/main.go)),
which `boot.go` treats as the explicit bypass:

```go
if c.Process != "" && c.Process != "all" {   // core/boot.go:56
```

So the change is small and mostly compose-side: read the role from the
environment, pass it through instead of the constant, and add a `worker`
service to the compose file. The framework does the rest, and already logs each
plugin it skips.

### 2. No release engineering

None of: tagged versions, a changelog, signed artefacts, a published image, or
an upgrade note. `make vuln` and CI are green, and there is still no way to say
"I am running 0.3.1" or to get from one version to the next.

UNIT3D is not better here in kind — it has no `CHANGELOG.md` either — but it
ships something this does not: a documentation book (`book/`, mdBook) covering
installation and upgrades, which is most of why people trust it enough to run
it. Documentation is the release artefact that matters, and ours is one README.

**What to do, in order:** `CHANGELOG.md` (Keep a Changelog); annotated tags;
a GitHub Release workflow building `ghcr.io/the-loon-clan/loon-site:<tag>`;
`docs/UPGRADING.md` stating the migration guarantee. This is the single
highest-value item on the list.

### 3. Input validation is not a named thing

UNIT3D has **114 Form Request classes** — one per endpoint that accepts input,
each a named, separately testable unit holding that endpoint's rules. This is
the pattern most worth stealing.

Handlers here read `c.PostForm` inline and validate ad hoc. It works, and it
means the rules for a given endpoint cannot be read in one place or tested
without a request. The avatar pipeline is the counter-example that shows the
value: once `processAvatar` was a named function, its rules got twelve tests.

**What to do:** a small `validate` package and a `…Input` struct per
state-changing endpoint, introduced where handlers are already being touched
rather than as a sweep.

### 4. Architecture decisions are not recorded

NNTmux keeps `docs/adr/` — numbered Architecture Decision Records with
Status / Context / Decision / Consequences. This project has the *reasoning*
in exceptional quantity, but it is distributed across comments and commit
messages, so a decision can only be found by someone who already knows where to
look.

**What to do:** `docs/adr/`, retrofitting the four decisions already argued at
length in the code — the defined `storage.SQL` type; `internal/` over `pkg/`;
the embed pinning the asset package to the module root; trust-no-proxy by
default.

### 5. No git hooks, no editor config

NNTmux uses CaptainHook for pre-commit and commit-msg. Nothing here stops an
unformatted or unlinted commit before CI sees it, and there is no
`.editorconfig`.

**What to do:** an opt-in `make hooks` installing a pre-commit that runs
`make fmt lint`. Opt-in because a hook nobody asked for gets bypassed with
`--no-verify` and then ignored forever.

### 6. One CI workflow doing everything

UNIT3D has nine workflows; this has one. Two are missing that matter: a
scheduled dependency/vulnerability run separate from the push job, and CodeQL
or equivalent. `make vuln` exists but is not in CI's critical path.

## Structure and Go standards

The layout is already idiomatic and better-argued than the comparators':
`cmd/`, `internal/` (compiler-enforced, not the meaningless `pkg/`), a root
package holding only the embed because `//go:embed` cannot reach a parent
directory. Nothing here needs rearranging.

Three specific observations:

- **`views.go` is 1,750 lines** and holds the `web` struct, the template set,
  the gates, the helpers and several page handlers. It is the one file where
  "what is this file about" has no single answer. Splitting it along the lines
  already implied by its own section comments would cost nothing.
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
| `internal/sanitize` | 93% |
| `internal/middleware` | 87% |
| `internal/markdown` | 88% |
| `internal/storage` | 38% (3.6% without a database) |
| `web/handlers` | 19% |
| **total** | **22.6% with services, 18.8% without** |

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
1. `CHANGELOG.md`, annotated tags, `docs/UPGRADING.md` with the migration guarantee.
2. A release workflow publishing a versioned image to ghcr.io.
3. `LOON_ROLE=web|worker` so a scaled deployment does not run N schedulers.
4. `make vuln` on the critical path in CI, plus a scheduled run.

**Before calling it production-ready**
5. `web/handlers` coverage past 40% via the consumer-interface pattern.
6. Input validation as named, tested units.
7. `docs/adr/` for the decisions already argued in comments.
8. Split `views.go`.
9. Requests/bounties, as the largest missing feature loop.

**Ongoing**
10. Keep verifying every new test against the bug it exists for. It is the
    practice that produced every real finding here, and it is the one thing on
    this page none of the three comparators do at all.
