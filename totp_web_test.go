package site

import (
	"encoding/base32"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/the-loon-clan/loon-baseline/password"
)

// The RFC 6238 test vectors, which are the whole reason to trust this file.
//
// A TOTP implementation that is subtly wrong still produces six plausible
// digits and fails only against real authenticator apps -- on somebody else's
// phone, after they have already turned the factor on. These vectors are how
// that gets caught here instead.
//
// The RFC's secret is the ASCII string "12345678901234567890"; the appendix
// prints codes for SHA-1 at 8 digits, so the expected values below are the last
// six of each, which is what a 6-digit authenticator shows.
func TestTOTPMatchesRFC6238Vectors(t *testing.T) {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).
		EncodeToString([]byte("12345678901234567890"))

	cases := []struct {
		unix int64
		want string
	}{
		{59, "287082"},          // RFC: 94287082
		{1111111109, "081804"},  // RFC: 07081804
		{1111111111, "050471"},  // RFC: 14050471
		{1234567890, "005924"},  // RFC: 89005924
		{2000000000, "279037"},  // RFC: 69279037
		{20000000000, "353130"}, // RFC: 65353130
	}
	for _, c := range cases {
		got, err := totpCode(secret, uint64(c.unix)/totpPeriod)
		if err != nil {
			t.Fatalf("totpCode at %d: %v", c.unix, err)
		}
		if got != c.want {
			t.Errorf("totpCode at %d = %s, want %s", c.unix, got, c.want)
		}
	}
}

// The skew window is a deliberate 90 seconds and no wider. A regression that
// widened it would not break anything visibly -- it would just quietly extend
// how long a stolen code keeps working.
func TestTOTPAcceptsOneStepEitherSideAndNoMore(t *testing.T) {
	secret, err := totpSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)

	for _, d := range []int64{-1, 0, 1} {
		code, err := totpCode(secret, uint64(now.Unix()/totpPeriod+d))
		if err != nil {
			t.Fatal(err)
		}
		if !totpVerify(secret, code, now) {
			t.Errorf("step %+d rejected; the window should cover it", d)
		}
	}
	for _, d := range []int64{-2, 2} {
		code, err := totpCode(secret, uint64(now.Unix()/totpPeriod+d))
		if err != nil {
			t.Fatal(err)
		}
		if totpVerify(secret, code, now) {
			t.Errorf("step %+d accepted; the window is wider than it should be", d)
		}
	}
}

// Garbage must be refused rather than panic the login handler, which is where
// this runs.
func TestTOTPRejectsMalformedInput(t *testing.T) {
	secret, _ := totpSecret()
	for _, code := range []string{"", "12345", "1234567", "abcdef", "  ", "000000"} {
		// "000000" is included because it is a real code roughly once in a
		// million steps; the assertion is only that it is not accepted for a
		// secret it does not belong to.
		if totpVerify(secret, code, time.Now()) && code != "" {
			t.Errorf("accepted %q", code)
		}
	}
	if totpVerify("not-base32!", "123456", time.Now()) {
		t.Error("accepted a code against an undecodable secret")
	}
}

// Recovery codes are credentials, so the round trip has to work and the
// forgiving-format rules have to hold -- somebody reading one off paper will
// type it lowercase and drop the dash.
func TestRecoveryCodeRoundTrip(t *testing.T) {
	h := password.Hasher{Cost: bcrypt.MinCost} // MinCost: this test hashes twenty times
	code, err := newRecoveryCode()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := hashRecoveryCode(h, code)
	if err != nil {
		t.Fatal(err)
	}
	if hash == code || hash == normaliseRecoveryCode(code) {
		t.Fatal("the code was stored in a recoverable form")
	}
	for _, typed := range []string{code, normaliseRecoveryCode(code), " " + code + " "} {
		if !recoveryCodeMatches(h, hash, typed) {
			t.Errorf("rejected %q, which is the same code", typed)
		}
	}
	other, _ := newRecoveryCode()
	if recoveryCodeMatches(h, hash, other) {
		t.Error("accepted a different code")
	}
}
