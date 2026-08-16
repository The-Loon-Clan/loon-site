# Where this stands, and what is left

A checklist, in three parts: is the framework solid, is it usable, and can
something other than a human drive it.

Numbers are measured, not estimated — every one has a command behind it in
[QUALITY.md](QUALITY.md). Re-run them rather than trusting this page.

---

## 1. Framework foundations

### Done

| | evidence |
| --- | --- |
| Build, vet, staticcheck, unused | 8 linters, 0 issues |
| Error handling | `errorlint` + `nilerr` + `bodyclose`; every discard triaged and reasoned |
| SQL safety | `sqllint.py` — 169 files, no SQL from anything but constants |
| Vulnerabilities | `govulncheck` 0 |
| HTML validity | W3C validator wired; only known failure is a data bug (below) |
| Accessibility | Lighthouse **100**, contrast checked at the token level across all themes |
| SEO | Lighthouse **100** |
| CSP + security headers | shipped, with `unsafe-inline` documented rather than hidden |
| CSRF | double-submit, header or field, tested including cross-session |
| Request validation | 26 input structs, rules in one place, compile-enforced by `Validate[T Input]` |
| Async layer | 12 controls, 3 documented swap shapes, request-level tests |
| Regression gates | `make check` runs fmt, vet, golangci, sqllint, **contrast**, tests, coverage floor |

### Open

| | why it matters | size |
| --- | --- | --- |
| **Mojibake in 2,008 release titles** | titles shown wrong to every visitor; the decode is in the usenet plugin | medium — needs the plugin fixed first, then a repair pass |
| **`unsafe-inline` in the CSP** | the policy cannot stop XSS while it stands | large — 4 host inline scripts plus a nonce mechanism for 35 in plugins |
| Coverage 23.8% / ~35% with services | large untested surface, especially plugin-backed pages | ongoing |
| `html` / `lh` not in CI | both need a running site; they are targets, not gates | small-medium |
| OpenSSF Scorecard never run | the single best signal for open-source release readiness | small (a workflow + token) |
| Performance never measured | Lighthouse perf needs a TLS deployment to mean anything | blocked on deployment |
| Diátaxis: no **how-to** docs | everything written is *explanation*, for someone who already has context | medium |

**Verdict.** The foundations are in good shape and, more importantly, they are
*measured* — the difference between this list and the one that could have been
written a week ago is that every "done" row has a command behind it. The two
substantial gaps are the mojibake (a data bug with a known cause) and
`unsafe-inline` (a design job touching the plugin contract).

---

## 2. Usability

Barely started, and it is the right next axis for humans.

**Measured: mobile layout.** `make mobile` loads every page the sitemap lists
at 390x844 and fails on any layout that does not fit. It is on the release list
below, and it earns its place: on its first run it failed **15 of 36 pages**,
14 of them the same fault — the account bar is a fixed three-track grid that
does not fit a phone, so every page in the account area scrolled sideways.

Nothing about that was visible. The bar rendered, the pages rendered, and the
35px of tab past the edge only showed if you dragged the page sideways, which
nobody does on purpose. Several passes done by eye had missed it, including one
done the same afternoon.

It checks two things, and the second is why it found anything:

| | |
| --- | --- |
| document overflow | the page scrolls sideways. Loud and rare. |
| element overhang | one element sticks past the viewport while the page does not scroll, because a container clips it. Invisible to `scrollWidth`, invisible in a screenshot. |

`/search` was the second kind: its results table laid out at 667px inside a
390px screen and the page never scrolled, because the table sat in a
`.data-table-wrapper`. Reading a result's size meant scrolling a table
sideways, on the page people use most.

The rest of usability is still unmeasured, which is itself the finding — the
accessibility score says the site is *operable*, not that it is *good*.
Candidates, in the order they would pay off:

- **A how-to layer in the docs.** Diátaxis names the gap precisely. There is no
  document that walks an operator through adding a plugin, seeding a site, or
  converting a control — only explanations of why things are as they are.
- **Empty states.** The demo seeds ranks, a store and news because "unseeded"
  and "broken" look identical to a visitor (`demoseed_web.go` argues this well).
  The same reasoning has not been applied page by page.
- **Error copy.** The htmx notice convention gave refusals a consistent shape;
  whether the words are any good has not been reviewed.
- **First-run.** A fresh operator meets an empty index and a wizard. Whether
  they get anywhere is unknown, because nobody has watched one try.

---

## 3. Can something other than a human drive it?

