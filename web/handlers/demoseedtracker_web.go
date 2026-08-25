package handlers

import (
	"github.com/the-loon-clan/loon-site/internal/storage"

	"context"
	"log/slog"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon-plugins/tracker"
	"github.com/the-loon-clan/loon/schedule"

	"github.com/the-loon-clan/loon-site/internal/config"
)

// Demo data for the BitTorrent tracker.
//
// Its own file rather than another function in demoseed_web.go, because this
// seeder has to BUILD something rather than write rows. A torrent IS its info
// dictionary: the info_hash is that dictionary's SHA-1, the .torrent download
// splices the stored bytes in unchanged, and the announce path keys on the
// hash. Get the encoding wrong and the rows look perfectly correct in psql
// while every .torrent the site hands out is one no client will accept — a
// failure that surfaces on somebody else's machine, in a client, days later.
//
// So the dictionaries are not built here at all: tracker.BuildTorrent is the
// one builder in the ecosystem, exported by the plugin that owns what a torrent
// is, and tested there against the bytes it produces. This file used to hold a
// second copy of that encoder, which is a second thing to keep byte-identical
// for no gain — "the demo's torrents hash differently from the real ones" is a
// bug nobody would find by reading either copy.
//
// TWO GUARDS, AND THE FIRST ONE IS THE POINT
//
//	flavourTracker()         — no torrents unless the flavour includes a
//	                           tracker. The migration runs either way, so a site
//	                           with LOON_TRACKER unset has the tables and
//	                           nothing in them, which is the honest state.
//	COUNT(*) = 0             — the same "seed only when empty" rule every other
//	                           seeder here follows, so an operator who deleted
//	                           these on purpose does not get them back.
//
// WHY THE TORRENTS COME FROM REAL RELEASES
//
// demoseed_web.go draws a line between content an operator curates and content
// members generate, and refuses to invent the second. A torrent is on the wrong
// side of that line — somebody uploaded it — which is why nothing here invents
// a NAME. Every torrent is made from a release already in the index, and
// tracker.torrents.nzb_id records which one, so the release page's swarm panel
// and the /browse seeder counts light up against rows that genuinely exist.
//
// An index with no releases therefore seeds no torrents. That is deliberate: a
// demo with no news server configured has nothing to make a torrent OF, and an
// empty tracker is the truthful answer rather than a catalogue of invented
// titles.
//
// WHAT THE SWARM FIGURES ARE
//
// seeders/leechers/snatches are DERIVED from the accounting rows below, not
// chosen. storage.ReadTrackerSiteStats already refuses to answer the same
// question two ways; a listing claiming forty seeders next to an admin table
// holding two members would be exactly that, and it is the kind of number
// nobody re-checks once it looks plausible.
//
// There are no peers in Redis to match. The counters are the denormalised cache
// the listing reads, and a real announce recomputes them — so what a member
// sees is a swarm that was there, which is also what they would see on a live
// tracker whose peers had all gone offline.

// demoTorrentPicks chooses the releases to make torrents from.
//
// DISTINCT ON (category_id) rather than "the newest N", because the newest N on
// a real index is fourteen episodes of the same show, and the listing this
// feeds is meant to show a range of sizes and kinds.
//
// The category filter skips two ranges on purpose. 6000–6999 is XXX (see
// catalog/categories.go), and 7000–7999 is Other — the bucket anything
// unclassified lands in, which on this index is where a handful of misfiled
// adult releases sit. A demo should not put either on its front page, and
// "skip the unclassified bucket" is a rule that keeps working as the index
// changes.
//
// The length bound drops the junk titles a real index always carries — a
// six-character release name tells a visitor nothing about what they are
// looking at.
const demoTorrentPicks storage.SQL = `
	SELECT DISTINCT ON (category_id) id, title, size
	  FROM usenet.nzbs
	 WHERE size BETWEEN 500000000 AND 60000000000
	   AND length(title) BETWEEN 24 AND 90
	   AND (category_id < 6000 OR category_id >= 8000)
	 ORDER BY category_id, posted_at DESC NULLS LAST`

