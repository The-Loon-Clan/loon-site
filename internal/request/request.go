// Package request makes an endpoint's input rules a named, testable thing.
//
// The problem it solves is not that ad-hoc validation is wrong — the handlers
// here validate carefully. It is that the rules for an endpoint cannot be read
// in one place, cannot be tested without building a request, and are enforced
// by whoever remembered to. UNIT3D reaches the same conclusion from the other
// side: 114 Form Request classes, one per endpoint that accepts input.
//
// The shape here is deliberately small: no struct tags, no reflection, no rule
// DSL. An input is a struct with a Validate method, and Validate is ordinary Go
// you can read top to bottom. What the package supplies is the plumbing that
// makes forgetting to call it impossible, plus the handful of checks that were
// otherwise going to be rewritten per endpoint.
package request

import (
	"fmt"
	"net/mail"
	"strings"
	"unicode/utf8"
)

// Input is a parsed request body that states its own rules.
//
// Validate returns errors keyed by form field, because these forms are
// re-rendered with the message beside the input that caused it. A nil return
// means the input is good.
//
// Messages are written to be READ BY THE PERSON WHO SUBMITTED THE FORM. They
// say what to do about it, name no internals, and quote nothing back that the
// submitter did not type — an error is an ordinary place for user input to end
// up on a page.
type Input interface {
	Validate() Errors
}

// Errors maps a form field name to the message shown beside it.
//
// A map rather than a slice: a form shows at most one message per field, and
// the natural bug with a slice is showing three messages against one input
// because three rules failed.
type Errors map[string]string

// Add records the first failure for a field and keeps it.
//
// FIRST, not last. Rules are written most-fundamental first — "you left this
// blank" before "this is not a valid address" — and the earlier message is
// almost always the more useful one.
func (e *Errors) Add(field, msg string) {
	if *e == nil {
		*e = Errors{}
	}
	if _, seen := (*e)[field]; !seen {
		(*e)[field] = msg
	}
}

// Any reports whether validation failed.
func (e Errors) Any() bool { return len(e) > 0 }

// First returns one message, for a page with a single error line rather than
// per-field placement. Deterministic across runs: map iteration order is not,
// and an error that moves between refreshes reads as a flapping site.
func (e Errors) First(order ...string) string {
	for _, f := range order {
		if m, ok := e[f]; ok {
			return m
		}
	}
	var best string
	for f, m := range e {
		if best == "" || f < best {
			best, _ = f, m
		}
	}
	return e[best]
}

// Validate runs an input's rules.
//
// The type parameter is the whole point. T is constrained to Input, so a struct
// without a Validate method cannot be passed here — that is a COMPILE error, not
// a review comment or a test. It is the same device as storage.SQL: make the
// mistake unrepresentable rather than detectable.
//
// It exists as a function, rather than leaving callers to write in.Validate(),
// so that binding and validating are one step at the call site and there is no
// arrangement of the code in which the parse happened and the check did not.
func Validate[T Input](in T) Errors { return in.Validate() }

// ── the checks that were otherwise going to be rewritten per endpoint ──

// Required reports a blank field.
//
// Takes the value already trimmed. Trimming is the caller's job because it is a
// decision, not a formality: a password made entirely of spaces is a password,
// and trimming one silently changes what the member typed.
func Required(e *Errors, field, value, label string) bool {
	if value == "" {
		e.Add(field, label+" is required.")
		return false
	}
	return true
}

// MaxRunes bounds a field by CHARACTERS, not bytes.
//
// utf8.RuneCountInString, because len() on a UTF-8 string counts bytes: a
// 30-byte limit silently becomes ten characters for anyone writing in Japanese,
// and about fifteen for anyone whose name has accents in it. The failure is
// invisible to whoever wrote the limit and constant for whoever hits it.
func MaxRunes(e *Errors, field, value, label string, max int) bool {
	if n := utf8.RuneCountInString(value); n > max {
		e.Add(field, fmt.Sprintf("%s is too long — %d characters, and the limit is %d.", label, n, max))
		return false
	}
	return true
}

// MinRunes bounds a field from below.
func MinRunes(e *Errors, field, value, label string, min int) bool {
	if n := utf8.RuneCountInString(value); n < min {
		e.Add(field, fmt.Sprintf("%s must be at least %d characters.", label, min))
		return false
	}
	return true
}

// Email checks an address is parseable.
//
// net/mail, not a regular expression. Every hand-rolled email regex rejects
// somebody's real address — a plus sign, a new TLD, a quoted local part — and
// the person on the other end has no way to argue with it.
func Email(e *Errors, field, value, label string) bool {
	if _, err := mail.ParseAddress(value); err != nil {
		e.Add(field, "That does not look like an email address.")
		return false
	}
	return true
}

// OneOf checks a value against a closed set.
//
// For selects and radios, where anything outside the set means the request did
// not come from the form — so the message says nothing about what the options
// are.
func OneOf(e *Errors, field, value, label string, allowed ...string) bool {
	for _, a := range allowed {
		if value == a {
			return true
		}
	}
	e.Add(field, "That is not one of the available options.")
	return false
}

// Matches checks two fields agree, for confirmation inputs.
func Matches(e *Errors, field, a, b, msg string) bool {
	if a != b {
		e.Add(field, msg)
		return false
	}
	return true
}

// Trim is the ordinary treatment for a text field: strip surrounding
// whitespace, which is almost always a copy-paste artefact rather than intent.
// Never use it on a password — see Required.
func Trim(s string) string { return strings.TrimSpace(s) }
