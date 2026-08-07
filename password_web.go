package main

// The one password rule this site actually has, in one place.
//
// It was in three: authflow.Flow defaulted to 8, authtoken.Flow defaulted to 8
// separately, and two templates said "at least 8 characters" in a placeholder.
// Four copies of a number that nothing checked against each other — change the
// flow's minimum and the copy keeps promising the old one, which is the version
// people believe.
//
// So the constant is set explicitly on both flows rather than left to their
// zero-value fallbacks, and handed to templates as {{pwmin}}. Raising it here
// raises the enforcement, the browser's own minlength, the strength meter's
// threshold, and the sentence under the field, together.
//
// LENGTH, and nothing else. No "must contain a digit and a symbol": composition
// rules are what produce P@ssw0rd1 — a password that satisfies every rule and
// is on the first page of every wordlist — while rejecting the long passphrase
// that is genuinely stronger. NIST SP 800-63B dropped them for that reason, and
// the strength meter in site_chrome.html scores the same way: length dominates,
// variety is a hint, and the common ones are called what they are regardless of
// how many character classes they use.
const minPasswordLen = 8
