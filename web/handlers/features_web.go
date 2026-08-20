package handlers

import (
	"context"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-site/internal/storage"
	"github.com/the-loon-clan/loon/core"
)

// Feature flags — the host half of core's feature system.
//
// Plugins declare what is switchable (core.RegisterFeature); this decides what
// is actually on, stores the operator's answer, and serves it from memory
// because core.FeatureOn is called several times per page.
//
// TWO SPEEDS, and unlike the site flavour both of them are immediate. A flavour
// change waits for a restart because it decides which plugins BOOT, and a
// booted tracker keeps its announce routes until the process goes away. A
// feature flag decides nothing at boot: every surface it governs is consulted
// at render, so a save moves the site on the next request. That is the whole
// reason this exists beside the flavour rather than inside it.
//
// What it CANNOT do is unmount a route, so a view belonging to a switched-off
// feature is refused at request time instead (see viewFeatureGate). The
// difference is invisible to a member — the link is gone and the URL 404s —
// and it is the honest reason there is no "restart to apply" note on this page.

const featureSettingPrefix = "feature."

// featureState is the decided set, mirrored in memory.
//
// An atomic.Value holding a map that is REPLACED rather than mutated: readers
// take no lock at all, which matters because this is on the render path of
// every page, several times over.
var featureState atomic.Value // map[string]bool

// featureStore is the settings table behind it.
var featureStore siteSettings

// loadFeatures restores the decided set at boot.
//
// Only the keys an operator has actually decided are stored. A feature nobody
// has touched has no row, which is what lets core fall back to the default the
// plugin shipped — and means a plugin changing its own default moves every site
// that never expressed an opinion, which is the correct behaviour for a
// default.
func loadFeatures(ctx context.Context, db storage.Conn) error {
	featureStore = siteSettings{db: db}
	featureState.Store(map[string]bool{})
	if !db.Valid() {
		return nil
	}
	rows, err := featureStore.SettingsWithPrefix(ctx, featureSettingPrefix)
	if err != nil {
		return err
	}
	decided := make(map[string]bool, len(rows))
	for k, v := range rows {
		decided[strings.TrimPrefix(k, featureSettingPrefix)] = v == "1"
	}
	featureState.Store(decided)
	return nil
}

// setFeature records an operator's decision and mirrors it.
func setFeature(ctx context.Context, key string, on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	if err := featureStore.SetSetting(ctx, featureSettingPrefix+key, v); err != nil {
		return err
	}
	// The map is replaced, never mutated: a reader mid-render must see either
	// the old set or the new one, and never a map being written to.
	old, _ := featureState.Load().(map[string]bool)
	next := make(map[string]bool, len(old)+1)
	for k, val := range old {
		next[k] = val
	}
	next[key] = on
	featureState.Store(next)
	return nil
}

// clearFeature forgets a decision, returning the feature to its declared
// default.
//
// A third state, and it earns its place: "off" and "the plugin's default,
// whatever that becomes" are different intentions, and an operator who set
// something off to try it has no way back to the second without this.
func clearFeature(ctx context.Context, key string) error {
	if err := featureStore.DeleteSetting(ctx, featureSettingPrefix+key); err != nil {
		return err
	}
	old, _ := featureState.Load().(map[string]bool)
	next := make(map[string]bool, len(old))
	for k, val := range old {
		if k != key {
			next[k] = val
		}
	}
	featureState.Store(next)
	return nil
}

// hostFeatures implements core.FeatureService.
type hostFeatures struct{}

var _ core.FeatureService = hostFeatures{}

// FeatureEnabled answers from the mirror. No lock and no I/O: this runs on the
// render path of every page, several times over.
func (hostFeatures) FeatureEnabled(key string) (bool, bool) {
	m, _ := featureState.Load().(map[string]bool)
	on, decided := m[key]
	return on, decided
}

// ── the admin page ──────────────────────────────────────────────────

// featureRow is one switch as the page draws it.
type featureRow struct {
	Key         string
	Title       string
	Description string
	Namespace   string
	// On is what the site is doing now; Decided says whether that came from
	// the operator or from the plugin's default. Both, because "off" and
	// "off because that is how it ships" are different things to be looking at
	// when deciding whether to touch it.
	On      bool
	Decided bool
	Default bool
	// Surfaces are the views and widgets this key governs, named so an
	// operator can see what a toggle actually reaches before pressing it.
	Surfaces []string
}

