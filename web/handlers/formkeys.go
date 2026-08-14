package handlers

// The form and query keys this host reads, named once.
//
// A key written as a bare string at the point of use is a value nobody can
// find: renaming a form field means grepping for a quoted word and hoping,
// and a typo produces an empty read rather than an error — the handler
// carries on with a zero value and the member's input is silently discarded.
//
// This is the same argument as the structured-log vocabulary in
// logkeys_test.go: the strings that cross a boundary are an interface, and an
// interface belongs somewhere it can be read as a whole.
//
// Endpoint-specific fields live on their …Input struct in inputs.go, which is
// the better home when a form has several. What is here is the vocabulary
// SHARED across handlers, plus the fields of the one-field forms where a whole
// struct would be more ceremony than the form has content.
const (
	// checked is what an HTML checkbox posts when it is ticked. An unchecked
	// box posts NOTHING — which is why every form reading these has to write
	// each known key explicitly rather than iterate what arrived.
	checked = "1"

	// The FORM-field constants are gone, all of them. Every one — action, id,
	// token, next, code, private_profile — is now a field on the …Input struct
	// for its endpoint, and request.Bind derives the wire name from the field
	// name. A constant beside a struct field declaring the same string is the
	// duplication this file exists to prevent, so when the structs arrived the
	// constants stopped earning their place and the unused check said so.

	// Query keys the templates read back to show the outcome of a redirect.
	// A handler redirects with one of these and the page renders a banner.
	queryErr   = "err"
	querySaved = "saved"
	queryDone  = "done"
	queryToken = "token"
)
