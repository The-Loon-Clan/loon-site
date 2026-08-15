package storage

import (
	"context"
	"testing"
)

// A failed read must not read as "no second factor".
//
// The login gate asks this question to decide whether to challenge. It used to
// ask `TOTPSecret() != ""`, where a database error returns "" — so a transient
// failure let an account with 2FA enabled through on a password alone, and then
// healed itself before anybody could see it had happened. Fail-open, invisible,
// and self-clearing.
//
// HasTOTP exists to make that answer impossible: it reports the error rather
// than a false, and the caller refuses the login.
func TestHasTOTPReportsAFailedReadRatherThanFalse(t *testing.T) {
	st := &Store{} // no connection at all, so every read fails

	ok, err := st.HasTOTP(context.Background(), 1)
	if err == nil {
		t.Fatal("a read that could not run reported success; the login gate " +
			"reads that as 'no second factor' and skips the challenge")
	}
	if ok {
		t.Error("HasTOTP returned true alongside an error")
	}
}

// TOTPSecret keeps discarding its error, and that stays correct for the use it
// is left with: verification. "" is not a secret anything verifies against, so
// a failed read makes the check fail rather than pass.
//
// Asserted so that if somebody later gives TOTPSecret a fallback — a cached
// value, a retry that returns a stale secret — the fail-closed property this
// depends on is not lost quietly.
func TestTOTPSecretIsEmptyWhenItCannotRead(t *testing.T) {
	st := &Store{}

	if got := st.TOTPSecret(context.Background(), 1); got != "" {
		t.Errorf("TOTPSecret returned %q from a store with no connection; "+
			"every verification path treats a non-empty return as the real "+
			"secret to compare against", got)
	}
}
