package handlers

import (
	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-site/internal/request"
)

// Request structs for the staff and admin endpoints.
//
// Split from inputs.go because that file is the member-facing forms and this
// one is the operator's; they change for different reasons and are read by
// different people. Same pattern throughout — the struct is the declaration of
// the form, request.Bind derives each key from the field name.
//
// A recurring shape here: an Action field plus the row it acts on. Those are
// closed sets, and the check is deliberately NOT in Validate — an unknown
// action means the request did not come from the page, so the handler's switch
// falls through to its default and redirects, which is the right answer and is
// already how these read.

// ── /admin/jobs/control ─────────────────────────────────────────────────

// jobControlInput is POST /admin/jobs/control.
type jobControlInput struct {
	// Name is the job's display name — "Usenet Crawler" — because that is what
	// the scheduler registers and what the page renders.
	Name   string
	Action string
}

func readJobControlInput(c *gin.Context) (jobControlInput, error) {
	var in jobControlInput
	return in, request.Bind(c, &in)
}

func (in jobControlInput) Validate() request.Errors { return nil }

// ── /admin/access ───────────────────────────────────────────────────────

// accessSaveInput is POST /admin/access — the two settings that decide who may
// read the site and who may join it.
type accessSaveInput struct {
	Registration string
	Browsing     string
	// Flavour is the site's kind — indexer, torrent or both (flavour_web.go).
	// On the access form because it lives with the site's other operating
	// modes, saved by the same single Save button.
	Flavour string
}

func readAccessSaveInput(c *gin.Context) (accessSaveInput, error) {
	var in accessSaveInput
	return in, request.Bind(c, &in)
}

// Validate says nothing, and the reason matters more than the brevity.
//
// saveAccessSettings refuses an unknown mode already, and it does so where the
// value is WRITTEN — one place, checked once. Repeating the check here would be
// two copies of a rule to keep in step, which is the same argument that keeps
// the gift limits inside TransferPoints.
func (in accessSaveInput) Validate() request.Errors { return nil }

// ── /admin/covers ───────────────────────────────────────────────────────

// coverModeInput is POST /admin/covers.
type coverModeInput struct {
	Mode string
}

func readCoverModeInput(c *gin.Context) (coverModeInput, error) {
	var in coverModeInput
	return in, request.Bind(c, &in)
}

// Validate: saveCoverMode refuses an unknown mode at the point of writing —
// "an unknown mode is a bug in the form, not a state to adopt", as the handler
// puts it.
func (in coverModeInput) Validate() request.Errors { return nil }

// ── /admin/widgets ──────────────────────────────────────────────────────

// widgetActionInput is POST /admin/widgets.
type widgetActionInput struct {
	Region string
	Slug   string
	Action string
	// Config is a widget's own settings blob, stored verbatim. Whatever a
	// widget does with it must be safe at RENDER — see the markdown widget,
	// which runs the site's sanitising renderer — so it is not trimmed or
	// inspected here.
	Config string `form:",raw"`
	// Pages is the host's rule for which pages a placement appears on.
	// Raw for the same reason Config is: it is a multi-line value an
	// operator typed, and trimming it here would eat their line breaks.
	Pages string `form:",raw"`
	// Delta moves a widget up or down. Zero means "no move", which the handler
	// treats as nothing to do.
	Delta int
}

func readWidgetActionInput(c *gin.Context) (widgetActionInput, error) {
	var in widgetActionInput
	return in, request.Bind(c, &in)
}

// Validate says nothing: the region is checked against the registered set and
// the slug against the registered widgets, both of which need the registry
// rather than the request.
func (in widgetActionInput) Validate() request.Errors { return nil }

// ── moderation queues ───────────────────────────────────────────────────

// avatarModInput is POST /moderation/avatars.
type avatarModInput struct {
	ID     int64
	Action string
}

func readAvatarModInput(c *gin.Context) (avatarModInput, error) {
	var in avatarModInput
	return in, request.Bind(c, &in)
}

func (in avatarModInput) Validate() request.Errors { return nil }

// cheatFlagInput is POST /moderation/cheat/clear.
type cheatFlagInput struct {
	ID int64
}

func readCheatFlagInput(c *gin.Context) (cheatFlagInput, error) {
	var in cheatFlagInput
	return in, request.Bind(c, &in)
}

func (in cheatFlagInput) Validate() request.Errors { return nil }

// communityVoteInput is POST /moderation/vote.
type communityVoteInput struct {
	ID int64
	// Vote is the member's own judgement, "remove" or anything else meaning
	// keep — see castVote, which stores a bool.
	Vote string
	// Staff is set only by the staff control on the same page. Empty for an
	// ordinary member's vote, and honoured only for RoleMod and above: it is a
	// different ACT rather than a bigger vote, and is recorded as one.
	Staff string
}

func readCommunityVoteInput(c *gin.Context) (communityVoteInput, error) {
	var in communityVoteInput
	return in, request.Bind(c, &in)
}

// Validate: an id that is missing or nonsense binds to zero, and the handler
// redirects rather than reporting — a vote form that arrives without its row
// did not come from the page.
func (in communityVoteInput) Validate() request.Errors { return nil }

// reportAvatarInput is POST for reporting somebody's avatar.
type reportAvatarInput struct {
	Reason string
}

func readReportAvatarInput(c *gin.Context) (reportAvatarInput, error) {
	var in reportAvatarInput
	return in, request.Bind(c, &in)
}

// Validate: reportAvatar caps the reason where it stores it.
func (in reportAvatarInput) Validate() request.Errors { return nil }

// ── undo ────────────────────────────────────────────────────────────────

// undoInput is POST /undo — a one-time token plus where to go afterwards.
type undoInput struct {
	Token string
	// Next is where to return. The handler checks it is a local path: an
	// attacker-supplied absolute URL here would make the site's own undo button
	// a redirect to anywhere.
	Next string
}

func readUndoInput(c *gin.Context) (undoInput, error) {
	var in undoInput
	return in, request.Bind(c, &in)
}

// Validate: an absent or spent token is performUndo's to judge — it is
// single-use, and single-use is a property of the row rather than of the form.
func (in undoInput) Validate() request.Errors { return nil }
