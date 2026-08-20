package handlers

import (
	"sync/atomic"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Name effects — the host half of the cosmetics plugin.
//
// The plugin sells and records what somebody is wearing; everything about
// DRAWING it is here, because drawing it means a class on the user-tag and CSS
// in this site's stylesheet. The catalogue of slugs is the seam between the two
// (pluginapi.Effects), and TestEveryEffectHasCSS asserts this site's stylesheet
// covers every entry — a plugin selling an effect the host cannot draw fails
// silently, with the sale succeeding and the name rendering plain.
//
// The lookup and its cache live in pluginapi rather than here, because this
// host is not the only place a username is drawn: the comments plugin renders
// its own authors, and the next plugin with a member list will render its own
// too. All of them want the same answer, and a second cache would be a second
// staleness window and a second query load.

// registry is the booted Core, held so the template helper can reach the
// extension registry.
//
// An atomic rather than a plain field because the helper runs on every request
// goroutine and is set once at boot — and because a nil read here would be a
// crash inside a template, which surfaces as a blank page rather than an error
// anybody can act on.
var registry atomic.Pointer[core.Core]

// SetCosmetics wires the booted Core for the name-effect helper.
//
// Called AFTER core.Boot, unlike the capabilities the host provides: this is
// one the host CONSUMES, and a plugin registers its own during Provision, which
// runs inside Boot. Asking earlier finds nothing and every name renders plain.
func SetCosmetics(c *core.Core) { registry.Store(c) }

// nameEffectClass returns the class to put on a rendered username, or "".
func nameEffectClass(name string) string { return slotClass(pluginapi.SlotName, name) }

// avatarEffectClass is the frame around a member's picture. Applied inside the
// avatar template itself, so every avatar on the site gets it and no call site
// changes — the same bargain user-tag makes for names.
func avatarEffectClass(name string) string { return slotClass(pluginapi.SlotAvatar, name) }

// profileEffectClass is the ground behind a profile card. One call site, since
// there is one profile page.
func profileEffectClass(name string) string { return slotClass(pluginapi.SlotProfile, name) }

func slotClass(slot, name string) string {
	c := registry.Load()
	if c == nil {
		return ""
	}
	return pluginapi.SlotClass(c, slot, name)
}

// memberTitle is a member's approved words and the effect worn on them.
//
// Returns the zero Title for almost everybody, which is what the caller checks:
// a title is bought, written and passed by staff before it exists at all.
func memberTitle(name string) pluginapi.Title {
	c := registry.Load()
	if c == nil {
		return pluginapi.Title{}
	}
	return pluginapi.MemberTitle(c, name)
}
