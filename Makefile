# The checks, in one place, so CI and a laptop run the same thing.
#
# CI calling `make check` rather than its own list of steps is the point: a
# check that only exists in a workflow file cannot be run before pushing, and
# one that only exists locally does not stop anybody.
#
# GOWORK=off throughout. A go.work pointing at sibling checkouts is for
# development; the checks must see what a clone sees, or they pass here and
# fail for everybody else.

# The Go toolchain runs in a container by default.
#
# `go build` and `go test` write unsigned executables, and a Windows anti-virus
# treats them as exactly what they look like — the symptom is not an obvious
# error but a toolchain reporting `no such tool "compile"` because the compiler
# was quarantined between two commands. In a container nothing lands on the
# host, and the Go version is the one go.mod asks for rather than whatever the
# machine happens to have.
#
#   make check           # in Docker
#   make check GO=go     # on the host, if you prefer
GO ?= bash scripts/go.sh
export GOWORK = off

# Coverage floor. Deliberately just under where the project stands rather than
# an aspiration: its job is to stop a regression, and a floor nobody can meet
# gets deleted rather than met. Raise it when the number rises.
#
# Set for a run with NO backing services — 23.0% on a laptop, 34.2% with the
# Postgres and Redis that CI provides, because the storage integration tests
# take that package from 3.6% to 38%.
#
# Which means the floor cannot police the thing it most looks like it polices.
# If the service containers or their environment variables ever went missing,
# every one of those tests would skip, coverage would fall to the laptop number,
# and the floor would still pass. That job belongs to the tests themselves: with
# CI set and no DSN they FAIL rather than skip. The floor is for gradual
# erosion; the guard is for the suite disappearing.
COVER_MIN ?= 22.0

# The floor for a run WITH backing services. A single floor has to be set for
# the lower case, which makes it nearly meaningless in CI: at 22% CI could lose
# twelve points of coverage and still pass. The floor follows the run instead —
# LOON_TEST_DSN present means the substantial half is running.
COVER_MIN_SERVICES ?= 33.0

.PHONY: help check build test itest cover lint golint fmt sql ctlchars css capabilities contrast resources vuln grade html lh run clean

# `make` on its own explains itself, rather than silently building the first
# target. Every target below already carried a `## name: description` line and
# nothing surfaced them, so the documentation existed and was unreachable.
.DEFAULT_GOAL := help

## help: list the targets, with what each one does
help:
	@echo "loon-site — make <target>"
	@echo
	@grep -hE '^## [a-z-]+:' $(MAKEFILE_LIST) \
	  | sed -e 's/^## //' \
	  | awk -F': ' '{ printf "  \033[1m%-8s\033[0m %s\n", $$1, $$2 }'
	@echo
	@echo "  The Go toolchain runs in Docker by default. Pass GO=go to use the host's."

## check: everything CI runs
check: fmt build lint golint ctlchars sql css capabilities contrast resources test cover

## build: compile every package
build:
	$(GO) build ./...

## test: the full suite. Storage integration tests SKIP unless LOON_TEST_DSN
## is set — see `make itest` for a throwaway database to point it at.
test:
	$(GO) test ./...

## itest: the tests that need real backing services — a disposable Postgres on
## 5599 and a Redis on 6398. Never the development database or its Redis: these
## truncate the tables they touch.
##
## This is what CI runs with its service containers, so a green CI and a green
## laptop mean the same thing. Two things here were wrong and are worth naming:
##
##   - it ended in `|| true`, so that the cleanup below always ran. That also
##     meant a FAILING integration test left the target reporting success —
##     `make itest` could not fail, which makes it a demonstration rather than
##     a check. The status is now kept, the cleanup still runs, and the target
##     exits with the result.
##   - it ran `-run "Scans|Ownership|Recovery"`, a hardcoded subset, so an
##     integration test added later would never run and nobody would be told.
itest:
	@docker rm -fv loon-itestdb loon-itestredis >/dev/null 2>&1 || true
	@docker run -d --name loon-itestdb -e POSTGRES_USER=demo -e POSTGRES_PASSWORD=demo -e POSTGRES_DB=loon_test -p 5599:5432 postgres:16-alpine >/dev/null
	@docker run -d --name loon-itestredis -p 6398:6379 redis:7-alpine >/dev/null
	@sleep 10
	@set +e; \
	 LOON_TEST_DSN="postgres://demo:demo@localhost:5599/loon_test?sslmode=disable" \
	 REDIS_TEST_ADDR="localhost:6398" \
	 $(GO) test -count=1 ./internal/storage/ ./web/handlers/ -v; \
	 status=$$?; \
	 docker rm -fv loon-itestdb loon-itestredis >/dev/null 2>&1; \
	 exit $$status

