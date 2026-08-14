# 3. The module root holds only the embed

## Status

Accepted.

## Context

The runtime image is `gcr.io/distroless/static-debian12`: a static binary and CA
certificates, and no `web/` directory at all. Templates and static assets are
therefore compiled in with `//go:embed`.

`//go:embed` has a hard constraint: **it cannot reference a parent directory.**
A file at `web/handlers/assets.go` cannot embed `web/templates`, and nothing at
`cmd/loonsite/` can embed anything above it. The package declaring those
directives must sit at or above the tree it embeds.

The natural Go layout — a thin `cmd/`, everything else under `internal/` or the
root package — collides with this the moment assets need embedding from the
root.

## Decision

The module root is a library package (`package site`) whose only contents are
the `//go:embed` directives and the `fs.FS` they produce. The command moves to
`cmd/loonsite`, and is four lines. Handlers live in `web/handlers`.

```
assets.go            //go:embed of web/ — the only thing at the module root
cmd/loonsite/        the binary
web/handlers/        the HTTP layer
internal/            everything else
```

## Consequences

**The root package must stay almost empty**, and the reason is not tidiness — it
is that the root is the only place the embed can live. A test asserts the root
holds nothing else.

**`handlers.Main` is exported**, which reads oddly for what is effectively an
entry point. It is the one symbol crossing from the command to the code, and it
is exported because the command cannot live where the code does.

**The image is small and has no filesystem dependencies.** A template change
requires a rebuild — accepted, and mitigated by `compose.dev.yml`, which mounts
the tree and re-reads templates per request for UI work.

**Moving a template is a code change.** The embed patterns are in one file, so
the failure is a build error rather than a page that 500s at request time.