// demoTrackerMembers is one entry per demo account, in user-id order.
//
// Not a member count pulled from the database: the numbers below are a POSITION
// — an established seeder and somebody still building a ratio — and they only
// mean anything if each account gets a different one. Extra accounts beyond
// this list get no accounting, which is correct; a member who has never
// announced has no row, and /tracker/my says so rather than showing zeroes.
//
// From/Take overlap so most torrents have more than one peer. Uploaded is
// computed as downloaded × Ratio, so the figure on the profile panel is the one
// named here rather than one that happens to fall out.
var demoTrackerMembers = []struct {
	From     int     // first torrent of the catalogue this member has
	Take     int     // how many
	Ratio    float64 // what their accounting should come out at
	Leeching int     // how many of those are still in progress
}{
	{From: 0, Take: 12, Ratio: 2.4, Leeching: 1},
	{From: 2, Take: 6, Ratio: 0.8, Leeching: 2},
}

// Minutes between one seeded accounting row's last_seen and the next, and the
// ceiling it is never allowed to pass. See trackerStatsSeed: Store.Totals
// counts seeding and leeching over a one-hour window, so a row staggered past
// it is written correctly and summarised as idle.
//
// The ceiling rather than "step × torrents is under sixty" on purpose. That
// arithmetic is true today at 14 torrents and stops being true the moment the
// picker returns more — silently, in the one direction nobody checks.
const (
	demoLastSeenStep = 4
	demoLastSeenMax  = 55
)

// demoLastSeenAgo is how many minutes ago member m last announced on torrent i.
func demoLastSeenAgo(i, m int) int {
	ago := i*demoLastSeenStep + m*2
	if ago > demoLastSeenMax {
		// Clamped, not wrapped: several rows sharing a timestamp is a swarm
		// that announced together, which is ordinary. A row outside the window
		// is a member the summary card calls inactive while the table below it
		// lists what they are seeding.
		return demoLastSeenMax
	}
	return ago
}

// trackerSeed gives the tracker a catalogue and two members' accounting.
func trackerSeed(db storage.Conn, log *slog.Logger) {
	if !flavourTracker() {
		return
	}
	var n int
	if err := db.Get(&n, `SELECT COUNT(*) FROM tracker.torrents`); err != nil || n > 0 {
		return
	}

	var picks []struct {
		ID    int64  `db:"id"`
		Title string `db:"title"`
		Size  int64  `db:"size"`
	}
	if err := db.Select(&picks, demoTorrentPicks); err != nil {
		log.Warn("tracker seed: choosing releases", "err", err)
		return
	}
	if len(picks) == 0 {
		// Said out loud, because an empty /tracker otherwise reads as a broken
		// plugin rather than an index with nothing to make a torrent of.
		log.Info("tracker seed: no releases in the index, so the tracker starts empty")
		return
	}

	hashes := make([]string, 0, len(picks))
	for i, p := range picks {
		// The shared builder. No Files: the seeder knows a release's title and
		// size and does not parse its NZB, so the tracker models the usual rar
		// set — which is what these torrents have always described.
		t := tracker.BuildTorrent(pluginapi.MirrorRequest{
			ReleaseID: p.ID, Name: p.Title, Size: p.Size,
		})
		// added_at staggered so the listing's Added column reads as a
		// catalogue that grew rather than one that appeared at once.
		if _, err := db.Exec(`
			INSERT INTO tracker.torrents
			  (info_hash, name, size, piece_length, file_count, files_json,
			   info_bytes, uploaded_by, nzb_id, added_at)
			SELECT $1, $2, $3, $4, $5, $6::jsonb, $7, u.id, $8,
			       now() - ($9 || ' hours')::interval
			  FROM (SELECT id FROM users ORDER BY id OFFSET $10 LIMIT 1) u
			ON CONFLICT (info_hash) DO NOTHING`,
			t.InfoHash, t.Name, t.Size, t.PieceLength, t.FileCount, t.FilesJSON,
			t.InfoBytes, p.ID, (i+1)*19, i%len(demoTrackerMembers)); err != nil {
			log.Warn("tracker seed: torrent", "title", p.Title, "err", err)
			continue
		}
		hashes = append(hashes, t.InfoHash)
	}
	if len(hashes) == 0 {
		return
	}

	var members []int64
	if err := db.Select(&members, `SELECT id FROM users ORDER BY id LIMIT 8`); err != nil {
		log.Warn("tracker seed: members", "err", err)
		return
	}
	rows := trackerStatsSeed(db, log, hashes, picks, members)

	// The swarm counters, computed from the rows just written rather than
	// picked. See the note at the top: the listing, /stats and the admin table
	// all read this, and they cannot disagree if there is one source.
	if _, err := db.Exec(`
		UPDATE tracker.torrents t
		   SET seeders  = c.seeders,
		       leechers = c.leechers,
		       snatches = c.snatches
		  FROM (SELECT info_hash,
		               count(*) FILTER (WHERE left_bytes = 0) AS seeders,
		               count(*) FILTER (WHERE left_bytes > 0) AS leechers,
		               count(*) FILTER (WHERE completed)      AS snatches
		          FROM tracker.user_stats
		         GROUP BY info_hash) c
		 WHERE c.info_hash = t.info_hash`); err != nil {
		log.Warn("tracker seed: swarm counts", "err", err)
	}
	log.Info("seeded demo tracker", "torrents", len(hashes), "stats", rows)
}

