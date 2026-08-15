package storage

import (
	"context"
	"testing"
)

// The per-member cap must not stop applying when the count fails.
//
// CountOpenWishes returned a bare int. A failed read gave 0, and `0 >= cap` is
// false — so the limit lifted itself exactly when the database was unhappy,
// which is when you least want a member adding unbounded rows. It returns the
// error now and the enforcing caller refuses.
func TestCountOpenWishesReportsAFailedReadRatherThanZero(t *testing.T) {
	st := &Store{} // no connection: the read cannot run

	n, err := st.CountOpenWishes(context.Background(), 1)
	if err == nil {
		t.Fatal("a count that could not run reported success; the cap check " +
			"reads that as zero open entries and lets the add through")
	}
	if n != 0 {
		t.Errorf("count = %d alongside an error, want 0", n)
	}
}
