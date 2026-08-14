# Architecture decision records

Decisions that shaped this codebase, why they were made, and what they cost.

Each record is short and does not change once accepted. If a decision is
revisited, the old record stays and a new one supersedes it — a record that gets
quietly edited is a record you cannot trust to tell you what people believed at
the time, which is most of the value.

The reasoning here was not invented for these pages. It already existed, spread
across doc comments and commit messages, where it could only be found by
somebody who already knew where to look. These are the four that a reader
encounters as *surprising* — the ones where the code looks unusual and the
justification is elsewhere.

| # | Decision | Status |
| --- | --- | --- |
| [0001](0001-sql-as-a-defined-type.md) | SQL is a defined type, so injection is a compile error | Accepted |
| [0002](0002-internal-over-pkg.md) | `internal/`, not `pkg/` | Accepted |
| [0003](0003-embed-pins-the-root-package.md) | The module root holds only the embed | Accepted |
| [0004](0004-trust-no-proxy-by-default.md) | Believe no proxy unless told which one | Accepted |

## Format

Status / Context / Decision / Consequences, after Michael Nygard's original.
Consequences is the section worth writing carefully: it is where the honest
cost goes, and a record with no costs listed is advocacy rather than a record.
