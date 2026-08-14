# 2. `internal/`, not `pkg/`

## Status

Accepted.

## Context

Go projects commonly grow a `pkg/` directory holding the code that is not the
command. It is a widespread convention, and it is one the toolchain knows
nothing about: `pkg/` is an ordinary directory name. Anything under it is
importable by any module in the world, whether or not that was intended.

This project is a reference host for a plugin framework, so the question is
sharper than usual. Plugin authors import `loon` and `loon-plugins`. If they can
also reach into this host's internals, then every internal helper becomes part
of a public API by accident, and changing one breaks somebody.

## Decision

Everything that is not the command or the embed lives under `internal/`, which
the **compiler** enforces: a package under `internal/` can only be imported by
code rooted in the parent of that `internal/` directory.

`pkg/` is not used at all.

## Consequences

**The boundary is checked rather than agreed.** An outside import of
`internal/storage` does not compile. No convention to explain, no review comment
to remember.

**What is genuinely public is obvious by elimination**: the module root (the
embed), `cmd/loonsite`, and `plugins/guestbook` as a worked example. Everything
else is ours.

**Splitting the host into several modules later would be work.** `internal/` is
scoped to the module, so extracting a package that other repositories want means
moving it out and giving it a real API. That is the correct amount of friction:
it makes "should this be public?" a decision somebody makes deliberately.

**It differs from the production indexer**, which keeps a `pkg/config`. The
difference is deliberate and noted where the two diverge, so a reader moving
between them is not surprised.
