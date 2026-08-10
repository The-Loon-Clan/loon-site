package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
)

// Cache-busting for the embedded stylesheets.
//
// This exists because of a real and repeatedly wasted afternoon: several CSS
// changes were deployed, verified as correct in the SERVED file, and still not
// visible in the browser. The cause was neither the CSS nor the deploy.
//
// The assets are served from an embed.FS through http.FS. An embedded file has
// a ZERO modification time, so net/http emits no Last-Modified, and nothing
// here set Cache-Control or an ETag. With no validator and no policy a browser
// falls back to heuristic caching — it may hold a stylesheet for as long as it
// likes and never ask again. The server had no way to say "this changed", so
// it did not.
//
// Two halves fix it. The URL carries a version that changes when the CONTENT
// changes, so a new build is a new URL; and /static is then allowed to be
// cached hard, because a stale copy can no longer be reached by name.

// assetVersion is a short hash of every embedded stylesheet, computed once at
// boot.
//
// Content-derived rather than a build timestamp, deliberately: a timestamp
// busts every cache on every restart, including the restarts that changed
// nothing, which trains people to hard-refresh and hides the next real
// problem. This changes only when a stylesheet does.
var assetVersion = computeAssetVersion()

func computeAssetVersion() string {
	h := sha256.New()
	sub, err := fs.Sub(siteFS, "web/static")
	if err != nil {
		return "dev"
	}
	// Walk in the FS's own order, which is deterministic — two builds of the
	// same content must produce the same version or the busting is noise.
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch strings.ToLower(path.Ext(p)) {
		case ".css", ".js":
		default:
			return nil
		}
		f, err := sub.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, _ = io.WriteString(h, p)
		_, err = io.Copy(h, f)
		return err
	})
	if err != nil {
		return "dev"
	}
	return hex.EncodeToString(h.Sum(nil))[:10]
}

// assetURL appends the version to a static path, for templates:
//
//	<link rel="stylesheet" href="{{asset "/static/css/components.css"}}">
//
// A query string rather than a hashed filename, because the files are embedded
// and referenced by their real names in half a dozen places; renaming them at
// build time would mean a manifest, which is a lot of machinery for three
// stylesheets.
func assetURL(p string) string {
	if strings.Contains(p, "?") {
		return p + "&v=" + assetVersion
	}
	return p + "?v=" + assetVersion
}

// staticCacheHeaders lets a browser keep static assets for a year.
//
// Safe ONLY because assetURL puts the content hash in the URL: a changed file
// is a different URL, so nothing cached under the old one is ever asked for
// again. Without the version this header would be the bug rather than the fix.
func staticCacheHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/static/") {
			if c.Request.URL.Query().Get("v") != "" {
				c.Header("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				// Unversioned request — an old page, or something linking the
				// bare path. Let it be cached briefly but always revalidated,
				// so a stale copy cannot outlive a deploy by more than a few
				// minutes.
				c.Header("Cache-Control", "public, max-age=300, must-revalidate")
			}
			// An ETag gives the browser something to revalidate AGAINST, which
			// the embedded FS's zero modtime never provided.
			c.Header("ETag", `W/"`+assetVersion+`"`)
			if match := c.GetHeader("If-None-Match"); match != "" && strings.Contains(match, assetVersion) {
				c.Status(http.StatusNotModified)
				c.Abort()
				return
			}
		}
		c.Next()
	}
}