// trackerStatsSeed writes each demo member's per-torrent accounting and returns
// how many rows it made.
//
// last_seen is staggered by a few minutes rather than set to now(): every row
// sharing one timestamp makes the "Last seen" column look like a fixture, which
// is exactly what it would be.
//
// A FEW minutes, though, and the bound is not cosmetic. Store.Totals counts
// seeding and leeching over a ONE-HOUR activity window — a torrent nobody has
// announced on in an hour is not in a swarm — so the first version of this,
// which staggered by seventeen minutes, wrote twelve correct rows and a summary
// card that read "4 / 0" above them. The table and the card disagreed, and the
// table was the one that looked right.
//
// demoLastSeenStep keeps every seeded row inside that window. Which is also
// what the data means: a client announces every half hour, so anything a member
// is actually seeding was seen within the last one.
func trackerStatsSeed(db storage.Conn, log *slog.Logger, hashes []string,
	picks []struct {
		ID    int64  `db:"id"`
		Title string `db:"title"`
		Size  int64  `db:"size"`
	}, members []int64) int {
	rows := 0
	for m, member := range members {
		if m >= len(demoTrackerMembers) {
			break
		}
		spec := demoTrackerMembers[m]
		for k := 0; k < spec.Take; k++ {
			i := spec.From + k
			if i >= len(hashes) {
				break
			}
			size := picks[i].Size
			// The last few of a member's slice are still downloading. Ordering
			// it that way rather than at random keeps the seed reproducible,
			// which matters the first time somebody asks why their numbers
			// moved.
			leeching := k >= spec.Take-spec.Leeching
			downloaded, left := size, int64(0)
			if leeching {
				downloaded = size * 2 / 5
				left = size - downloaded
			}
			// Seedtime only accrues once a download is finished, and the
			// hit-and-run rules read it, so a leeching row has none.
			seedtime := int64(0)
			if !leeching {
				seedtime = int64(3*24*3600 + i*7*3600)
			}
			if _, err := db.Exec(`
				INSERT INTO tracker.user_stats
				  (user_id, info_hash, uploaded, downloaded, seedtime,
				   left_bytes, completed, last_seen)
				VALUES ($1, $2, $3, $4, $5, $6, $7,
				        now() - ($8 || ' minutes')::interval)
				ON CONFLICT (user_id, info_hash) DO NOTHING`,
				member, hashes[i], int64(float64(downloaded)*spec.Ratio),
				downloaded, seedtime, left, !leeching,
				demoLastSeenAgo(i, m)); err != nil {
				log.Warn("tracker seed: stats", "user", member, "err", err)
				continue
			}
			rows++
		}
	}
	return rows
}

