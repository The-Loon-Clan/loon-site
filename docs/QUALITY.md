# Rating this codebase

A scorecard for Go, HTML, UI, UX, docs and release posture — and the commands
that produce each number.

## The rule this is built on

**A score not produced by a command is a vibe.**

That is the lesson of the work that preceded this document, learned repeatedly
and expensively: a mutation that did not compile "passed" three times; a test
asserting "never redirects" went green while a CSRF rejection stopped every
request before it reached a handler; a CSP was verified present and could have
been malformed and unenforced without anything saying so.

So every dimension below is either **measured** — there is a command, it emits a
number, and the number is in the table — or explicitly **judged**, which means a
human read it and no score is claimed. Nothing sits in between, because a
dimension that *sounds* measured and is not is worse than one openly marked as
opinion: nobody re-checks it.

## The scorecard

Numbers taken Aug 2026. Re-run them rather than trusting this table.

| Dimension | Measured by | Today |
| --- | --- | --- |
| Build correctness | `go vet`, `staticcheck`, `unused` | clean |
| Formatting | `scripts/gofmt.sh` | clean |
| Error handling | `errorlint`, `nilerr`, `bodyclose` (enabled) | clean — 4 found, 2 real bugs fixed |
| — explicit discards | `grep '^\s*_ = '` (non-test) | **all triaged**; each carries its reason |
| — log-and-continue | `grep 'log.Error('` in web/handlers | **52** |
| — wrapped with `%w` | `grep '%w'` | 28 |
| Test depth | `make cover` | 34.7% with services / 23.0% without |
| SQL safety | `scripts/sqllint.py` | clean |
| Vulnerabilities | `scripts/govulncheck.sh` | 0 — but it read 1 the day after this was written |
| HTML validity | `scripts/htmlvalidate.sh` | 16 errors, all one cause (below) |
| Accessibility | `scripts/lighthouse.sh` + `scripts/contrast.py` | **100** (was 90) |
| SEO | `scripts/lighthouse.sh` | **100** (was 91) |
| Best practices | `scripts/lighthouse.sh` | 78 — the rest is HTTPS, i.e. deployment |
| **Performance** | Lighthouse CI (LCP/INP/CLS) | **never run** |
| **Security posture** | OWASP ASVS L1 checklist | partial — CSRF, CSP, sanitiser, password rules done |
| **Supply chain** | OpenSSF Scorecard | **never run** |
| Docs structure | Diátaxis coverage (below) | partial |
| Comments | *judged* | — |
| API/UX copy | *judged* | — |

Bold rows are gaps with no measurement at all. That is the point of the table:
it is a map of what nobody is checking, not a report card on what is already
good.

## Reading the error-handling numbers

The three error rows are one finding, not three.

`errcheck` has been running since before this document and reports nothing,
which reads as "errors are handled". **`_ = f()` is exactly how a caller
silences errcheck**, and there are 34 of them. Some are correct — a deferred
`Close` on a read, a best-effort cache write — and some are a swallowed failure
that will surface later as data that quietly is not there. Nothing currently
distinguishes the two.

The 52 `log.Error` sites are the same shape one level up: logged, then
execution continues as though it had not happened. Again, some are right — a
notification that fails should not fail the request that triggered it — and the
rest are a decision nobody has revisited.

**Triage, not eradication.** Each site should be one of: handled, deliberately
discarded with a comment saying why, or wrapped and returned. A blanket rule
would be wrong and would get reverted.

The question that decides it, arrived at while doing the first twelve:

> **Does the failure announce itself?**

A session save that fails did not write the cookie, so the next request behaves
as though the action never happened — the member sees it, and there is nothing
further to tell them. Closing a read after a scan error is the same: the scan
error is the one that matters. Those stay discarded, with the reason written
down and `//nolint:errcheck` so the decision is visible to tooling too.

A store write that fails is invisible. The ticket is filed and its author is
never told; the pending email changes survive, still confirmable; the widget
layout reports "updated" and is not. Those get logged, and where the member is
waiting on an answer, refused.

**Done.** Every `_ = f()` in non-test code is now either handled or carries a
`//nolint:errcheck` naming the reason it is safe.