func (w *web) adminFeatures(c *gin.Context) {
	reg := w.registry()
	var rows []featureRow
	if reg != nil {
		surfaces := featureSurfaces(reg)
		for _, f := range reg.Features() {
			on, decided := hostFeatures{}.FeatureEnabled(f.Key)
			if !decided {
				on = f.Default
			}
			rows = append(rows, featureRow{
				Key: f.Key, Title: f.Title, Description: f.Description,
				Namespace: f.Namespace(), On: on, Decided: decided,
				Default: f.Default, Surfaces: surfaces[f.Key],
			})
		}
	}
	w.render(c, "admin_features.html", map[string]any{
		"Title":    "Features",
		"Features": rows,
		"Saved":    c.Query(querySaved) == "1",
	})
}

// featureSurfaces maps a feature key to the views and widgets it governs.
//
// Built from the registrations rather than from a list somebody maintains,
// which is the only version that stays true — and it is the difference between
// an admin page that says "thanks" and one that says which page and which card
// stop appearing.
func featureSurfaces(reg *core.Core) map[string][]string {
	out := map[string][]string{}
	for _, slot := range []core.ViewSlot{core.SlotSitePage, core.SlotAdminPage, core.SlotAdminSettings, core.SlotUserTab} {
		for _, v := range reg.AllViews(slot) {
			if v.Feature != "" {
				out[v.Feature] = append(out[v.Feature], "page: "+v.Title)
			}
		}
	}
	// Widgets() hides switched-off ones, which is exactly wrong here — this
	// page has to name what a toggle governs whether it is on or off. The
	// catalogue is read through the registry's own list instead.
	for _, wd := range reg.AllWidgets() {
		if wd.Feature != "" {
			out[wd.Feature] = append(out[wd.Feature], "widget: "+wd.Title)
		}
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}

// adminFeaturesSave applies one toggle.
//
// ONE at a time, deliberately, rather than a form of checkboxes saved together.
// A page of switches submitted as a set is one where an operator who came to
// change a single thing also silently reverts anything that changed underneath
// them since the page loaded — and these are switches that turn parts of a live
// site off.
func (w *web) adminFeaturesSave(c *gin.Context) {
	key := strings.TrimSpace(c.PostForm("key"))
	if key == "" {
		c.Redirect(303, "/admin/features")
		return
	}
	reg := w.registry()
	// Refused unless a plugin declared it. Without this the settings table
	// accumulates rows for keys nothing reads, which is how a feature that was
	// renamed keeps a stale decision nobody can see or clear.
	if reg == nil {
		c.Redirect(303, "/admin/features")
		return
	}
	if _, ok := reg.FeatureByKey(key); !ok {
		c.Redirect(303, "/admin/features")
		return
	}

	ctx := c.Request.Context()
	var err error
	switch c.PostForm("state") {
	case "on":
		err = setFeature(ctx, key, true)
	case "off":
		err = setFeature(ctx, key, false)
	default:
		err = clearFeature(ctx, key)
	}
	if err != nil {
		w.log.Error("save feature", "feature", key, "err", err)
		c.Redirect(303, "/admin/features")
		return
	}
	w.log.Info("feature toggled", "feature", key, "state", c.PostForm("state"))
	c.Redirect(303, "/admin/features?"+querySaved+"=1")
}

// viewFeatureGate refuses a plugin view whose feature is switched off.
//
// Needed because a route mounted at boot stays mounted: core.Views hides the
// view from every nav built from it, but the URL still resolves, and a page
// that is supposed to be off answering 200 to anybody who kept the link is not
// off. 404 rather than 403 — the site is not saying "you may not", it is saying
// there is no such page, which is true while the feature is off.
func (w *web) viewFeatureGate(feature string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if feature == "" || core.FeatureOn(w.registry(), feature) {
			c.Next()
			return
		}
		c.AbortWithStatus(404)
	}
}

// liveAdminNav is the admin bar with switched-off features removed.
//
// Copied rather than filtered in place: w.adminNav is built once at boot and
// shared by every request, so a filter that reordered or truncated it would be
// one request quietly editing what the next one sees.
func (w *web) liveAdminNav() []navItem {
	reg := w.registry()
	out := make([]navItem, 0, len(w.adminNav))
	for _, it := range w.adminNav {
		if core.FeatureOn(reg, it.Feature) {
			out = append(out, it)
		}
	}
	return out
}
