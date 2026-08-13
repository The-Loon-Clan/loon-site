package storage

import (
	"context"
	"os"
	"testing"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

// Storage tests against a real Postgres.
//
// These exist because of a bug that shipped. A rename rewrote 53 struct tags
// from `db:"..."` to `st.db:"..."`, and sqlx falls back to the lowercased field
// name when it does not recognise a tag — so most kept working by coincidence
// and twelve did not: avatar_path, thread_id, when_at, release_id, used_by_name,
// nzb_id, info_hash, is_mine, is_open. Those columns silently stopped being
// scanned.
//
// Nothing caught it. The unit tests do not touch a database, the linter reads
// syntax, and the pages still rendered — because 20 of this package's 64
// statements turn an error into a zero value, so a broken query looks exactly
// like a member with no bookmarks. It took the COMPILER, months later, being
// made to read those tags for another reason.
//
// A test that executes the statement catches it in one run. That is the whole
// argument for this file: not coverage for its own sake, but that a query which
// cannot scan its own result is a defect no amount of reading finds.
//
// Skipped without LOON_TEST_DSN, so `go test ./...` still works on a laptop
// with no database. CI supplies one.

func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("LOON_TEST_DSN")
	if dsn == "" {
		t.Skip("set LOON_TEST_DSN to run storage integration tests")
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	st := New(db)
	// The site's own tables. Ordered as boot orders them: the users table has
	// to exist before anything referencing it.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS users (
		id BIGSERIAL PRIMARY KEY,
		username TEXT NOT NULL UNIQUE,
		email TEXT NOT NULL DEFAULT '',
		role INTEGER NOT NULL DEFAULT 1,
		points BIGINT NOT NULL DEFAULT 0,
		invites INTEGER NOT NULL DEFAULT 0,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		t.Fatalf("users table: %v", err)
	}
	for name, fn := range map[string]func() error{
		"site settings": st.MigrateSiteSettings,
		"follows":       st.MigrateFollows,
		"bookmarks":     st.MigrateBookmarks,
		"wishlist":      st.MigrateWishlist,
		"grabs":         st.MigrateGrabs,
		"gifts":         st.MigrateGifts,
		"invite codes":  st.MigrateInviteCodes,
		"user display":  st.MigrateUserDisplay,
		"security":      st.MigrateSecurity,
		"settings":      st.MigrateSettings,
		"profile bio":   st.MigrateProfileBio,
		"points":        st.MigratePoints,
	} {
		if err := fn(); err != nil {
			t.Fatalf("%s migrate: %v", name, err)
		}
	}

	// Start from nothing, every run.
	//
	// Not tidiness — correctness. Two of the methods under test are TOGGLES,
	// so a second run against a database the first one left behind undoes what
	// it did and the assertions invert: follows and bookmarks passed on the
	// first run and failed on the second, which is the worst way for a test to
	// be wrong. Rows are removed by hand rather than trusted to cascade,
	// because whether a foreign key cascades is exactly the sort of thing this
	// file exists to not assume.
	for _, table := range []string{
		"user_follow", "release_bookmark", "wishlist_items", "release_grab",
		"point_gifts", "invite_codes", "totp_recovery_codes",
	} {
		_, _ = db.Exec(`DELETE FROM ` + table + ` WHERE true`) // sqllint:allow table names are the literals in this slice, never input
	}
	_, _ = db.Exec(`DELETE FROM users WHERE username LIKE 'itest_%'`)
	return st
}

// seedUser returns a member's id, creating the row.
func seedUser(t *testing.T, st *Store, name string) int64 {
	t.Helper()
	var id int64
	if err := st.db.Raw().QueryRow(
		`INSERT INTO users (username, email, points, invites) VALUES ($1, $2, 100, 5)
		 ON CONFLICT (username) DO UPDATE SET points = 100, invites = 5
		 RETURNING id`, name, name+"@example.test").Scan(&id); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return id
}

