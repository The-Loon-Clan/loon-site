package storage

import "github.com/jmoiron/sqlx"

// Store is the data-access layer's handle on the database.
//
// It exists to delete a pattern rather than to add one. Eight package-level
// `var XxxDB *sqlx.DB` globals were assigned during boot and then defensively
// checked at every use — 44 `if XxxDB == nil` guards across this package.
// Each of those guards returned a zero value, so a store that was never wired
// looked exactly like a user with no bookmarks, no follows and no invites: an
// empty page, no error, nothing in the log.
//
// A handle that arrives through the constructor cannot be nil by the time a
// method runs, so the guards are not merely redundant, they are unwriteable.
// The failure they were defending against now happens at boot, at the call to
// New, where it is a compile-time or startup concern rather than a silently
// empty page months later.
//
// This also unifies the package. Some functions already took `db *sqlx.DB`
// explicitly (the tracker reads, the gift transfer) while others read the
// global, so the same package answered the question "where does the database
// come from?" two different ways depending on which file you opened.
type Store struct {
	db *sqlx.DB
}

// New returns a Store over db.
//
// db must be non-nil: every method assumes it. That is the point — the check
// moves here, once, from 44 call sites that could only report the problem as
// an empty result.
func New(db *sqlx.DB) *Store {
	if db == nil {
		panic("storage.New: nil database handle")
	}
	return &Store{db: db}
}

// DB exposes the underlying handle for the few callers that still need one —
// migrations and the seeding paths that run before the site serves anything.
// Not for queries: those belong on this type as methods.
func (st *Store) DB() *sqlx.DB { return st.db }
