package handlers

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/trackerdir"
	"github.com/the-loon-clan/loon-plugins/trackersearch"

	"github.com/the-loon-clan/loon-site/internal/storage"
)

// Storing a private tracker's API key, which is what turns the 75 built-but-
// dormant UNIT3D trackers into live search sources.
//
// The key never comes back to the page. A saved tracker shows a masked hint
// and its last four characters, never the value: the admin who typed it does
// not need to read it again, and a shoulder over the screen must not learn it.
// Re-saving rotates the key; the form is always a fresh entry, never a
// pre-filled secret.
//
// A save reconfigures the search client LIVE (trackersearch.SetUnit3d): the
// operator sees the tracker appear in the searched set on the next gap without
// a restart. See trackerskeys reload below.

// trackerKeyRow is one configurable or configured tracker for the page.
type trackerKeyRow struct {
	Slug       string
	Name       string
	Engine     string
	Configured bool
	Enabled    bool
	Hint       string // masked key: "••••1a2b", never the value
	Domain     string
}

type trackerKeysVM struct {
	// Configured are the trackers with a stored key, enabled or not.
	Configured []trackerKeyRow
	// Available are engine trackers with no key yet, for the add form.
	Available []trackerKeyRow
	Engine    string
}

func (w *web) adminTrackerKeys(c *gin.Context) {
	vm := w.trackerKeysVM(c.Request.Context())
	w.render(c, "admin_tracker_keys.html", map[string]any{
		"Title": "Tracker keys",
		"VM":    vm,
	})
}

func (w *web) trackerKeysVM(ctx context.Context) trackerKeysVM {
	vm := trackerKeysVM{Engine: "unit3d"}
	stored := map[string]storage.TrackerKey{}
	if w.data != nil {
		if keys, err := w.data.TrackerKeys(ctx); err == nil {
			for _, k := range keys {
				stored[k.Slug] = k
			}
		}
	}
	// The family a stored key would activate: the UNIT3D trackers the adapter
	// can serve. Offering the other 500-odd would be offering keys for
	// trackers no adapter speaks to.
	for _, t := range trackerdir.WithEngine("unit3d") {
		row := trackerKeyRow{Slug: t.Slug, Name: t.Name, Engine: t.Engine}
		if len(t.Domains) > 0 {
			row.Domain = t.Domains[0]
		}
		if k, ok := stored[t.Slug]; ok {
			row.Configured = true
			row.Enabled = k.Enabled
			row.Hint = maskKey(k.APIKey)
			vm.Configured = append(vm.Configured, row)
		} else {
			vm.Available = append(vm.Available, row)
		}
	}
	sort.Slice(vm.Configured, func(i, j int) bool { return vm.Configured[i].Name < vm.Configured[j].Name })
	sort.Slice(vm.Available, func(i, j int) bool { return vm.Available[i].Name < vm.Available[j].Name })
	return vm
}

// adminTrackerKeysSave stores or updates one tracker's key, then reloads the
// client so the change is live.
func (w *web) adminTrackerKeysSave(c *gin.Context) {
	slug := strings.TrimSpace(c.PostForm("slug"))
	key := strings.TrimSpace(c.PostForm("api_key"))
	base := strings.TrimSpace(c.PostForm("base_url"))
	if slug == "" || key == "" {
		c.Redirect(http.StatusSeeOther, "/admin/tracker-keys")
		return
	}
	// Only a tracker the adapter can actually serve: a key for an unknown or
	// wrong-engine slug would be stored and never used.
	if t, ok := trackerdir.BySlug(slug); !ok || t.Engine != "unit3d" {
		c.Redirect(http.StatusSeeOther, "/admin/tracker-keys")
		return
	}
	if err := w.data.SaveTrackerKey(c.Request.Context(), storage.TrackerKey{
		Slug: slug, APIKey: key, BaseURL: base, Enabled: true,
	}); err != nil {
		w.log.Error("save tracker key", "slug", slug, "err", err)
	}
	w.reloadTrackerKeys(c.Request.Context())
	c.Redirect(http.StatusSeeOther, "/admin/tracker-keys")
}

// adminTrackerKeysToggle enables or disables a stored tracker without losing
// its key.
func (w *web) adminTrackerKeysToggle(c *gin.Context) {
	slug := strings.TrimSpace(c.PostForm("slug"))
	on := c.PostForm("enabled") == "1"
	if slug != "" {
		if err := w.data.SetTrackerKeyEnabled(c.Request.Context(), slug, on); err != nil {
			w.log.Error("toggle tracker key", "slug", slug, "err", err)
		}
		w.reloadTrackerKeys(c.Request.Context())
	}
	c.Redirect(http.StatusSeeOther, "/admin/tracker-keys")
}

// adminTrackerKeysDelete removes a tracker's key entirely.
func (w *web) adminTrackerKeysDelete(c *gin.Context) {
	slug := strings.TrimSpace(c.PostForm("slug"))
	if slug != "" {
		if err := w.data.DeleteTrackerKey(c.Request.Context(), slug); err != nil {
			w.log.Error("delete tracker key", "slug", slug, "err", err)
		}
		w.reloadTrackerKeys(c.Request.Context())
	}
	c.Redirect(http.StatusSeeOther, "/admin/tracker-keys")
}

// reloadTrackerKeys rebuilds the search client's private adapters from the
// enabled keys. Called after every change so a save is live; also at boot.
func (w *web) reloadTrackerKeys(ctx context.Context) {
	if w.trackers == nil || w.data == nil {
		return
	}
	client, ok := w.trackers.(*trackersearch.Client)
	if !ok {
		return // a host wired a different searcher; nothing to reconfigure
	}
	keys, err := w.data.EnabledTrackerKeys(ctx)
	if err != nil {
		w.log.Error("load tracker keys", "err", err)
		return
	}
	configs := make([]trackersearch.Unit3dConfig, 0, len(keys))
	for _, k := range keys {
		if t, ok := trackerdir.BySlug(k.Slug); !ok || t.Engine != "unit3d" {
			continue // a stored key whose tracker the adapter no longer serves
		}
		configs = append(configs, trackersearch.Unit3dConfig{
			Slug: k.Slug, APIKey: k.APIKey, BaseURL: k.BaseURL,
		})
	}
	client.SetUnit3d(configs)
}

// maskKey renders a stored key as a hint the admin recognises without
// revealing it: four dots and the last four characters, or all dots when the
// key is too short to spare any.
func maskKey(key string) string {
	if len(key) <= 4 {
		return strings.Repeat("•", len(key))
	}
	return "••••" + key[len(key)-4:]
}
