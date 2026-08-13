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
# Set for a run with NO backing services — 18.8% on a laptop, 22.6% with the
# Postgres and Redis that CI provides, because the storage integration tests
# take that package from 3.6% to 38%.
#
# Which means the floor cannot police the thing it most looks like it polices.
# If the service containers or their environment variables ever went missing,
# every one of those tests would skip, coverage would fall to the laptop number,
# and the floor would still pass. That job belongs to the tests themselves: with
# CI set and no DSN they FAIL rather than skip. The floor is for gradual
# erosion; the guard is for the suite disappearing.
COVER_MIN ?= 18.0

.PHONY: check build test itest cover lint golint fmt sql vuln run clean

## check: everything CI runs
check: fmt build lint golint sql test cover

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
	 awk -v t="$$total" -v m="$(COVER_MIN)" 'BEGIN { \
	   if (t+0 < m+0) { printf "coverage %.1f%% is below the %.1f%% floor\n", t, m; exit 1 } \
	   printf "coverage %.1f%% (floor %.1f%%)\n", t, m }'

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

sql:
	@$(PYTHON) scripts/sqllint.py

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
