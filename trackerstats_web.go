package main

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// Host-side READS of the tracker's tables, for the places a member expects to
// see their standing: the top bar, their profile, and the release a torrent
// came from.
//
// Nothing here writes, and nothing here lives in the tracker plugin. The plugin
// owns announce, accounting and its own pages; this is the site putting what it
// already knows where people look for it. Keeping the direction one-way is what
// lets the tracker be swapped or switched off without the chrome noticing.
//
// Every function is a no-op returning "nothing to show" when the tracker is
// off, so a caller never has to ask first — see trackerEnabled.
//
// Cross-schema by hand. The plugin's tables live in the `tracker` schema and
// the host holds a pool for its own, so names are qualified explicitly rather
// than relying on a search_path that belongs to somebody else.

// trackerTotals is a member's whole-tracker standing.
//
// Uploaded/Downloaded are raw bytes because the callers format differently: the
// top bar wants "1.2 TB" and a test wants the number.
type trackerTotals struct {
	Uploaded   int64
	Downloaded int64
	Seeding    int
	Leeching   int
	Snatched   int
}

// Ratio is Uploaded/Downloaded, matching the plugin's own definition rather
// than inventing a second one — including the two cases that look arbitrary and
// are not.
//
// A member who has downloaded nothing has no ratio to compute, so an
// upload-only member reads as their upload figure rather than +Inf, and a
// member who has done neither reads 0 rather than NaN. Both are what
// tracker.Totals.Ratio does; if that ever changes, this is the copy to fix, and
// trackerRatioMatchesPlugin is the test that says so.
func (t trackerTotals) Ratio() float64 {
	if t.Downloaded == 0 {
		if t.Uploaded == 0 {
			return 0
		}
		return float64(t.Uploaded)
	}
	return float64(t.Uploaded) / float64(t.Downloaded)
}

// RatioLabel renders the ratio the way a tracker does: two decimals, and the
// infinity sign for a member who has uploaded without ever downloading.
//
// "∞" rather than a huge number because that IS the meaning, and a member
// seeing "12884901888.00" would reasonably read it as a bug.
func (t trackerTotals) RatioLabel() string {
	if t.Downloaded == 0 {
		if t.Uploaded == 0 {
			return "—"
		}
		return "∞"
	}
	return fmt.Sprintf("%.2f", t.Ratio())
}

// readTrackerTotals aggregates one member's per-torrent rows.
//
// Returns ok=false when the tracker is off, when there is no pool, or when the
// member has never announced — the three cases where the caller should show
// nothing at all rather than a row of zeroes. A member who has genuinely
// announced and moved no bytes still gets ok=true, because "0.00" is a real
// standing and worth showing.
//
// This runs on EVERY page render for a signed-in viewer (see chromeData), so it
// is one aggregate over the (user_id, last_seen) index and nothing else. If it
// ever needs to be cheaper, cache it per request rather than making it cleverer.
func readTrackerTotals(ctx context.Context, db *sqlx.DB, userID int64) (trackerTotals, bool) {
	var t trackerTotals
	if !trackerEnabled() || db == nil || userID == 0 {
		return t, false
	}
	var rows int
	err := db.QueryRowContext(ctx, `
		SELECT count(*),
		       coalesce(sum(uploaded), 0),
		       coalesce(sum(downloaded), 0),
		       count(*) FILTER (WHERE completed),
		       count(*) FILTER (WHERE NOT completed AND left_bytes > 0),
		       count(*) FILTER (WHERE completed)
		  FROM tracker.user_stats
		 WHERE user_id = $1`, userID).
		Scan(&rows, &t.Uploaded, &t.Downloaded, &t.Seeding, &t.Leeching, &t.Snatched)
	// A missing tracker schema is the ordinary state of a host that has never
	// switched the plugin on, not an error worth a 500 on an unrelated page.
	if err != nil || rows == 0 {
		return trackerTotals{}, false
	}
	return t, true
}

// trackerSiteStats is the whole tracker at a glance, for /stats.
type trackerSiteStats struct {
	Torrents int
	Seeders  int
	Leechers int
	Peers    int
	Snatches int
	Uploaded int64
}