## cover: test with coverage, print the per-package table, enforce the floor
cover:
	@$(GO) test ./... -coverprofile=coverage.out >/dev/null 2>&1 || true
	@$(GO) tool cover -func=coverage.out | tail -1
	@total=$$($(GO) tool cover -func=coverage.out | tail -1 | awk '{print $$3}' | tr -d '%'); \
	 if [ -n "$$LOON_TEST_DSN" ]; then min="$(COVER_MIN_SERVICES)"; kind="with services"; \
	 else min="$(COVER_MIN)"; kind="no services"; fi; \
	 awk -v t="$$total" -v m="$$min" -v k="$$kind" 'BEGIN { \
	   if (t+0 < m+0) { printf "coverage %.1f%% is below the %.1f%% floor (%s)\n", t, m, k; exit 1 } \
	   printf "coverage %.1f%% (floor %.1f%%, %s)\n", t, m, k }'

## lint: vet, plus gofmt as an error rather than a suggestion
lint:
	$(GO) vet ./...

## golint: golangci-lint (errcheck, govet, ineffassign, staticcheck, unused).
##
## Its own target as well as part of `check` because the first run has to build
## the linter and takes minutes, while every run after it is seconds — worth
## being able to trigger deliberately rather than discovering mid-commit.
golint:
	@bash scripts/golangci.sh ./...

## fmt: fail if anything is unformatted, rather than reformatting silently
##
## The exit status is checked, not just the output. This target used to capture
## stdout and call an empty result clean — so when the script itself failed to
## run at all (`docker: not found`) it printed "gofmt clean" and `make check`
## carried on. A check that reports success when it did not execute is worse
## than no check, because it is the one nobody thinks to re-examine.
fmt:
	@out=$$(bash scripts/gofmt.sh); status=$$?; \
	 if [ $$status -ne 0 ]; then echo "gofmt check could not run (exit $$status)"; exit $$status; fi; \
	 out=$$(printf '%s' "$$out" | grep -v '^vendor/' || true); \
	 if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	@echo "gofmt clean"

## sql: refuse SQL built from anything but constants
##
## python3 first, then python. Bare `python` does not exist on a lot of Linux
## distributions and is not guaranteed on a CI runner that has not installed it
## deliberately — and this target failing to run is indistinguishable, from the
## outside, from it finding nothing wrong.
PYTHON ?= $(shell command -v python3 2>/dev/null || command -v python 2>/dev/null || echo python3)

## THE SELF-TEST RUNS FIRST, and it is not ceremony. sqllint's fmt.Sprintf rule
## carried a stray backspace in its regex and matched nothing for its whole
## life; a clean run over the tree was read as proof it worked. A detector that
## has never refused anything and one that CANNOT refuse anything look
## identical from outside. This makes them look different.
sql:
	@$(PYTHON) scripts/sqllint_selftest.py
	@$(PYTHON) scripts/sqllint.py

## ctlchars: stray control characters, which hide dead code in plain sight
##
## Runs over the sibling repos too, because the accident is a paste and pastes
## do not respect repository boundaries — one of the three found was in a Go
## comment in loon-plugins.
ctlchars:
	@$(PYTHON) scripts/audit_ctlchars.py . $(wildcard ../loon) $(wildcard ../loon-plugins) $(wildcard ../loon-baseline)

## css: classes the templates use and no stylesheet defines
##
## A class that does not exist has no effect and raises no error, so the element
## renders as though the class were absent -- indistinguishable from being
## styled to look plain. `.button--danger` and `.text-danger` were used across
## the forum, communities, admin and host templates and defined NOWHERE, so
## every Delete and Remove on the site looked like the safe button beside it.
##
## The script existed and nothing ran it. When first wired it exited 1 with
## eleven findings, six of which were its own false positives -- it read only
## .css files and missed a page that carried its own <style> block.
css:
	@$(PYTHON) scripts/audit_css.py

