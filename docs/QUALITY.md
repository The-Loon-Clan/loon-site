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
| — explicit discards | `grep '^\s*_ = '` (non-test) | **34** |
| — log-and-continue | `grep 'log.Error('` in web/handlers | **52** |
| — wrapped with `%w` | `grep '%w'` | 28 |
| Test depth | `make cover` | 34.7% with services / 23.0% without |
| SQL safety | `scripts/sqllint.py` | clean |
| Vulnerabilities | `scripts/govulncheck.sh` | 0 |
| **HTML validity** | W3C Nu validator | **never run** |
| **Accessibility** | `pa11y-ci` / `axe-core` vs WCAG 2.2 AA | **never run** |
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

**Triage, not eradication.** The goal is that each of the 86 sites is one of:
handled, deliberately discarded with a comment saying why, or wrapped and
returned. A blanket rule would be wrong and would get reverted.

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
| `contextcheck` | a function that should take a `context.Context` and does not |
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

**Next:** triage the 34 `_ =` discards. Each becomes one of: handled, discarded
with a comment saying why, or wrapped and returned. That is reading, not
linting — no tool can tell a deliberate best-effort write from a swallowed
failure.