Six bugs came out of it, five of them fail-open — because Go's zero values are
`""`, `0` and `false`, and in this codebase each of those reads as "nothing
here, carry on":

| what | the failure it hid |
| --- | --- |
| `TOTPSecret` at the login gate | a failed read skipped 2FA entirely |
| `session.Clear` on logout | logout reported success, session survived |
| `CountOpenWishes` | a failed count read as 0, so the per-member cap lifted |
| entitlement `RoleOf` | a transient DB error cached as "no such user" |
| `err == sql.ErrNoRows` | a wrapped error read as a lookup failure |
| `moveWidget` | reorder failed, "Layout updated" shown |

One of the original 34 was not an error discard at all (`_ = old`, a
deliberately unused variable), so that count flattered itself slightly.

**Why this is not enforced by a linter.** `errcheck` ignores `_ = f()` unless
`check-blank` is set — which is exactly why it ran clean over all of this for
months. Turning it on was tried: it also reports `x, _ := f()`, landing on 79
sites, most of them the deliberate `in, _ := readXInput(c)` pattern. Revisit
alongside a decision about that signature, not on its own. Until then the
guarantee is social: the reasons are written down, and a reviewer can see a
bare discard for what it is.

## Standards worth adopting

### Go

Currently linted: `errcheck`, `govet`, `ineffassign`, `staticcheck`, `unused`,
`errorlint`, `nilerr`, `bodyclose` — 8 of golangci-lint's ~100.

`bodyclose` came back clean on the first run, which is worth recording: every
outbound call on the metadata paths already closes its body.

`nilerr` found three and **one was real** — see below. The other two were
deliberate and already carried prose comments saying so; they now carry
`//nolint:nilerr` with the reason, which makes a decision that was only
readable by humans visible to the tool as well.

Highest value for the gaps above:

| linter | catches |
| --- | --- |
| `errorlint` | comparing errors with `==` instead of `errors.Is`; `%v` where `%w` belongs |
| `wrapcheck` | errors returned across a package boundary without context |
| `godot`, `gocritic` | comment and idiom hygiene |
| `cyclop` / `gocognit` | functions past a complexity budget |

Style references: **Google's Go Style Guide** (style / decisions /
best-practices — the most thorough) and the **Uber Go Style Guide** (the most
widely adopted third-party one). Both are worth reading against
`web/handlers/`, which is by far the largest package here.

### HTML, UI, accessibility

- **WCAG 2.2 AA** — the actual standard, not a proxy for one.
- **pa11y-ci** or **axe-core** — score pages against it in CI. Start with the
  five that matter: `/`, `/browse`, `/search`, `/release/:id`, `/login`.
- **W3C Nu validator** (`vnu`) — runs offline in a container; catches the
  malformed markup a browser silently repairs and a screen reader does not.
- **ARIA Authoring Practices Guide** — canonical patterns for what is already
  built here: disclosure menus (`<details>`), tab strips, the live region the
  htmx notices swap into.
- **Lighthouse CI** — Core Web Vitals, and it will have opinions about the
  48 KB category grid.

### What the first HTML validation run found

`scripts/htmlvalidate.sh` runs the official W3C validator image against five
pages. First run: 98 errors, 82 of them htmx attributes (filtered, with the
reasoning in the script) and **16 real ones, all the same cause**.

`Forbidden code point U+0086` / `U+0088` — C1 control characters in release
titles, emitted straight into the page. The extract gives the mechanism away:
a mojibake run beginning `[B` followed by the tell-tale `A-circumflex` sequence.

That is U+2206 (bytes `E2 88 86`) decoded as UTF-8, re-read as Latin-1 — giving
U+00E2, U+0088, U+0086 — and re-encoded. A classic double decode.

**2,008 stored titles are affected**, counted directly against the C1 range
U+0080..U+009F rather than estimated.

So this is a data-integrity finding rather than a markup nit: those titles are
displayed wrong to every visitor, and the invalid HTML is the symptom that made
it visible. The decode happens at ingest, which is the usenet plugin's, so the
fix is not in this repository — the host could defend at render, and neither is
done yet. See BACKLOG.md item 9.

