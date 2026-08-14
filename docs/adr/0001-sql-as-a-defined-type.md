# 1. SQL is a defined type, so injection is a compile error

## Status

Accepted.

## Context

Every statement in this project is parameterised, and that was true before this
decision too. The problem is that "every statement is parameterised" is a claim
maintained by attention: it holds until somebody in a hurry writes

```go
db.Query("SELECT … WHERE name = '" + name + "'")
```

and the reviewer does not notice. Nothing in the language objects. The code
compiles, passes vet, passes the tests, and works — right up until a name
contains an apostrophe, or something worse.

`scripts/sqllint.py` catches the common shapes by reading the source, and it
earns its place. But a linter is a detector: it runs after the fact, it can be
skipped, and it can only find the patterns somebody thought to describe. Its
first version called `` `…` + userSearch + `…` `` a constant, because it only
examined the first character of the expression.

## Decision

`internal/storage` exposes a defined string type and accepts nothing else:

```go
type SQL string

func (c Conn) ExecContext(ctx context.Context, q SQL, args ...any) (sql.Result, error)
```

This works because of one rule in the language: an **untyped string constant**
converts to a defined string type implicitly, and a **string variable** does
not. So this compiles —

```go
c.ExecContext(ctx, `UPDATE users SET name = $1 WHERE id = $2`, name, id)
```

— and this does not, with no linter involved:

```go
q := "UPDATE users SET name = '" + name + "'"
c.ExecContext(ctx, q, …)     // cannot use q (variable of type string) as SQL
```

A concatenation of constants is still a constant, so composing static fragments
keeps working. The moment a request value enters the expression it stops being
one, and the compiler refuses.

`Conn` deliberately does **not** embed `*sqlx.DB`. Embedding would promote every
method of the underlying pool, including the ones taking a plain `string`, and
the guarantee would be bypassable by accident rather than by intent. The escape
hatch is `Raw()`, which is a word that appears in a diff.

## Consequences

**The mistake becomes unrepresentable rather than detectable.** There is no rule
to keep updated, nothing to run, and no way to skip it.

**Genuinely dynamic SQL needs `Raw()`, and that is the point.** Where a query
truly must be assembled — a search with optional filters — the code says so in
a way a reviewer can grep for. The cost is that those sites get no help from the
compiler and need the ordinary care.

**Retyping ~97 methods surfaced a bug that had been there for months.** During
the conversion the compiler rejected 53 struct tags reading `st.db:"…"` — the
residue of an earlier blanket rename of a `db` field. Twelve columns had been
silently unscanned, because sqlx falls back to the lowercased field name when a
tag is unrecognised, and the fallback happened to match for the rest. Nothing
had failed; the rows simply came back with zero values in those columns.

**The linter stays.** It catches things the type cannot: a constant that is
correct Go and wrong SQL, or a `Raw()` call that should not be one. Two
mechanisms with different blind spots, and the cheap one is not the type.
