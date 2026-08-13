# Changelog

Notable changes to loon-site. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Entries are written for somebody deciding whether to upgrade, so a change that
alters behaviour says what it does to a running site. Anything under
**Security** states the impact plainly — a fix nobody understands is a fix
nobody applies.

## [Unreleased]

Nothing is tagged yet. This section is the working set for the first release;
see [docs/BENCHMARK.md](docs/BENCHMARK.md) for what still stands between the
project and one.

### Added

- Load-balancer example: `compose.lb.yml` runs nginx in front of N app
  replicas. Sessions live in Redis, so no sticky rule is needed.
- `LOON_TRUSTED_PROXIES` — which proxy's `X-Forwarded-For` to believe. Empty by
  default, which trusts none.
- Input rules as named units: `internal/request` plus one `…Input` struct per
  endpoint, stating what that endpoint accepts. Enforced by the compiler —
  `request.Validate` is generic over an interface, so a struct with no rules
  will not compile at the call site.
- `make golint` — golangci-lint with every check enabled, no baseline, and none
  of the default exclusion presets.
- `make help`, `.editorconfig`, `CHANGELOG.md`, `CODE_OF_CONDUCT.md`, issue and
  pull-request templates.
- CI service containers for Postgres and Redis, so the integration tests run on
  every push rather than only when somebody remembers `make itest`.
- Coverage reporting with a floor in CI.

### Changed

- `make check` now includes golangci-lint, and CI installs the linter and calls
  the same target rather than keeping its own list of steps.
- `make itest` runs the whole integration suite against a throwaway Postgres
  **and** Redis, and fails when a test fails. It previously ended in `|| true`,
  so it could not fail, and ran a hardcoded subset of test names.
- The Go toolchain runs in a container by default (`scripts/go.sh`). Pass
  `GO=go` for the host's.

### Fixed

- **`X-Forwarded-For` was believed from anyone.** gin trusts every proxy unless
  told otherwise, so `ClientIP()` returned whatever the caller put in the
  header — and that value is recorded in `login_logs`, the page a member opens
  to check whether somebody else has been in their account. Two logins from one
  machine with two invented values recorded two different addresses. Now
  trusts nothing until `LOON_TRUSTED_PROXIES` names a proxy.
  **If you run behind a proxy, set that variable or every request will be
  logged as coming from the proxy's own address.**
- Seven reachable vulnerabilities, four of them in `golang.org/x/net/html` —
  the parser the HTML sanitiser is built on. All fixed; `make vuln` runs in CI.
- `make fmt` reported "gofmt clean" when the formatter failed to run at all,
  because the target captured stdout and ignored the exit status.
- `make sql` called bare `python`, which does not exist on many Linux
  distributions and is not guaranteed on a CI runner.
- Route names, page names and log keys are now checked by tests rather than by
  memory: a gated route mounted without its gate, a handler rendering a page
  that does not exist, and a log key outside the documented vocabulary each
  fail the build.
- `/apikeys` and `/rssfeed` were exempt from the members-only gate, because the
  exemption matched `/api` and `/rss` as bare prefixes. No route was called
  either, so nothing was reachable — but the exemption was granted by spelling
  rather than by decision. Now matches whole path segments.

[Unreleased]: https://github.com/The-Loon-Clan/loon-site/commits/main
