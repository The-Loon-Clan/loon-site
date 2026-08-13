package handlers

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// The avatar file sweep — the single owner of deleting an avatar file.
//
// Nothing else removes one any more. Replacing an avatar leaves the old file;
// clearing one leaves the file entirely, because undo has to be able to put it
// back (undo_web.go). Both used to delete inline, which is what made an avatar
// removal irreversible in the first place.
//
// So deletion moved here, and gained a rule it could not have inline: a file
// goes when NO row references it AND no undo record could still need it. The
// second half is the whole point. A file deleted the moment its row stopped
// pointing at it is a file the undo offer on the very next page cannot restore.
//
// The age check is belt and braces on top of that. A file written seconds ago
// may belong to an upload whose UPDATE has not landed yet, and a sweep that
// runs in that window deletes a picture nobody has finished saving.
//
// This also closes docs/BACKLOG.md's orphan note. Before it, a delete that
// failed was logged and forgotten and an account removed straight from the
// database left its file behind forever — the app image is distroless, so
// clearing them by hand needs a one-off container with a shell in it.

const (
	// sweepMinAge is how old a file must be before the sweep will consider it.
	// Comfortably longer than any request, so an upload mid-flight is never a
	// candidate.
	sweepMinAge = 30 * time.Minute

	// sweepInterval is how often it runs. Orphans cost disk and nothing else,
	// so this is a caretaker rather than a deadline.
	sweepInterval = 6 * time.Hour
)

// sweepAvatars removes avatar files nothing references and nothing can undo.
//
// Returns how many it removed. Errors on individual files are logged and
// skipped rather than aborting: one unreadable file must not stop the sweep
// reaching the rest.
func (w *web) sweepAvatars(ctx context.Context, db *sqlx.DB) (int, error) {
	if db == nil {
		return 0, nil
	}
	dir := filepath.Join(uploadRoot, "avatars")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil // nobody has uploaded one yet
		}
		return 0, err
	}

	// Everything a row still points at.
	var paths []string
	if err := db.SelectContext(ctx, &paths,
		`SELECT avatar_path FROM users WHERE avatar_path <> ''`); err != nil {
		// A read that fails must NOT be treated as "no avatars are referenced".
		// That reading deletes every file on the disk, which is the worst
		// possible response to a transient database error.
		return 0, err
	}
	live := map[string]bool{}
	for _, p := range paths {
		if n := avatarBlobName(p); n != "" {
			live[strings.TrimPrefix(n, "avatars/")] = true
		}
	}

	// Everything an unexpired undo record could still restore. Same rule: a
	// failed read means "keep everything", never "delete everything".
	var undoPaths []string
	if w.db() != nil {
		if err := w.db().SelectContext(ctx, &undoPaths, `
			SELECT payload->>'path' FROM undo_actions
			 WHERE kind = $1 AND used_at IS NULL AND expires_at > now()
			   AND payload->>'path' IS NOT NULL`, undoKindAvatar); err != nil {
			return 0, err
		}
	}
	for _, p := range undoPaths {
		if n := avatarBlobName(p); n != "" {
			live[strings.TrimPrefix(n, "avatars/")] = true
		}
	}

	cutoff := time.Now().Add(-sweepMinAge)
	removed := 0
	for _, e := range entries {
		if e.IsDir() || live[e.Name()] {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			slog.Warn("avatar sweep: could not remove", "file", e.Name(), "err", err)
			continue
		}
		removed++
	}
	return removed, nil
}

// startAvatarSweep runs the sweep on a ticker for the life of the process.
//
// A goroutine rather than a registered job, because it needs neither the job
// runner's scheduling nor its admin controls: there is no reason to trigger it
// by hand and no configuration worth exposing. If it ever grows either, it
// belongs in schedule with the rest.
//
// Runs once at startup as well, so a process that is restarted more often than
// the interval still collects.
func (w *web) startAvatarSweep(ctx context.Context, db *sqlx.DB, log *slog.Logger) {
	sweep := func() {
		n, err := w.sweepAvatars(ctx, db)
		if err != nil {
			log.Warn("avatar sweep failed", "err", err)
			return
		}
		// Purged here rather than on a timer of its own: one caretaker, not
		// two, and the records this drops are the ones the sweep just stopped
		// having to respect.
		purged, _ := w.purgeUndo(ctx)
		if n > 0 || purged > 0 {
			log.Info("avatar sweep", "files_removed", n, "undo_records_purged", purged)
		}
	}
	go func() {
		sweep()
		t := time.NewTicker(sweepInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sweep()
			}
		}
	}()
}
