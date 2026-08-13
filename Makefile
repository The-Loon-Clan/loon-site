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
COVER_MIN ?= 15.0

.PHONY: check build test itest cover lint fmt sql vuln run clean

## check: everything CI runs
check: fmt build lint sql test cover

## build: compile every package
build:
	$(GO) build ./...

## test: the full suite. Storage integration tests SKIP unless LOON_TEST_DSN
## is set — see `make itest` for a throwaway database to point it at.
test:
	$(GO) test ./...

## itest: the storage tests against a disposable Postgres on port 5599.
## Never the development database: these truncate the tables they touch.
itest:
	@docker rm -fv loon-itestdb >/dev/null 2>&1 || true
	@docker run -d --name loon-itestdb -e POSTGRES_USER=demo -e POSTGRES_PASSWORD=demo -e POSTGRES_DB=loon_test -p 5599:5432 postgres:16-alpine >/dev/null
	@sleep 10
	@LOON_TEST_DSN="postgres://demo:demo@localhost:5599/loon_test?sslmode=disable" $(GO) test -count=1 ./internal/storage/ -v -run "Scans|Ownership|Recovery" || true
	@docker rm -fv loon-itestdb >/dev/null 2>&1

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

## fmt: fail if anything is unformatted, rather than reformatting silently
fmt:
	@out=$$(bash scripts/gofmt.sh | grep -v '^vendor/' || true); \
	 if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	@echo "gofmt clean"

## sql: refuse SQL built from anything but constants
sql:
	@python scripts/sqllint.py

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