## capabilities: a wired plugin asking for something no wired plugin provides
##
## Optional lookups degrade quietly, correctly -- a host that has not installed
## the provider is a legitimate configuration -- and the cost of that lands on
## the host, which gets no signal at all. Rewards ran without event gating for
## as long as the events plugin went unwired, and what noticed was the link
## crawler seeing a 404.
##
## Static: it reads the host's imports and the plugins' Lookup calls, so it
## needs no running site.
capabilities:
	@$(PYTHON) scripts/audit_capabilities.py

## contrast: theme colour pairs against the WCAG AA minimum
##
## In `check` because it needs nothing — no Docker, no network, no running
## site, just Python and the CSS. That matters: the other two accessibility
## checks below need a browser and a live server, so they cannot gate a pull
## request, and this one can.
##
## It also catches what a browser check cannot. Lighthouse loads ONE theme on
## ONE page; this reads the tokens, so a theme nobody screenshotted is checked
## too. That is how nord's panel headings were found at 2.75:1 — a browser-only
## check had reported the site at 90 and never looked at that theme.
contrast:
	@$(PYTHON) scripts/contrast.py

## resources: refuse hardcoded images, unresolvable icons, new text in Go
##
## In `check` for the same reason contrast is: it reads files, so it gates a
## pull request rather than a release. It also reads the PLUGIN tree when one
## is checked out beside this repo — a plugin's <use href="#id"> resolves
## against the host's sprite sheet, so the host is the only place that check
## can be made, and nothing was making it.
## loon-baseline IS SCANNED TOO, and it was not until 21 Aug 2026. That scope
## gap is why eight POST forms there — change password, regenerate API key, the
## admin set-role and reset-password forms — carried no CSRF token and answered
## 403 to everyone who clicked them, invisibly, for as long as the host had
## CSRF middleware. A check's blind spot is wherever its argument list stops.
resources:
	@$(PYTHON) scripts/audit_resources.py $(wildcard ../loon-plugins) $(wildcard ../loon-baseline)

## html: W3C validation of the running site (needs `make run` first)
html:
	@bash scripts/htmlvalidate.sh

## lh: Lighthouse accessibility/SEO/best-practices (needs `make run` first)
lh:
	@bash scripts/lighthouse.sh

## release: everything, in the order that fails fastest
##
## `check` needs no site; the rest do, so this starts the stack itself rather
## than assuming. That is the point of having one target: every one of these
## already existed and four of them had never been run by anything, because
## running them meant remembering they were there.
##
## It STOPS at the first failure. A release check that prints twelve red
## sections is one nobody reads to the end.
release:
	@echo "== 1/7 check (no site needed)"      && $(MAKE) --no-print-directory check
	@echo "== 2/7 bringing the stack up"       && docker compose up -d --build >/dev/null && 	  for i in $$(seq 1 60); do 	    [ "$$(curl -s -o /dev/null -w '%{http_code}' http://localhost:8090/healthz)" = "200" ] && break; 	    sleep 2; 	  done; 	  test "$$(curl -s -o /dev/null -w '%{http_code}' http://localhost:8090/healthz)" = "200" 	    || { echo "the site did not come up; nothing below this can be trusted"; exit 1; }
	@echo "== 3/7 access (staff routes, destructive POSTs, table coverage)" && $(PYTHON) scripts/audit_access.py
	@echo "== 4/7 links"                       && $(PYTHON) scripts/audit_links.py
	@echo "== 5/7 admin nav (every admin page reachable)" && $(PYTHON) scripts/audit_adminnav.py
	@echo "== 6/7 accessibility"               && $(PYTHON) scripts/audit_a11y.py
	@echo "== 7/7 mobile (every page at 390px)" && $(PYTHON) scripts/mobile.py
	@echo
	@echo "all seven passed. html and lh are NOT in here — they need a browser"
	@echo "container and a network pull, so run them by hand: make html, make lh"

## access: every route probed as anonymous, a member and an admin
access:
	@$(PYTHON) scripts/audit_access.py

## links: every internal link the site offers actually resolves
links:
	@$(PYTHON) scripts/audit_links.py

## adminnav: every admin page is reachable from the admin nav
##
## An admin route served and named nowhere is one an operator finds by knowing
## the URL, which means they do not. It also hides breakage: /admin/donate had
## been truncating mid-render for as long as it existed, and the link crawler
## never reached it because nothing linked to it.
adminnav:
	@$(PYTHON) scripts/audit_adminnav.py

