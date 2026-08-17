package handlers

import (
	"github.com/the-loon-clan/loon-site/internal/config"
	"github.com/the-loon-clan/loon-site/internal/storage"

	"context"
	"net/http"
	"sync/atomic"
)

// Site flavour — what this deployment IS: a Usenet indexer, a BitTorrent
// tracker, or both at once. An admin setting, beside the access modes it
// lives with on /admin/access, because "what kind of site is this" is an
// operator decision and a .env variable is where operator decisions go to be
// forgotten.
//
// TWO speeds, stated on the form as plainly as here. The parts of the site
// the HOST draws — navigation, the account menu's tracker group, the seeds
// and sweeps that check the flag — follow a save immediately, through the
// same atomic-mirror pattern as the access modes. The PLUGINS follow at the
// next restart: which of them boot is decided once, in the config snapshot
// core.Boot reads, and a booted tracker keeps its announce routes mounted
// until the process goes away. A saved flavour is therefore honest twice —
// now in the chrome, and at restart in what actually runs.
//
// LOON_TRACKER did not die: it is the FIRST-BOOT default. A database with no
// flavour row adopts "both" when the env flag is set and "indexer" when it
// is not, and the answer is then WRITTEN, so the admin page tells the truth
// and the env flag stops mattering. Existing deployments keep exactly the
// site they had.

// The three flavours.
const (
	FlavourIndexer = "indexer"
	FlavourTorrent = "torrent"
	FlavourBoth    = "both"
)

const settingSiteFlavour = "site_flavour"

var (
	flavourMode  atomic.Value // string
	flavourStore siteSettings
)

// siteFlavour returns the current flavour, defaulting to indexer — the
// stance the demo has always shipped with when nothing was asked for.
func siteFlavour() string {
	s, _ := flavourMode.Load().(string)
	if s == "" {
		return FlavourIndexer
	}
	return s
}

// flavourTracker reports whether the tracker half of the site is wanted.
// Boot reads it into the tracker plugin's enabled; the runtime checks that
// used to read config.TrackerEnabled read this instead, so a save moves the
// host-drawn surfaces without a restart.
func flavourTracker() bool { return siteFlavour() != FlavourIndexer }

// flavourIndexer reports whether the indexer half is wanted. Same shape:
// boot feeds it to plugins.usenet.enabled, the chrome reads it live.
func flavourIndexer() bool { return siteFlavour() != FlavourTorrent }

// loadSiteFlavour restores the flavour at boot, deriving and PERSISTING the
// first-boot answer from LOON_TRACKER when no row exists yet. Must run
// before the plugin config snapshot is built — migrateSiteTables does, well
// ahead of it.
func loadSiteFlavour(ctx context.Context, db storage.Conn) error {
	flavourStore = siteSettings{db: db}
	v, err := flavourStore.GetSetting(ctx, settingSiteFlavour)
	if err != nil {
		return err
	}
	if v == "" {
		v = FlavourIndexer
		if config.TrackerEnabled() {
			v = FlavourBoth
		}
		if err := flavourStore.SetSetting(ctx, settingSiteFlavour, v); err != nil {
			return err
		}
	}
	flavourMode.Store(v)
	return nil
}

// saveSiteFlavour persists and mirrors the flavour. The same refusal rule as
// the access modes: an unknown value is a form bug, not a state to adopt.
func saveSiteFlavour(ctx context.Context, v string) error {
	if !validFlavour(v) {
		return http.ErrNotSupported
	}
	if err := flavourStore.SetSetting(ctx, settingSiteFlavour, v); err != nil {
		return err
	}
	flavourMode.Store(v)
	return nil
}

func validFlavour(s string) bool {
	return s == FlavourIndexer || s == FlavourTorrent || s == FlavourBoth
}
