package handlers

import (
	"context"
	"strings"
	"testing"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/core"
)

// The declarer seam's one silent failure mode is type identity: Lookup hands
// back an any, and a plugin's assertion to pluginapi.I18nDeclarer matches
// IDENTICAL types only — a host that registered its own func type would hand
// every plugin a value that asserts to nothing, and the plugin's guarded
// Lookup would read that as "host has no catalogue". This walks the real
// registration through a real registry and asserts as a plugin would.
func TestI18nDeclarerSeamAssertsAsPluginsWill(t *testing.T) {
	c := &core.Core{}
	if err := registerI18nSeams(c, &web{}); err != nil {
		t.Fatalf("registerI18nSeams: %v", err)
	}
	v, ok := c.Lookup(pluginapi.I18nDeclarerName)
	if !ok {
		t.Fatalf("nothing registered under %q", pluginapi.I18nDeclarerName)
	}
	if _, ok := v.(pluginapi.I18nDeclarer); !ok {
		t.Fatalf("registered value is %T — a plugin's assertion to pluginapi.I18nDeclarer fails, "+
			"which reads as 'no catalogue' rather than as an error", v)
	}
}

// A malformed slug refuses the WHOLE batch before anything is written — the
// contract's promise that a bad declaration is a loud Provision failure, not
// a partial seed. Proven on a zero web: validation runs before any storage
// call, so if a write slipped ahead of it this test would panic on the nil
// store rather than fail politely.
func TestDeclareI18nRefusesTheBatchOnOneBadSlug(t *testing.T) {
	w := &web{}
	err := w.declareI18n(context.Background(), map[string]string{
		"ach.fine.title": "Fine",
		"Not A Slug":     "refused",
	})
	if err == nil {
		t.Fatal("a batch with a malformed slug was accepted")
	}
	if !strings.Contains(err.Error(), "Not A Slug") {
		t.Fatalf("the error does not name the offender: %v", err)
	}
}
