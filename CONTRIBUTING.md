# Contributing

## Before you push

```sh
make check
```

That is gofmt, build, vet, the SQL linter, the tests and the coverage floor —
the same target CI runs, so a green run here is a green run there. If it is red
in CI and green for you, the usual cause is a `go.work` pointing at sibling
checkouts you have and CI does not; see below.

## Working across repositories

This site depends on [loon](https://github.com/The-Loon-Clan/loon),
[loon-plugins](https://github.com/The-Loon-Clan/loon-plugins) and
[loon-baseline](https://github.com/The-Loon-Clan/loon-baseline), pinned in
`go.mod` like any other dependency so that a clone builds on its own.

To change them alongside this repository:

```sh
cp go.work.example go.work    # gitignored
```

Your working copies then take over. Two things to remember: `go.work` overrides
`go.mod` entirely for those modules, so if you start using something new from a
sibling you must `go get` it here before pushing — otherwise CI resolves a
version where it does not exist. And a change that spans repositories needs its
sibling merged and this repository's `go.mod` bumped, in that order.

## House rules

These are conventions the codebase already follows. They exist because each one
was learned the expensive way.

**SQL lives in `internal/storage`, not in a handler.** A handler decides what to
show and who may see it. If you need a query, add a method to the store. The
linter will not catch a handler doing its own SQL — that one is on review — but
it will catch a statement built from anything but constants.

**Say what a statement guarantees, beside the statement.** Several queries here
depend on a property that is invisible from the call site: an email token is
claimed and read in one statement so a double-opened link cannot apply twice; a
recovery code is marked used inside the statement that claims it so two logins
cannot both spend it; a wishlist edit carries the owner in its `WHERE` so a
lookup-then-write cannot race. Those are one rewrite away from being lost, and
a comment on the SQL is what stops it.

**An absent figure is not zero.** "0 members" and "members are not measurable
here" are different claims, and on a staff page a wrong number gets acted on.
The store returns `(value, ok)`; the page renders an em dash when `ok` is false.

**Tests should fail against the bug they describe.** A test that has never been
seen failing proves nothing. When fixing something, check the test fails on the
old code first — several tests here carry a note saying which broken version
they were verified against, and the SQL linter is itself tested against a real
injection.

**Verify behaviour, not compilation.** A green build says the code compiles. For
anything on the request path, run it: the container is one `make run` away, and
`docker compose exec db psql -U demo -d loon_demo` will tell you what actually
landed in the database. Several changes here passed every test and were still
wrong.

## Commit messages

Say what changed and *why it was wrong before*. If you found the problem by
measuring something, put the measurement in — the number is the part that is
hard to reconstruct later, and it is what tells the next reader whether your
reasoning still applies.

## Reporting something you cannot fix

An issue describing what you expected, what happened, and how to reproduce it is
worth more than a guess at the cause. If it is a security problem, do not open
an issue — see [SECURITY.md](SECURITY.md).
