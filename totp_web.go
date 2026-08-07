package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 specifies HMAC-SHA1; it is not a hash of secrets
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/the-loon-clan/loon-baseline/password"
)

// TOTP — the second factor, RFC 6238, implemented rather than imported.
//
// It is about sixty lines: an HMAC of a counter, six digits off the end. A
// dependency for that would be a dependency in the login path, which is the one
// place in this codebase where a supply-chain problem is a break-in rather than
// a broken page.
//
// SHA-1 is not a mistake here. RFC 6238 specifies HMAC-SHA1 and every
// authenticator app implements it; the security comes from the shared secret,
// not from the hash's collision resistance, and "upgrading" it would produce
// codes no app can generate.
//
// The parts that actually matter for not locking people out are in totpsetup:
// enrolment is not complete until a code has been verified, and recovery codes
// exist before the factor is switched on.

const (
	// totpPeriod is the step, in seconds. Thirty is the universal default and
	// changing it silently breaks every app.
	totpPeriod = 30

	// totpDigits is the code length.
	totpDigits = 6

	// totpSkew is how many steps either side of now are accepted.
	//
	// One step, not zero: phone clocks drift, and somebody typing a code as it
	// rolls over should not be told their password is wrong. Not more than one
	// either -- every extra step widens the window a stolen code stays usable
	// in, and at ±1 that window is already 90 seconds.
	totpSkew = 1
)

// totpSecret makes a new shared secret.
//
// 20 bytes = 160 bits, which is what RFC 4226 recommends and what every app
// expects. crypto/rand, obviously: a guessable second factor is not one.
func totpSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// Unpadded base32 — the encoding authenticator apps read, and the one a
	// person can type off a screen if the QR will not scan.
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
}

// totpCode computes the code for one time step.
func totpCode(secret string, counter uint64) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).
		DecodeString(strings.ToUpper(strings.ReplaceAll(secret, " ", "")))
	if err != nil {
		return "", fmt.Errorf("bad secret")
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	// Dynamic truncation, RFC 4226 §5.3: the low nibble of the last byte picks
	// where in the digest to read the code from.
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint32(1)
	for i := 0; i < totpDigits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", totpDigits, v%mod), nil
}

// totpVerify reports whether code is valid for secret at time t.
//
// Constant-time comparison. A timing oracle on a six-digit code is a narrow
// thing to exploit, and comparing with == when the alternative is one function
// call is a choice nobody should have to defend later.
func totpVerify(secret, code string, t time.Time) bool {
	code = strings.TrimSpace(code)
	if secret == "" || len(code) != totpDigits {
		return false
	}
	counter := uint64(t.Unix()) / totpPeriod
	for d := -totpSkew; d <= totpSkew; d++ {
		want, err := totpCode(secret, uint64(int64(counter)+int64(d)))
		if err != nil {
			return false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

// totpURI builds the otpauth:// URI an authenticator app imports.
//
// The issuer appears twice by design — as a label prefix and as a parameter —
// because apps disagree about which one they read, and an entry that says only
// "alice" is useless on a phone holding twelve of them.
func totpURI(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(totpDigits))
	q.Set("period", fmt.Sprint(totpPeriod))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// totpFormatSecret groups the secret for reading aloud or typing by hand.
func totpFormatSecret(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ── recovery codes ──────────────────────────────────────────────────────────

// recoveryCodeCount is how many are issued. Ten is enough that losing a phone
// is an inconvenience rather than a support ticket, and few enough that they
// fit on the card somebody writes them on.
const recoveryCodeCount = 10

// newRecoveryCode returns one single-use code.
//
// Same alphabet and shape as an invite code, for the same reason: it survives
// being read aloud, typed from a screen, and pasted through something that
// lowercases it.
func newRecoveryCode() (string, error) {
	b := make([]byte, 5) // 40 bits
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	s := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	return s[:4] + "-" + s[4:], nil
}

// normaliseRecoveryCode makes matching forgiving about how one was typed.
func normaliseRecoveryCode(s string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), "-", ""))
}

// hashRecoveryCode stores a code the way a password is stored.
//
// Hashed, not encrypted and not plain: a recovery code IS a credential -- it
// bypasses the second factor entirely -- and a database dump handing over ten
// working bypasses per account has defeated the point of having one.
//
// Through the site's OWN hasher rather than bcrypt directly, so a recovery code
// gets the same pepper and the same cost as a password. Two hashing pipelines
// in one codebase means one of them is the one nobody remembers to update.
func hashRecoveryCode(h password.Hasher, code string) (string, error) {
	return h.Hash(normaliseRecoveryCode(code))
}

func recoveryCodeMatches(h password.Hasher, hash, code string) bool {
	return h.Check(hash, normaliseRecoveryCode(code))
}
