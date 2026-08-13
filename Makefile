# The checks, in one place, so CI and a laptop run the same thing.
#
# CI calling `make check` rather than its own list of steps is the point: a
# check that only exists in a workflow file cannot be run before pushing, and
# one that only exists locally does not stop anybody.
#
# GOWORK=off throughout. A go.work pointing at sibling checkouts is for
# development; the checks must see what a clone sees, or they pass here and
# fail for everybody else.

GO ?= go
export GOWORK = off

# Coverage floor. Deliberately just under where the project stands rather than
# an aspiration: its job is to stop a regression, and a floor nobody can meet
# gets deleted rather than met. Raise it when the number rises.
COVER_MIN ?= 15.0

.PHONY: check build test cover lint fmt sql run clean

## check: everything CI runs
check: fmt build lint sql test cover

## build: compile every package
build:
	$(GO) build ./...

## test: the full suite
test:
	$(GO) test ./...

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
	@out=$$(gofmt -l . 2>/dev/null | grep -v '^vendor/' || true); \
	 if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	@echo "gofmt clean"

## sql: refuse SQL built from anything but constants
sql:
	@python scripts/sqllint.py

## run: the site, on http://localhost:8090
run:
	docker compose up --build -d
	@echo "http://localhost:8090/  —  alice/alice (admin), bob/bob"

## clean: stop the stack, KEEPING the volumes (down -v would drop the index)
clean:
	docker compose down
	@rm -f coverage.out
