package storage

import (
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"
)

// SQL is a statement written as a constant.
//
// This is the type that makes injection a COMPILE error rather than a review
// finding, and it works because of one rule in the language: an untyped string
// constant converts to a defined string type implicitly, and a string variable
// does not.
//
//	db.ExecContext(ctx, `UPDATE users SET bio = $1 WHERE id = $2`, bio, id)  // fine
//	db.ExecContext(ctx, "SELECT * FROM users WHERE name = '"+name+"'")       // does not compile
//
//	cannot use "SELECT ... '" + name + "'" (value of type string)
//	as SQL value in argument to ExecContext
//
// Constant folding still works, so `WHERE ` + someConst stays legal — that is
// a compile-time constant and cannot carry a request value. Only a runtime
// string is refused, which is exactly the set that can.
//
// PREPARED STATEMENTS ARE NOT THE ANSWER HERE, and it is worth saying why,
// because the instinct is a good one. A parameterised query already sends the
// statement and its values as separate protocol messages — lib/pq uses the
// extended query protocol for anything with parameters — so the values can
// never become part of the statement, prepared or not. Calling Prepare()
// explicitly buys connection-state management and no safety. What is actually
// lost, every time, is the SQL TEXT being assembled from input, and that is
// what this type prevents.
//
// The escape hatch is a conversion, SQL(x). It cannot be removed from the
// language, but it is explicit, greppable, and scripts/sqllint.py fails on it
// without a reasoned // sqllint:allow.
type SQL string

// Conn is a database handle that only accepts constant SQL.
//
// It deliberately does NOT embed *sqlx.DB. Embedding would promote the real
// ExecContext, QueryContext and the rest, which take plain strings — so every
// protection above could be bypassed by accident, by somebody who did not know
// there was anything to bypass. Each method below is written out for that
// reason.
type Conn struct{ raw *sqlx.DB }

// Wrap returns a Conn over a pool.
func Wrap(db *sqlx.DB) Conn { return Conn{raw: db} }

// Raw exposes the underlying pool.
//
// For the few things that are not statements — passing the handle to a plugin
// that owns its own schema, or to loon-baseline's stores. NOT for queries: a
// query written through here is one this type cannot check, which is the whole
// point of the type.
func (c Conn) Raw() *sqlx.DB { return c.raw }

// Valid reports whether there is a pool behind this handle.
func (c Conn) Valid() bool { return c.raw != nil }

func (c Conn) GetContext(ctx context.Context, dest any, q SQL, args ...any) error {
	return c.raw.GetContext(ctx, dest, string(q), args...)
}

func (c Conn) SelectContext(ctx context.Context, dest any, q SQL, args ...any) error {
	return c.raw.SelectContext(ctx, dest, string(q), args...)
}

func (c Conn) ExecContext(ctx context.Context, q SQL, args ...any) (sql.Result, error) {
	return c.raw.ExecContext(ctx, string(q), args...)
}

func (c Conn) Exec(q SQL, args ...any) (sql.Result, error) {
	return c.raw.Exec(string(q), args...)
}

func (c Conn) QueryRowContext(ctx context.Context, q SQL, args ...any) *sql.Row {
	return c.raw.QueryRowContext(ctx, string(q), args...)
}

// Get and Select are the non-context forms, for the seeding and migration
// paths that run before there is a request to carry one.
func (c Conn) Get(dest any, q SQL, args ...any) error {
	return c.raw.Get(dest, string(q), args...)
}

func (c Conn) Select(dest any, q SQL, args ...any) error {
	return c.raw.Select(dest, string(q), args...)
}

// QueryContext returns raw rows, for the few reads that scan by hand.
func (c Conn) QueryContext(ctx context.Context, q SQL, args ...any) (*sql.Rows, error) {
	return c.raw.QueryContext(ctx, string(q), args...)
}

// Rebind translates $1-style placeholders for the driver in use.
func (c Conn) Rebind(q SQL) SQL { return SQL(c.raw.Rebind(string(q))) }

// BeginTxx starts a transaction that carries the same restriction.
func (c Conn) BeginTxx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	tx, err := c.raw.BeginTxx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &Tx{raw: tx}, nil
}

// Tx is Conn's equivalent inside a transaction.
//
// Separate rather than shared through an interface because the two differ in
// what they can do — a Tx commits and rolls back — and an interface wide enough
// for both would have to include those, which a Conn cannot honour.
type Tx struct{ raw *sqlx.Tx }

func (t *Tx) GetContext(ctx context.Context, dest any, q SQL, args ...any) error {
	return t.raw.GetContext(ctx, dest, string(q), args...)
}

func (t *Tx) SelectContext(ctx context.Context, dest any, q SQL, args ...any) error {
	return t.raw.SelectContext(ctx, dest, string(q), args...)
}

func (t *Tx) ExecContext(ctx context.Context, q SQL, args ...any) (sql.Result, error) {
	return t.raw.ExecContext(ctx, string(q), args...)
}

func (t *Tx) QueryRowContext(ctx context.Context, q SQL, args ...any) *sql.Row {
	return t.raw.QueryRowContext(ctx, string(q), args...)
}

func (t *Tx) Commit() error   { return t.raw.Commit() }
func (t *Tx) Rollback() error { return t.raw.Rollback() }