// readTrackerSiteStats totals the tracker across every torrent.
//
// Seeders/leechers come from the denormalised counters the announce path
// maintains, which is what the tracker's own listing shows — reading them from
// user_stats instead would give a second, slightly different answer to the same
// question, and two numbers for one fact is how a stats page loses its
// credibility.
//
// ok=false when the tracker is off or holds nothing, so /stats renders no
// section rather than a table of zeroes claiming a dead tracker.
func readTrackerSiteStats(ctx context.Context, db *sqlx.DB) (trackerSiteStats, bool) {
	var s trackerSiteStats
	if !trackerEnabled() || db == nil {
		return s, false
	}
	err := db.QueryRowContext(ctx, `
		SELECT count(*),
		       coalesce(sum(seeders), 0),
		       coalesce(sum(leechers), 0),
		       coalesce(sum(snatches), 0)
		  FROM tracker.torrents`).
		Scan(&s.Torrents, &s.Seeders, &s.Leechers, &s.Snatches)
	if err != nil || s.Torrents == 0 {
		return trackerSiteStats{}, false
	}
	s.Peers = s.Seeders + s.Leechers
	// Total uploaded is the members' side of the ledger and lives in user_stats.
	// Best-effort: the torrent figures above are the section's substance, and
	// losing one row of it is not a reason to drop the rest.
	_ = db.QueryRowContext(ctx,
		`SELECT coalesce(sum(uploaded), 0) FROM tracker.user_stats`).Scan(&s.Uploaded)
	return s, true
}

// trackerSwarm is one release's presence on the tracker.
type trackerSwarm struct {
	InfoHash string
	Seeders  int
	Leechers int
	Snatches int
}

// swarmCounts is readTrackerSwarm for a whole listing — one query for the page
// rather than one per row.
//
// Modelled on grabCounts, including the parts that matter: ids are deduplicated
// before the IN clause (a listing can carry the same release twice), and any
// problem returns nil so the caller leaves every row untouched rather than
// asserting an empty swarm on all of them.
//
// A release absent from the map has no torrent. That is most of them, which is
// why the map holds only what exists rather than a zero for every id asked.
func swarmCounts(ctx context.Context, db *sqlx.DB, releaseIDs []int64) map[int64]trackerSwarm {
	if !trackerEnabled() || db == nil || len(releaseIDs) == 0 {
		return nil
	}
	seen := make(map[int64]bool, len(releaseIDs))
	ids := make([]int64, 0, len(releaseIDs))
	for _, id := range releaseIDs {
		if id > 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	// DISTINCT ON keeps one torrent per release — the newest — matching what
	// readTrackerSwarm shows on the release page itself. Two torrents for one
	// release is unusual but permitted by the schema, and a listing that
	// silently doubled a row would be worse than picking the same one twice.
	q, args, err := sqlx.In(`
		SELECT DISTINCT ON (nzb_id) nzb_id, info_hash, seeders, leechers, snatches
		  FROM tracker.torrents
		 WHERE nzb_id IN (?)
		 ORDER BY nzb_id, added_at DESC`, ids)
	if err != nil {
		return nil
	}
	var rows []struct {
		NzbID    int64  `db:"nzb_id"`
		InfoHash string `db:"info_hash"`
		Seeders  int    `db:"seeders"`
		Leechers int    `db:"leechers"`
		Snatches int    `db:"snatches"`
	}
	if err := db.SelectContext(ctx, &rows, db.Rebind(q), args...); err != nil {
		return nil
	}
	out := make(map[int64]trackerSwarm, len(rows))
	for _, r := range rows {
		out[r.NzbID] = trackerSwarm{
			InfoHash: r.InfoHash, Seeders: r.Seeders,
			Leechers: r.Leechers, Snatches: r.Snatches,
		}
	}
	return out
}

// readTrackerSwarm reports whether a RELEASE has a torrent on this tracker, and
// how its swarm looks.
//
// Keyed on torrents.nzb_id, which the plugin fills in when a torrent is created
// from a release. A release with no torrent returns ok=false, which is the
// common case on an index of 137,000 releases and a tracker holding a handful —
// so the caller renders nothing rather than "0 seeders", which would read as a
// dead torrent rather than no torrent.
func readTrackerSwarm(ctx context.Context, db *sqlx.DB, releaseID int64) (trackerSwarm, bool) {
	var s trackerSwarm
	if !trackerEnabled() || db == nil || releaseID == 0 {
		return s, false
	}
	err := db.QueryRowContext(ctx, `
		SELECT info_hash, seeders, leechers, snatches
		  FROM tracker.torrents
		 WHERE nzb_id = $1
		 ORDER BY added_at DESC
		 LIMIT 1`, releaseID).
		Scan(&s.InfoHash, &s.Seeders, &s.Leechers, &s.Snatches)
	if err != nil {
		return trackerSwarm{}, false
	}
	return s, true
}
