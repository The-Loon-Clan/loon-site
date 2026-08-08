package main

import (
	"context"
	"net/http"
	"os"
	"strings"
	"sync/atomic"

	"github.com/jmoiron/sqlx"
)

// Where cover art comes from, as an operator setting.
//
// Downloading and hotlinking are both defensible and the right answer depends
// on things this code cannot know: whether the deployment has disk to spare,
// whether its provider's terms prefer hotlinking (Open Library asks that public
// pages point at covers.openlibrary.org), whether visitor privacy matters more
// than storage, and whether a future provider allows scraping at all. So it is
// a choice, persisted like the access modes and mirrored into an atomic because
// it is read on every scraped cover.
//
// The two "fallback" modes are not decoration. Each single-source mode has a
// failure it cannot answer on its own — a download that fails, or a remote URL
// that is already dead — and the fallback is what stops that failure becoming a
// release with no art at all.

// Cover modes.
const (
	// CoverRemote — always store the provider's URL. Nothing is downloaded and
	// nothing is written to disk. The original behaviour, and the right one for
	// a deployment with no storage to spare or a provider that asks for it.
	CoverRemote = "remote"

	// CoverLocal — always download; if the download fails the release gets NO
	// cover. Strict, and the only mode that guarantees no third-party request
	// is ever made from a visitor's browser. Choose it when that guarantee is
	// the point and a missing image is an acceptable price.
	CoverLocal = "local"

	// CoverLocalRemote — download, and store the provider's URL if that fails.
	// The default: it prefers local art but never leaves a release blank
	// because a CDN hiccuped.
	CoverLocalRemote = "local_remote"

	// CoverRemoteLocal — store the provider's URL if it is actually reachable,
	// and download only when it is not. For a deployment that would rather
	// hotlink but does not want to store a URL that is already dead — link rot
	// is the failure hotlinking cannot see, since nothing re-checks a stored
	// URL and the break surfaces as a missing image on a page nobody is
	// looking at.
	CoverRemoteLocal = "remote_local"
)

const settingCoverMode = "cover_mode"

// coverModeVal is the live mirror: read on every scraped cover, written on save.
var coverModeVal atomic.Value // string

func init() { coverModeVal.Store(CoverLocalRemote) }

// coverMode returns the current mode, defaulting to local-with-remote-fallback.
func coverMode() string {
	s, _ := coverModeVal.Load().(string)
	if s == "" {
		return CoverLocalRemote
	}
	return s
}

// validCoverMode reports whether s is a mode the code implements. An unknown
// value is a bug in a form or a typo in the environment, never a state to
// adopt: adopting it would leave covers behaving in a way nothing here
// describes.
func validCoverMode(s string) bool {
	switch s {
	case CoverRemote, CoverLocal, CoverLocalRemote, CoverRemoteLocal:
		return true
	}
	return false
}

// coverModeLabel is the human name for a mode, for the admin page and logs.
func coverModeLabel(s string) string {
	switch s {
	case CoverRemote:
		return "Remote only (hotlink the provider)"
	case CoverLocal:
		return "Local only (download; no cover if it fails)"
	case CoverLocalRemote:
		return "Local, falling back to remote"
	case CoverRemoteLocal:
		return "Remote, falling back to local"
	}
	return s
}

// coverModes lists every mode in the order the admin page offers them:
// least local first, so the column reads as a spectrum rather than a set.
func coverModes() []string {
	return []string{CoverRemote, CoverRemoteLocal, CoverLocalRemote, CoverLocal}
}

// coverSettingsStore is the key/value store, resolved at boot.
var coverSettingsStore siteSettings

// loadCoverMode restores the setting at boot.
//
// COVER_MODE seeds the default for a fresh deployment, but a stored value wins:
// an operator who changed it in the admin page must not have that undone by an
// environment variable left over in a compose file. Same precedence as every
// other persisted setting here — the database is the answer, env is the seed.
func loadCoverMode(ctx context.Context, db *sqlx.DB) error {
	coverSettingsStore = siteSettings{db: db}
	if env := strings.TrimSpace(os.Getenv("COVER_MODE")); env != "" && validCoverMode(env) {
		coverModeVal.Store(env)
	}
	v, err := coverSettingsStore.GetSetting(ctx, settingCoverMode)
	if err != nil {
		return err
	}
	if v != "" && validCoverMode(v) {
		coverModeVal.Store(v)
	}
	return nil
}

// saveCoverMode persists and mirrors the setting.
func saveCoverMode(ctx context.Context, mode string) error {
	if !validCoverMode(mode) {
		return http.ErrNotSupported
	}
	if err := coverSettingsStore.SetSetting(ctx, settingCoverMode, mode); err != nil {
		return err
	}
	coverModeVal.Store(mode)
	return nil
}