**MCP coverage is zero.** There is no MCP server, no tool definitions, nothing.
(The `agent` plugin is unrelated — those are fleet upload workers, not AI.)

But MCP is not the missing piece, and installing one would not help. MCP is a
transport that exposes *tools*; the tools do not exist. The measurement:

```
129  routes registered by the host
  2  reachable by a machine   (/api and /rss — the same Newznab search handler)
```

So of the things asked for — reply to a forum post, answer a ticket, moderate
content, fix a release — an AI can do **none of them**, and not for want of
permission. There is no endpoint. Every one of those actions exists only as an
HTML form behind a session cookie and a CSRF token.

### The good news, which is structural

Three things already built make this much smaller than it sounds:

**The request schema already exists.** 26 `…Input` structs declare exactly what
every endpoint accepts, in one file, with the rules beside them. That is an API
schema written in Go — it just has no JSON encoder attached. Nothing needs
designing; it needs exposing.

**Content negotiation already exists.** 26 handlers branch on `isHTMX(c)` to
choose between a fragment and a redirect. A third arm — JSON — is an extension
of a pattern that is already there and already tested, not a new architecture.

```go
if wantsJSON(c) { c.JSON(200, result); return }   // the arm that does not exist
if isHTMX(c)    { w.renderFragment(...); return } // the arm added this week
c.Redirect(...)                                    // the arm that always existed
```

**Authorisation is already per-route and centralised.** `mountModeration`,
`auth.Require(core.RoleMod)`, the entitlements service — an API key mapped to a
role reuses all of it. An AI acting as a moderator would be *a member with a
role*, which is the correct model and already the one in force.

### What it would take, in order

1. **Content-negotiate the existing handlers.** Add the JSON arm to the 26 that
   already branch. No new endpoints, no new validation, no new authorisation —
   the same handler, a third representation.
2. **Machine authentication.** API keys already exist as a concept
   (`apikey.Views`). They need to carry a role and gate non-Newznab routes.
3. **A schema document.** Generated from the input structs, not hand-written —
   a hand-written one drifts the first week.
4. **Then MCP**, which at that point is a thin wrapper: each tool is one
   endpoint, and the tool definitions come from the same structs.

### The thing to decide first, before any of it

**What is an AI allowed to do, and how is that visible?**

The moderation surfaces already made this concrete without meaning to.
`communityModVote` distinguishes a community vote from a staff resolution and
records `resolved_by` differently for each, precisely so a reader of the queue
can tell who decided and how. An AI moderator needs the same treatment: an
action taken by a model must be attributable *as such*, in the same log a human
action lands in, or the audit trail quietly stops meaning anything.

That is a design decision about accountability, not a technical one about
transport, and it wants making before the first tool ships rather than after.

---

## Before a release

Everything in `make check` runs without a site. These need one up (`make run`),
which is why they are a list rather than a target — a check that quietly passes
because the thing it tests was not running is worse than no check at all.

```sh
make release   # everything below, in one command, stopping at the first failure
```

That is six steps: `check` (which needs no site), then the stack, then four
audits against it — access, links, accessibility and mobile layout. Each is also
its own target (`make access`, `make links`, `make a11y`, `make mobile`) for
when one of them is what you are working on.

Two are NOT in it, because they pull a browser container and a few hundred
megabytes over the network, which does not belong in a command people run
often:

```sh
make html   # W3C validation of the running site
make lh     # Lighthouse: accessibility, SEO, best practices
make grade  # the scorecard, measured rather than believed
```

**The audits sign themselves in.** Four of them existed before this and were
wired to nothing — written, working, and run by no target, which is the same
failure as a check that passes without executing. `mobile` also used to want a
cookie pasted in by hand: without it, it silently checked 23 of 36 pages, and
the 13 it skipped were the account area, where 14 of its first 15 findings were.
They use `AUDIT_USER`/`AUDIT_PASS` now (alice/alice on the demo) and say so if
they cannot sign in, rather than reporting a clean run of a third of the site.

---

## Suggested order

1. **`ReleaseNFOStore`** — the only remaining item that is a feature silently
   doing nothing. Small, self-contained, and it feeds the metadata chain better
   identifiers.
2. **The JSON arm** on the handlers that already negotiate. This is the step
   that unlocks everything in part 3, and it is mostly mechanical.
3. **The mojibake fix**, which needs the plugin repo.
4. **`unsafe-inline`**, which needs a plugin-contract decision.
5. **Usability**, once there is a how-to layer to hang it on.

MCP comes after 2, and by then it is a small job rather than the large one it
looks like today.
