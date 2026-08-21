# Security

## Reporting a vulnerability

Please report privately through GitHub's
[security advisories](https://github.com/The-Loon-Clan/loon-site/security/advisories/new)
rather than opening a public issue.

Include what you did, what happened, and what you expected. A proof of concept
helps but is not required — a clear description of the flaw is enough to act on.

This is a small project without a paid security team, so expect a first reply
within a week rather than within hours.

## What this software is

A Usenet indexer that anyone can run, and the reference host for a plugin
framework. It handles accounts, passwords, sessions, two-factor authentication,
uploads and — optionally — a BitTorrent tracker that keeps ratio accounting.

## What is defended, and how

**Injection.** Every statement is parameterised. Inside `internal/storage` the
compiler enforces it: `Conn`'s methods take the named type `SQL`, so a `string`
expression will not compile at all. Outside it, where handlers use a raw `sqlx`
handle, `scripts/sqllint.py` fails the build on SQL assembled by concatenation
or formatting. Exceptions require `// sqllint:allow <reason>`.

The linter is tested against real injections — seven cases in
`scripts/sqllint_selftest.py`, four that must be refused and three that must be
allowed, run by `make check` **before** the linter itself.

This paragraph used to make that claim and it was not true; there was no test
of any kind. Writing one found that the `fmt.Sprintf` rule had a literal
backspace inside its regex, so it could never match and had been dead its whole
life. See *Known limitations*.

**Stored HTML.** `internal/sanitize` is an allowlist over a real parser, not a
denylist of tags: input is parsed with `x/net/html` and re-serialised, so
anything not explicitly permitted cannot survive. Markdown from members runs the
same policy.

**CSRF.** Double-submit token on every state-changing method, minted per session
and required as a form field or header. `SameSite=Lax` already blocks the common
case; this covers what it does not. Ten tests in
`internal/middleware/csrf_test.go`, including one that walks every
state-changing verb and one that pins the exemptions below.

**Four paths are exempt**, and naming them is the point — "every
state-changing method" was doing a lot of work in that sentence. `/api` and
`/rss` authenticate by API key and carry no session cookie; `/api/downloads/report`
is posted by a script inside a member's download client, which has no browser to
have been issued a token; `/dev/focus` belongs to the dev inspector, whose whole
route tree is only registered when `LOON_UI_INSPECT` is set. They are listed by
exact path rather than by prefix, so a future route under `/api` is not exempted
by accident.

`/admin/jobs/config` **used to be a fifth**, and it was the one that mattered: a
browser-rendered admin form, exempted because loon/schedule's bundled page could
not embed a host token. loon's `schedule` now reads the token off the gin context
(`schedule.CSRFContextKey`), so the form carries one and the route is gated like
everything else.

**Authentication.** Passwords are bcrypt-hashed by loon-baseline. Two-factor is
TOTP with hashed single-use recovery codes; enrolment is not complete until a
code verifies, and disabling requires a code rather than merely a session.
Sessions are signed cookies, `HttpOnly`, and `Secure` when `SECURE_COOKIES=1`.

**Authorisation.** Role gates sit on the route, not inside the handler, and
answer an API caller with a status rather than a redirect to a login page.
Row-level ownership is checked inside the `WHERE` of the statement that changes
the row, so there is no window between "is this yours" and "change it".

## Known limitations

Stated because a security document that lists only strengths is not useful.

- **Two checks were dead for their whole lives, for the same one-byte reason.**
  `sqllint`'s `fmt.Sprintf` rule and `audit_links.py`'s tokenless-form check
  each carried a literal backspace (0x08) where a `\b` word boundary was meant.
  Both patterns were unmatchable, both reported clean, and nothing showed it —
  an editor renders 0x08 as nothing and a diff shows an unchanged line.
  `make check` now runs `scripts/audit_ctlchars.py` over this repo and its three
  siblings. Assume the same class of accident is what a future silent check
  looks like.
- **Eight POST forms in `loon-baseline` carry no CSRF token**, so every one of
  them answers 403 to the person who clicks it — including *change password*,
  *regenerate API key*, and the admin *set role* and *reset password* forms.
  They are broken rather than exploitable, and they were invisible twice over:
  `make resources` scans this repo and `loon-plugins` but not `loon-baseline`,
  and the live audit that would have caught them was the dead one above. Fixing
  them needs the CSRF seam to move from `loon-plugins/pluginapi` into `loon`,
  because `loon-baseline` cannot depend on the plugins module.
- **No dependency scanning or signed releases.** Worth having; not there yet.
- **Coverage is 15%.** The tests that exist are mostly regressions for real
  bugs, which is where they earn most, but the handler layer is thinly covered.
- **`SECURE_COOKIES` is off by default** so the site runs over plain HTTP out of
  the box. Set it for any deployment behind TLS.
- **`LOON_SESSION_SECRET` has an insecure default** so a checkout runs without
  configuration. Set it for anything reachable; the default is well known.
- **The dev UI inspector (`LOON_UI_INSPECT`) serves files from disk and injects
  script into a frame.** It is off unless asked for, and its routes are not even
  registered otherwise — but it must never be set on anything reachable.

## Scope

The demo accounts (`alice`, `bob`) have passwords equal to their usernames by
design, so the site is usable the moment it boots. That is not a vulnerability;
a deployment that keeps them is.
