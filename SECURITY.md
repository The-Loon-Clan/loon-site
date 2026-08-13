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

**Injection.** Every statement is parameterised. `scripts/sqllint.py` fails the
build on SQL assembled by concatenation or formatting, which is the way that
protection is actually lost; the linter is tested against a real injection
rather than only against a clean tree. Exceptions require
`// sqllint:allow <reason>`.

**Stored HTML.** `internal/sanitize` is an allowlist over a real parser, not a
denylist of tags: input is parsed with `x/net/html` and re-serialised, so
anything not explicitly permitted cannot survive. Markdown from members runs the
same policy.

**CSRF.** Double-submit token on every state-changing method, minted per session
and required as a form field or header. `SameSite=Lax` already blocks the common
case; this covers what it does not.

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