## a11y: the accessibility rules that can be checked without a browser
a11y:
	@$(PYTHON) scripts/audit_a11y.py

## mobile: every page at 390px, checked for layout that does not fit
##
## Needs `make run` first, and Chrome on the host (CHROME= to point at it).
## Signs itself in as AUDIT_USER/AUDIT_PASS, so the account pages are covered —
## it used to want a LOON_COOKIE, and without one it silently reached 23 of 36
## pages, skipping the third of the site where 14 of the first 15 failures were.
##
## Not in `check`, for the same reason html and lh are not: it needs a RUNNING
## site, and a check that silently passes when the thing it tests is not up is
## worse than no check. It belongs on the release list — see docs/READINESS.md.
mobile:
	@$(PYTHON) scripts/mobile.py

## shot: screenshot one page as a signed-in member (make shot NAME=x URL=/path)
##
## Here rather than left as a script somebody has to know about, which is what
## four working audits were until `make release` went looking for them.
##
## scripts/shot.sh is the anonymous version and captures the LIVE url, headers
## and all. This one holds a session, which Chrome's CLI cannot — so it is the
## only way to see /tracker, the account area or an admin page.
shot:
	@$(PYTHON) scripts/shot.py $(or $(NAME),page) $(or $(URL),/) $(or $(W),1400) $(or $(H),1000)

## grade: print the scorecard in docs/QUALITY.md, measured rather than believed
##
## Rows that need a running site are SKIPPED with a note rather than scored,
## because a blank is honest and a zero is not — a zero implies somebody looked.
grade:
	@echo "loon-site quality scorecard — $$(date -u +%Y-%m-%dT%H:%MZ)"
	@echo
	@printf "  %-22s " "formatting"; if bash scripts/gofmt.sh >/dev/null 2>&1; then echo "clean"; else echo "NEEDS gofmt"; fi
	@printf "  %-22s " "vet + linters"; if bash scripts/golangci.sh ./... 2>&1 | grep -q "^0 issues"; then echo "0 issues"; else echo "SEE make golint"; fi
	@printf "  %-22s " "sql safety"; $(PYTHON) scripts/sqllint.py >/dev/null 2>&1 && echo "constants only" || echo "FAIL"
	@printf "  %-22s " "contrast (WCAG AA)"; $(PYTHON) scripts/contrast.py >/dev/null 2>&1 && echo "all pairs pass" || echo "FAIL — run make contrast"
	@printf "  %-22s " "resources"; $(PYTHON) scripts/audit_resources.py $(wildcard ../loon-plugins) >/dev/null 2>&1 && echo "no hardcoded images or new text in Go" || echo "FAIL — run make resources"
	@printf "  %-22s " "vulnerabilities"; bash scripts/govulncheck.sh 2>&1 | grep -qi "No vulnerabilities found" && echo "0" || echo "SEE make vuln"
	@printf "  %-22s " "mobile (390px)"; if curl -sf -o /dev/null http://localhost:8090/healthz 2>/dev/null; then 	   $(PYTHON) scripts/mobile.py >/dev/null 2>&1 && echo "every page fits" || echo "FAIL — run make mobile"; 	 else echo "skipped (site not running)"; fi
	@printf "  %-22s " "error discards"; echo "$$(grep -rn '^[[:space:]]*_ = ' --include='*.go' . | grep -v _test | wc -l | tr -d ' ') (each carries its reason — docs/QUALITY.md)"
	@printf "  %-22s " "coverage"; $(GO) test ./... -coverprofile=coverage.out >/dev/null 2>&1; $(GO) tool cover -func=coverage.out 2>/dev/null | tail -1 | awk '{print $$3}'
	@rm -f coverage.out
	@echo
	@echo "  needs a running site (make run, then):"
	@echo "    make html    W3C validity"
	@echo "    make lh      accessibility / SEO / best practices"
	@echo
	@echo "  never run:  performance (needs a TLS deployment), OpenSSF Scorecard (needs a token)"

## vuln: known vulnerabilities in anything this code actually calls
vuln:
	@bash scripts/govulncheck.sh

## run: the site, on http://localhost:8090
run:
	docker compose up --build -d
	@echo "http://localhost:8090/  —  alice/alice (admin), bob/bob"

## clean: stop the stack, KEEPING the volumes (down -v would drop the index)
clean:
	docker compose down
	@rm -f coverage.out
