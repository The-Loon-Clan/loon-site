package main

import (
	"bytes"
	"fmt"
	"html/template"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	gmhtml "github.com/yuin/goldmark/renderer/html"

	"github.com/the-loon-clan/loon/blob"
	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/wiki"
)

// Wiki (loon-plugins/wiki) host wiring — UNIT3D's wiki/* area. The plugin owns
// /wiki/* and /admin/wiki/*, and renders the HOST's templates by name through
// gin's HTML set (web/templates/plugin/, parsed by pluginTemplates()).
//
// Three seams: chrome data, a markdown renderer, and somewhere to put image
// uploads. The plugin refuses to Provision if any is nil.
//
// Like forum and news, it ships no migration — its comment says the tables live
// in prod's numbered core migrations — so the host creates them.

// wikiMigrate creates the plugin's tables (idempotent). Columns are taken from
// pg.go's own INSERT/SELECT lists rather than from the model structs, since the
// queries are what will actually fail if a column is missing.
func wikiMigrate(db *sqlx.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS wiki_topics (
		    id          SERIAL PRIMARY KEY,
		    name        TEXT NOT NULL,
		    slug        TEXT NOT NULL UNIQUE,
		    description TEXT NOT NULL DEFAULT '',
		    sort_order  INTEGER NOT NULL DEFAULT 0,
		    icon        TEXT NOT NULL DEFAULT '',
		    color       TEXT NOT NULL DEFAULT '',
		    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS wiki_posts (
		    id         SERIAL PRIMARY KEY,
		    topic_id   INTEGER NOT NULL REFERENCES wiki_topics(id) ON DELETE CASCADE,
		    title      TEXT NOT NULL,
		    slug       TEXT NOT NULL,
		    content    TEXT NOT NULL DEFAULT '',
		    created_by BIGINT NOT NULL DEFAULT 0,
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    view_count BIGINT NOT NULL DEFAULT 0
		)`,
		// A post is addressed as /wiki/:topic/:post, so the slug only has to be
		// unique WITHIN its topic.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_wiki_posts_topic_slug
		     ON wiki_posts (topic_id, slug)`,
		`CREATE INDEX IF NOT EXISTS idx_wiki_posts_recent
		     ON wiki_posts (created_at DESC)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// wikiMarkdown is goldmark configured for the wiki, wrapped so the plugin's
// Deps.Markdown signature is satisfied.
//
// The plugin notes that wiki authors are mods+, so a host "may allow richer
// markup here". We still do not: goldmark's default already refuses to pass raw
// inline HTML through (there is no WithUnsafe here), and the result is ALSO run
// through sanitizeNewsHTML. A moderator account is precisely the level a stored
// XSS payload wants to reach, and the cost of the second pass is nothing.
//
// GFM is on for tables and strikethrough, which is what a knowledge base
// actually uses. Typographer is off: smart quotes in a page full of config
// snippets and CLI flags are a liability, not a feature.
var wikiMD = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(gmhtml.WithHardWraps()),
)

func wikiMarkdown(src string) template.HTML {
	var buf bytes.Buffer
	if err := wikiMD.Convert([]byte(src), &buf); err != nil {
		// Render nothing rather than the raw source: on a page that marks its
		// result template.HTML, falling back to unrendered input would hand
		// through exactly the markup the pipeline exists to filter.
		return ""
	}
	return template.HTML(sanitizeNewsHTML(buf.String()))
}

// wikiUploadRoot is where wiki image uploads land, and wikiUploadURL is the
// path they are served under. Kept together because blob.NewLocal pairs them
// and a mismatch shows up only as a broken image at render time.
const (
	wikiUploadRoot = "data/wiki-uploads"
	wikiUploadURL  = "/uploads"
)

// wireWikiPlugin installs the SetDeps seams and serves the upload directory.
// Call after core.New and before core.Boot, like the other plugin wirings.
func wireWikiPlugin(c *core.Core, engine *gin.Engine, w *web) error {
	if db := c.Storage.DB(); db != nil {
		if err := wikiMigrate(db); err != nil {
			return fmt.Errorf("wiki migrate: %w", err)
		}
	}
	// blob.NewLocal writes under root and reports URLs under the prefix; the
	// plugin saves into a "wiki-uploads/" namespace beneath that, so the
	// on-disk path ends up data/wiki-uploads/wiki-uploads/<uuid>.<ext>. Give it
	// the parent so the served path matches what Save returns.
	root := filepath.Dir(wikiUploadRoot)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("wiki upload dir: %w", err)
	}
	engine.Static(wikiUploadURL, root)

	wiki.SetDeps(wiki.Deps{
		BaseData: func(gc *gin.Context, extra gin.H) gin.H { return w.chromeData(gc, extra) },
		Markdown: wikiMarkdown,
		Files:    blob.NewLocal(root, wikiUploadURL),
	})
	return nil
}