// How often the seeded swarm is nudged forward.
//
// demoSwarmWindow must match the interval internal/storage/tracker.go counts
// "now" over. It is the definition of stale here, and it is what decides which
// rows this touches at all.
const (
	demoSwarmEveryMin = 20
	demoSwarmWindow   = time.Hour
)

// demoTrackerActivity keeps the seeded swarm inside the window the tracker's
// "now" figures are counted over.
//
// The seed lays down last_seen values between 0 and 55 minutes old, and every
// per-torrent seeder/leecher count is `last_seen > now() - interval '1 hour'`
// (internal/storage/tracker.go). Those two facts agree for exactly one hour.
// After that the demo tracker reads 0 seeding and 0 leeching on every torrent
// while the tables below still list who is on them — which is not a subtle
// staleness, it is a tracker that looks broken. This host had been showing it
// for eight days.
//
// The fix is a SHIFT, not a rewrite: every row moves by the same delta, so the
// spread the seeder deliberately arranged — who announced recently, who is
// trailing — survives intact, and no row's accounting is touched. Sliding the
// newest row up to now() reproduces exactly the distribution seeding created.
//
// ONLY STALE ROWS MOVE, and that is the whole safety argument: a peer that is
// genuinely announcing is inside the window by definition, so it is never
// selected and never touched.
//
// It REGENERATES the spread rather than shifting it, and that took two goes to
// get right. A shift has to anchor somewhere, and both anchors were wrong on
// real data: against the newest row overall it measured itself against a live
// announce and moved the seeded block by seconds; against the newest STALE row
// it latched onto a lone fifteen-hour-old row sitting between the live peers
// and the block, and left the block a day out. Timestamps in this table arrive
// in arbitrary clusters, so nothing derived from one row survives contact with
// them. Rebuilding the ladder by RANK does: the order of who announced most
// recently is preserved, and every stale row lands inside the window whatever
// shape it was in.
//
// The step and the ceiling are the SEEDER'S own constants, passed as
// parameters, so the two halves cannot drift into disagreeing about what the
// demo swarm looks like — and the ceiling reproduces its clamp, which is what
// keeps a large stale set from spilling back out of the window.
//
// DEMO SEEDING, like everything else in this file: it fabricates activity and
// belongs to the reference host, not to a real deployment.
func demoTrackerActivity(db storage.Conn, log *slog.Logger) {
	if !flavourTracker() || !db.Valid() {
		return
	}
	res, err := db.Exec(`
		UPDATE tracker.user_stats s
		   SET last_seen = now() - (LEAST(r.rn * $2, $3) || ' minutes')::interval
		  FROM (SELECT user_id, info_hash,
		               row_number() OVER (ORDER BY last_seen DESC) - 1 AS rn
		          FROM tracker.user_stats
		         WHERE last_seen < now() - $1::interval) r
		 WHERE s.user_id = r.user_id AND s.info_hash = r.info_hash`,
		demoSwarmWindow.String(), demoLastSeenStep, demoLastSeenMax)
	if err != nil {
		// Warn, never fail: a demo whose swarm went stale is worse-looking than
		// one that logged why, and neither is worth failing a boot over.
		log.Warn("demo tracker activity", "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Info("demo tracker activity: seeded swarm nudged forward", "rows", n)
	}
}

// wireDemoTrackerActivity registers the nudge as a visible job, so an operator
// reading /admin/jobs finds out the demo is doing this rather than wondering
// why the swarm never ages.
func wireDemoTrackerActivity(db storage.Conn, log *slog.Logger) {
	if !flavourTracker() {
		return
	}
	job := schedule.RegisterJob("Demo tracker activity",
		"Keeps the SEEDED tracker swarm inside the one-hour window the seeder/leecher counts are measured over, so the demo does not read as a dead tracker. Demo data only: it stops the moment a real peer announces.")
	job.IntervalMin = demoSwarmEveryMin
	job.SetTrigger(func() { go demoTrackerActivity(db, log) })
	if config.RunsJobs() {
		go schedule.ServiceLoop(context.Background(), job,
			10*time.Second, demoSwarmEveryMin*time.Minute,
			func(context.Context) { demoTrackerActivity(db, log) })
	}
}
