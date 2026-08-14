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

	// Shared control fields. These appear on many forms and mean the same
	// thing on all of them.
	fieldAction = "action" // which button was pressed
	fieldID     = "id"     // the row a form acts on
	fieldToken  = "token"  // a one-time token from a link or a form
	fieldNext   = "next"   // where to return after this succeeds

	// Single-field forms.
	//
	// private_profile is NOT here any more: settingsPrivacyInput.PrivateProfile
	// declares it, and request.Bind derives the key from the field name. A
	// constant beside it would be the same string written twice — which is what
	// this file exists to stop.
	fieldCode = "code" // a TOTP or recovery code

	// Query keys the templates read back to show the outcome of a redirect.
	// A handler redirects with one of these and the page renders a banner.
	queryErr   = "err"
	querySaved = "saved"
	queryDone  = "done"
	queryToken = "token"
)