Worth noting how nearly this was missed. The first version of the script wrote
the pages to a `mktemp -d` and mounted it; on Windows that path is not
mountable, `/work` was empty, and the validator **exited 0 having checked
nothing**. It looked exactly like a clean run.

### Release and supply chain

- **OpenSSF Scorecard** — rates branch protection, signed releases, dependency
  update tooling, CI tests, fuzzing. The single highest-signal number available
  for the open-source goal, and it emits an actionable list rather than a grade
  alone. Dependabot is already on the backlog and is one of its checks.
- **OWASP ASVS Level 1** — a checklist rather than a scanner, which is the
  right shape for security: it names what is missing without pretending to have
  tested it.

### Documentation

**Diátaxis** — four modes, and a doc that is trying to be two of them is the
usual reason one reads badly:

| mode | question it answers | here |
| --- | --- | --- |
| Tutorial | teach me, by doing | missing |
| How-to | I have a task | thin |
| Reference | what are the exact rules | `ASYNC.md` rules, ADRs |
| Explanation | why is it like this | strong — `ASYNC.md`, `METADATA-METHODS.md`, `GAZELLE`/`BENCHMARK` |

The gap is **how-to**. Everything written so far explains decisions to somebody
who already has the context; nothing walks an operator through adding a plugin,
seeding a site, or converting a control to htmx.

## `make grade`

Not built yet. It should run every measured row and print the table, so the
numbers above can be regenerated rather than believed. Two constraints:

- It must **not** gate CI on the new dimensions until each has a floor that
  reflects reality. A check nobody can pass gets deleted rather than met — the
  same reasoning as the coverage floor in the `Makefile`.
- Rows with no measurement print `unmeasured`, never a zero. A zero implies
  somebody looked.

## Where to start

**Done:** `errorlint` is enabled. It found one — `err == sql.ErrNoRows` in the
donations settings store, where a wrapped error would have fallen through as a
lookup failure instead of "not set", and would only have begun doing so the day
something started wrapping.

`wrapcheck` was considered and **rejected**, for the reason .golangci.yml gives
about itself: it flags every error crossing a package boundary unwrapped, which
here is hundreds of sites, and a linter nobody can satisfy gets excluded — after
which people skim failures. Revisit only alongside a decision about where this
codebase wants wrapping to happen.

`nilerr` then found the bug that justifies the whole exercise. An entitlement
lookup read:

    if err != nil || u == nil {
        // Reserve the error return for transient failures, which are NOT cached.
        return 0, false, nil
    }

The comment states the intent exactly. The code never implemented it: a
transient database failure took the same branch as a genuinely absent user and
was cached as "no such user", so a momentary blip denied a real member their
grants for a whole cache window and then healed itself — the hardest kind of
bug to catch in the act. Split in two, and the error now propagates uncached.

### Linters considered and rejected

Two, and the reasoning is the same both times: this file's policy is that every
enabled linter is enforced, so one whose output is mostly annotations is not
earning its place — it trains people to skim.

**`wrapcheck`** flags every error crossing a package boundary unwrapped. Here
that is hundreds of sites. Revisit only alongside a decision about where this
codebase wants wrapping to happen; that is a design question, not a lint
setting.

**`contextcheck`** was enabled, run, and reverted. Five findings, and the two
that settle it are the graceful-shutdown pair:

    <-ctx.Done()
    shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
    _ = srv.Shutdown(shutCtx)

Inheriting there would be an actual bug — `Shutdown` would return immediately
with nothing drained. The linter wants the cancelled context passed in. The
remaining three are boot-time wiring and a fire-and-forget goroutine, where a
request context would be the wrong lifetime.

The fifth was checked by hand and is also a false positive: `renderRegions`
reads `c.Request.Context()` directly. contextcheck flagged the *call chain*
that reaches it, not the function, which already does the right thing.

So the final count is five findings and **zero real ones**. That is the whole
case against it here, and it is stronger than the "mostly annotations" argument
first written: annotations at least mark real decisions. These marked nothing.

**Next:** triage the 34 `_ =` discards. Each becomes one of: handled, discarded
with a comment saying why, or wrapped and returned. That is reading, not
linting — no tool can tell a deliberate best-effort write from a swallowed
failure.
