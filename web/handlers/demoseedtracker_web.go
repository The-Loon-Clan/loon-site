package handlers

import (
	"github.com/the-loon-clan/loon-site/internal/storage"

	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/the-loon-clan/loon/bencode"
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
// So the dictionaries are built with loon/bencode, the same encoder the plugin
// uses to rebuild a torrent per member, and demoseedtracker_web_test.go reads
// the output back and checks the hash, the key order and the file lengths.
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

// Torrent shape. A release on Usenet is a rar set — an NFO, an SFV and a run of
// volumes — and a torrent made from one carries the same files, so that is what
// these describe. It also means file_count and files_json say something: a
// listing whose Files column reads 1 on every row is a column with no content.
const (
	demoNFOBytes = 4 << 10   // an NFO is a few kilobytes of ASCII art and a description
	demoSFVBytes = 1 << 10   // one CRC line per volume
	demoVolBytes = 500 << 20 // 500 MiB volumes, the common posting size
)

// Piece length bounds and target, which are the conventions every torrent
// creator follows rather than anything this project decided: powers of two
// between 256 KiB and 16 MiB, chosen so the piece COUNT stays near 1500. Too
// few pieces and a client cannot verify incrementally; too many and the
// .torrent itself becomes megabytes of hashes.
const (
	demoMinPiece    = 256 << 10
	demoMaxPiece    = 16 << 20
	demoPieceTarget = 1500
)

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
		t := buildDemoTorrent(p.Title, p.Size)
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

// demoTorrent is one built torrent, ready to insert.
type demoTorrent struct {
	InfoHash    string
	Name        string
	Size        int64
	PieceLength int64
	FileCount   int
	FilesJSON   []byte
	InfoBytes   []byte
}

// demoFile is one entry of the info dict's file list, and of files_json.
type demoFile struct {
	Path   string `json:"path"`
	Length int64  `json:"length"`
}

// buildDemoTorrent turns a release name and size into a torrent.
//
// Deterministic all the way through — the piece hashes are a SHA-1 chain seeded
// from the name — so re-running this on the same index produces the same
// info_hash rather than a second copy of every torrent.
func buildDemoTorrent(title string, size int64) demoTorrent {
	name := demoTorrentName(title)
	files := demoRarSet(name, size)
	pieceLen := demoPieceLength(size)
	pieces := demoPieces(name, int((size+pieceLen-1)/pieceLen))

	info := demoInfoDict(name, files, pieceLen, pieces)
	sum := sha1.Sum(info) //nolint:gosec // BitTorrent info hashes ARE SHA-1; this is the protocol, not a security choice
	fj, err := json.Marshal(files)
	if err != nil {
		// Marshalling a []demoFile cannot fail — every field is a string or an
		// int64 — so this is unreachable rather than handled. NULL is a legal
		// files_json and nothing renders it, so a torrent survives it.
		fj = nil
	}
	return demoTorrent{
		InfoHash:    hex.EncodeToString(sum[:]),
		Name:        name,
		Size:        size,
		PieceLength: pieceLen,
		FileCount:   len(files),
		FilesJSON:   fj,
		InfoBytes:   info,
	}
}

// demoInfoDict encodes the info dictionary.
//
// The keys are written in ascending order — files, name, piece length, pieces,
// private — because bencode requires it and no client re-sorts before hashing.
// A dict out of order still parses, still stores, and hashes to something the
// tracker has never heard of.
func demoInfoDict(name string, files []demoFile, pieceLen int64, pieces []byte) []byte {
	var w bencode.Writer
	w.BeginDict()
	w.Str("files")
	w.BeginList()
	for _, f := range files {
		w.BeginDict()
		w.Str("length")
		w.Int(f.Length)
		// path is a LIST of components, not a string: it is how a torrent
		// expresses a subdirectory, and a client reading a bare string here
		// rejects the file.
		w.Str("path")
		w.BeginList()
		w.Str(f.Path)
		w.End()
		w.End()
	}
	w.End()
	w.Str("name")
	w.Str(name)
	w.Str("piece length")
	w.Int(pieceLen)
	w.Str("pieces")
	w.Bytes(pieces)
	// private (BEP 27): no DHT, no PEX, no peer exchange — the tracker is the
	// only way to find peers. Set because this IS a private tracker, and a
	// demo torrent that advertised itself as public would be teaching the
	// wrong shape.
	w.Str("private")
	w.Int(1)
	w.End()
	return w.Out()
}

// demoRarSet models the release as it sits on Usenet.
func demoRarSet(base string, size int64) []demoFile {
	rest := size - demoNFOBytes - demoSFVBytes
	if rest < demoVolBytes/8 {
		// Too small to be worth splitting. Single file, and the length is the
		// whole size so the dict still totals correctly.
		return []demoFile{{Path: base + ".bin", Length: size}}
	}
	files := []demoFile{
		{Path: base + ".nfo", Length: demoNFOBytes},
		{Path: base + ".sfv", Length: demoSFVBytes},
	}
	vols := int((rest + demoVolBytes - 1) / demoVolBytes)
	for i := range vols {
		n := int64(demoVolBytes)
		if i == vols-1 {
			// The last volume carries the remainder, so the file lengths sum
			// to exactly the release size. Off by one byte here and the
			// torrent claims a size the database disagrees with.
			n = rest - int64(vols-1)*demoVolBytes
		}
		files = append(files, demoFile{
			Path: fmt.Sprintf("%s.part%02d.rar", base, i+1), Length: n,
		})
	}
	return files
}

// demoPieceLength picks the power of two that keeps the piece count near the
// target, within the conventional bounds.
func demoPieceLength(size int64) int64 {
	pl := int64(demoMinPiece)
	for pl < demoMaxPiece && size/pl > demoPieceTarget {
		pl <<= 1
	}
	return pl
}

// demoPieces produces count × 20 bytes of piece hashes.
//
// A SHA-1 chain from the name rather than crypto/rand, and the difference
// matters: random bytes would give a different info_hash on every boot, so a
// re-seed would double the catalogue instead of colliding with itself.
//
// The hashes do not describe any real data — nothing here has data — so a
// client that actually fetched these pieces would fail to verify them. That is
// true of every torrent on a demo with no content behind it, and the alternative
// is hashing 35 GB of zeroes at boot.
func demoPieces(seed string, count int) []byte {
	if count < 1 {
		count = 1
	}
	out := make([]byte, 0, count*sha1.Size)
	h := sha1.Sum([]byte("loon-demo-torrent:" + seed)) //nolint:gosec // see buildDemoTorrent
	for range count {
		out = append(out, h[:]...)
		h = sha1.Sum(h[:]) //nolint:gosec // see buildDemoTorrent
	}
	return out
}

// demoTorrentName makes a release title safe to use as a directory name.
//
// Only the separators and control characters, not a general scrub: a release
// name is what a member recognises the torrent by, and rewriting the brackets
// and dots out of it would leave something they cannot match to the index.
func demoTorrentName(title string) string {
	name := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r < 0x20 {
			return '.'
		}
		return r
	}, strings.TrimSpace(title))
	if name == "" {
		return "release"
	}
	return name
}