// TestEveryRowTypeScans is the regression for the struct-tag bug.
//
// Each case writes a row, reads it back through the real method, and asserts
// the result is NOT empty. That assertion is the point: a tag sqlx cannot match
// makes the scan fail, the method swallow the error, and the result come back
// empty — which is indistinguishable from "no data" unless a test has put data
// there first.
func TestEveryRowTypeScans(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	alice := seedUser(t, st, "itest_alice")
	bob := seedUser(t, st, "itest_bob")

	t.Run("follows", func(t *testing.T) {
		if _, err := st.ToggleFollow(ctx, bob, alice); err != nil {
			t.Fatalf("follow: %v", err)
		}
		got := st.ListFollowers(ctx, alice)
		if len(got) == 0 {
			t.Fatal("ListFollowers returned nothing for a member who has one — " +
				"a scan failure looks exactly like this")
		}
		// The field whose tag was broken. Empty here means avatar_path did not
		// map, which is precisely what shipped.
		if got[0].Username == "" {
			t.Error("follower row scanned with an empty username")
		}
	})

	t.Run("bookmarks", func(t *testing.T) {
		if _, err := st.ToggleBookmark(ctx, alice, 12345); err != nil {
			t.Fatalf("bookmark: %v", err)
		}
		if n, ok := st.BookmarkCount(ctx, alice); !ok || n == 0 {
			t.Errorf("BookmarkCount = %d, ok=%v; want at least one", n, ok)
		}
		if ids := st.BookmarkedIDs(ctx, alice, 10); len(ids) == 0 {
			t.Error("BookmarkedIDs returned nothing after a bookmark was added")
		}
	})

	t.Run("wishlist", func(t *testing.T) {
		if err := st.AddWish(ctx, alice, "a title", "a note"); err != nil {
			t.Fatalf("AddWish: %v", err)
		}
		rows := st.ListWishlist(ctx, alice, false)
		if len(rows) == 0 {
			t.Fatal("ListWishlist returned nothing after an entry was added — " +
				"is_mine and is_open are the tags that were broken here")
		}
	})

	t.Run("grabs", func(t *testing.T) {
		st.RecordGrab(ctx, 999, alice)
		counts := st.GrabCounts(ctx, []int64{999})
		if counts[999] == 0 {
			t.Error("GrabCounts lost the grab — release_id is the tag that was broken")
		}
	})

	t.Run("gifts", func(t *testing.T) {
		if err := st.TransferPoints(ctx, alice, bob, 5, "here"); err != nil {
			t.Fatalf("TransferPoints: %v", err)
		}
		if rows := st.ListGifts(ctx, alice, 10); len(rows) == 0 {
			t.Error("ListGifts returned nothing after a transfer — when_at was broken")
		}
	})

	t.Run("invites", func(t *testing.T) {
		ok, err := st.MintInviteCode(ctx, alice, "ITEST-CODE-1", "24 hours")
		if err != nil || !ok {
			t.Fatalf("MintInviteCode = %v, %v", ok, err)
		}
		if rows := st.ListInviteCodes(ctx, alice); len(rows) == 0 {
			t.Error("ListInviteCodes returned nothing — used_by_name was broken")
		}
	})

	t.Run("security", func(t *testing.T) {
		if err := st.SetPendingTOTP(ctx, alice, "SECRET123"); err != nil {
			t.Fatalf("SetPendingTOTP: %v", err)
		}
		if got := st.ReadTOTPStatus(ctx, alice); got.Pending != "SECRET123" {
			t.Errorf("pending secret = %q, want it read back", got.Pending)
		}
	})

	t.Run("settings", func(t *testing.T) {
		if err := st.SetPrivateProfile(ctx, alice, true); err != nil {
			t.Fatalf("SetPrivateProfile: %v", err)
		}
		if !st.IsPrivateProfile(ctx, alice) {
			t.Error("privacy did not read back")
		}
		if err := st.SetBio(ctx, alice, "hello"); err != nil {
			t.Fatalf("SetBio: %v", err)
		}
		if got := st.ReadBio(ctx, alice); got != "hello" {
			t.Errorf("bio = %q, want hello", got)
		}
	})
}

// TestOwnershipIsCheckedInTheStatement is the other class worth an integration
// test: a rule that only holds if the SQL says so.
//
// A read-then-write leaves a window between "is this yours" and "change it".
// These methods put the owner in the WHERE instead, and the only way to show
// that actually happened is to try it as the wrong user against a real table.
func TestOwnershipIsCheckedInTheStatement(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	alice := seedUser(t, st, "itest_owner")
	bob := seedUser(t, st, "itest_thief")

	if err := st.AddWish(ctx, alice, "alice's wish", ""); err != nil {
		t.Fatalf("AddWish: %v", err)
	}
	var id int64
	if err := st.db.Raw().QueryRow(
		`SELECT id FROM wishlist_items WHERE user_id = $1 ORDER BY id DESC LIMIT 1`,
		alice).Scan(&id); err != nil {
		t.Fatalf("find wish: %v", err)
	}

	// Bob's delete must find nothing. Note it returns no error: a WHERE that
	// matches nothing is a refusal, and one that looked different from success
	// would tell bob the row exists.
	if err := st.RemoveWish(ctx, id, bob); err != nil {
		t.Fatalf("RemoveWish by a stranger errored: %v", err)
	}
	var still int
	if err := st.db.Raw().Get(&still,
		`SELECT count(*) FROM wishlist_items WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if still != 1 {
		t.Fatal("another member's delete removed the row — the ownership check " +
			"is not in the statement")
	}

	if err := st.RemoveWish(ctx, id, alice); err != nil {
		t.Fatalf("RemoveWish by the owner: %v", err)
	}
	if err := st.db.Raw().Get(&still,
		`SELECT count(*) FROM wishlist_items WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if still != 0 {
		t.Error("the owner's own delete did not remove the row")
	}
}

// TestRecoveryCodeCanOnlyBeSpentOnce covers the claim-and-mark statement.
//
// Two concurrent logins both matching one code reach the same UPDATE; only one
// updates a row. That is invisible from the Go code — it lives entirely in
// `used_at IS NULL` — so it can only be shown against a database.
func TestRecoveryCodeCanOnlyBeSpentOnce(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	user := seedUser(t, st, "itest_recovery")

	if err := st.ReplaceRecoveryCodes(ctx, user, []string{"hash-a", "hash-b"}); err != nil {
		t.Fatalf("ReplaceRecoveryCodes: %v", err)
	}
	codes, err := st.UnusedRecoveryCodes(ctx, user)
	if err != nil || len(codes) != 2 {
		t.Fatalf("UnusedRecoveryCodes = %d, %v; want 2", len(codes), err)
	}

	if !st.SpendRecoveryCode(ctx, codes[0].ID) {
		t.Fatal("first spend failed")
	}
	if st.SpendRecoveryCode(ctx, codes[0].ID) {
		t.Error("the same code was spent twice — used_at IS NULL is not doing its job")
	}
	left, _ := st.UnusedRecoveryCodes(ctx, user)
	if len(left) != 1 {
		t.Errorf("%d codes left, want 1", len(left))
	}
}
