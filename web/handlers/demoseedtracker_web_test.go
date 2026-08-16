package handlers

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"sort"
	"testing"

	"github.com/the-loon-clan/loon/bencode"

	"github.com/the-loon-clan/loon-plugins/tracker"
)

// The seeded torrents, read back the way a client would read them.
//
// Every check here exists because the failure it catches is INVISIBLE from the
// database. A torrent whose keys are out of order, whose piece count does not
// match its length, or whose info dict was re-encoded on the way out still
// stores as a perfectly ordinary row — and the first person to notice is
// somebody whose client says the tracker does not know this torrent, on a
// machine nobody here can see.

// A spread of real sizes: the smallest that still splits into volumes, one in
// the middle, and the largest the seeder's picker will accept.
var demoTorrentSizes = []int64{
	632 * 1000 * 1000,
	4728 * 1000 * 1000,
	35 * 1000 * 1000 * 1000,
	// Below the split threshold, so this one takes the single-file branch.
	demoVolBytes / 16,
}

func TestBuildDemoTorrentIsAValidTorrent(t *testing.T) {
	const title = "Some.Release.2024.1080p.WEB-DL.DDP5.1.H.264-GROUP"
	for _, size := range demoTorrentSizes {
		dt := buildDemoTorrent(title, size)

		// 1. The hash is the hash OF the bytes. If these ever disagree the
		//    announce path looks up a torrent nobody can download.
		sum := sha1.Sum(dt.InfoBytes)
		if want := hex.EncodeToString(sum[:]); dt.InfoHash != want {
			t.Errorf("size %d: info_hash %s is not the SHA-1 of info_bytes (%s)",
				size, dt.InfoHash, want)
		}

		keys, err := bencode.ScanTopDict(dt.InfoBytes)
		if err != nil {
			t.Fatalf("size %d: info dict does not parse: %v", size, err)
		}

		// 2. Keys ascend. BEP-3 requires it, bencode.Writer does not sort (it
		//    cannot — it has no idea when a dict is finished), and a client
		//    hashes what it was given without re-sorting. So the encoder here
		//    is the only thing keeping this true, which is why it is tested
		//    rather than assumed.
		order := make([]string, 0, len(keys))
		for k := range keys {
			order = append(order, k)
		}
		sort.Slice(order, func(i, j int) bool {
			return keys[order[i]].Start < keys[order[j]].Start
		})
		if !sort.StringsAreSorted(order) {
			t.Errorf("size %d: info dict keys are not in ascending order: %v", size, order)
		}

		// 3. A private tracker's torrents say so — no DHT, no PEX (BEP 27).
		if priv, err := bencode.DecodeInt(dt.InfoBytes, keys["private"]); err != nil || priv != 1 {
			t.Errorf("size %d: private = %d, %v; want 1", size, priv, err)
		}

		// 4. The file lengths total the release size. One byte out here and the
		//    torrent claims a size the row beside it disagrees with.
		var total int64
		files, err := bencode.DecodeList(dt.InfoBytes, keys["files"])
		if err != nil {
			t.Fatalf("size %d: files is not a list: %v", size, err)
		}
		for _, span := range files {
			fd, err := bencode.ScanDict(dt.InfoBytes, span)
			if err != nil {
				t.Fatalf("size %d: file entry is not a dict: %v", size, err)
			}
			n, err := bencode.DecodeInt(dt.InfoBytes, fd["length"])
			if err != nil {
				t.Fatalf("size %d: file length: %v", size, err)
			}
			total += n
			// path is a LIST of components. A bare string parses as bencode and
			// is rejected by clients, which is the sort of thing that only shows
			// up in somebody else's torrent client.
			if _, err := bencode.DecodeList(dt.InfoBytes, fd["path"]); err != nil {
				t.Errorf("size %d: file path is not a list: %v", size, err)
			}
		}
		if total != size {
			t.Errorf("size %d: file lengths total %d", size, total)
		}
		if len(files) != dt.FileCount {
			t.Errorf("size %d: file_count %d but %d files in the dict",
				size, dt.FileCount, len(files))
		}

		// 5. One 20-byte hash per piece, and the piece count covers the whole
		//    size. A short pieces string is the classic silent corruption: the
		//    dict parses, the hash is stable, and the client stalls at the end.
		pieces, err := bencode.DecodeString(dt.InfoBytes, keys["pieces"])
		if err != nil {
			t.Fatalf("size %d: pieces: %v", size, err)
		}
		wantPieces := int((size + dt.PieceLength - 1) / dt.PieceLength)
		if len(pieces) != wantPieces*sha1.Size {
			t.Errorf("size %d: pieces is %d bytes, want %d (%d pieces × 20)",
				size, len(pieces), wantPieces*sha1.Size, wantPieces)
		}

		// 6. files_json describes the same files, since it is what an operator
		//    or a later feature would read instead of re-parsing the dict.
		var fj []demoFile
		if err := json.Unmarshal(dt.FilesJSON, &fj); err != nil {
			t.Fatalf("size %d: files_json: %v", size, err)
		}
		if len(fj) != len(files) {
			t.Errorf("size %d: files_json has %d entries, dict has %d",
				size, len(fj), len(files))
		}
	}
}

// The end-to-end check, and the one that matters most: the .torrent a member
// downloads must announce on the hash the tracker recorded.
//
// tracker.BuildForUser splices info_bytes into an outer dict UNCHANGED — that
// is the whole reason the bytes are stored rather than the fields — and
// bencode.InfoHash re-derives the hash from the result. If the seeder ever
// produced something the splice had to touch, this is where it shows.
func TestDemoTorrentSurvivesTheDownloadPath(t *testing.T) {
	dt := buildDemoTorrent("Another.Release.S01E02.2160p.WEB-DL-GROUP", 4575*1000*1000)
	blob := tracker.BuildForUser(dt.InfoBytes, "http://localhost:8090/api/tracker/announce/deadbeef")

	sum, err := bencode.InfoHash(blob)
	if err != nil {
		t.Fatalf("the built .torrent has no readable info hash: %v", err)
	}
	if got := hex.EncodeToString(sum[:]); got != dt.InfoHash {
		t.Fatalf("the .torrent announces %s but the tracker recorded %s", got, dt.InfoHash)
	}
}

// Re-seeding must collide with itself rather than double the catalogue, which
// is only true while nothing in the build is random. crypto/rand piece hashes
// would pass every check above and fail this one.
func TestBuildDemoTorrentIsDeterministic(t *testing.T) {
	const title = "Deterministic.Release.1080p-GROUP"
	a := buildDemoTorrent(title, 3660*1000*1000)
	b := buildDemoTorrent(title, 3660*1000*1000)
	if a.InfoHash != b.InfoHash {
		t.Fatalf("two builds of the same release gave %s and %s", a.InfoHash, b.InfoHash)
	}
}

// Every seeded row must land inside the one-hour window Store.Totals counts
// seeding and leeching over.
//
// This is here because it already happened. The first stagger was seventeen
// minutes, which put nine of one member's twelve rows outside the window: the
// table listed eleven seeding, the summary card above it said four, and both
// were reading the same rows. Nothing failed — the page rendered, the numbers
// were internally consistent per query, and only a screenshot showed the two
// disagreeing.
//
// 200 torrents, far past what the picker returns, because the arithmetic was
// fine at the count it was written for.
func TestDemoAccountingStaysInsideTheActivityWindow(t *testing.T) {
	const window = 60
	for m := range demoTrackerMembers {
		for i := range 200 {
			if ago := demoLastSeenAgo(i, m); ago >= window {
				t.Fatalf("member %d torrent %d last seen %d minutes ago, outside the %d-minute window",
					m, i, ago, window)
			}
		}
	}
}

func TestDemoPieceLengthStaysInBounds(t *testing.T) {
	for _, size := range append(demoTorrentSizes, 1, 1<<40) {
		pl := demoPieceLength(size)
		if pl < demoMinPiece || pl > demoMaxPiece {
			t.Errorf("size %d: piece length %d is outside [%d, %d]",
				size, pl, demoMinPiece, demoMaxPiece)
		}
		if pl&(pl-1) != 0 {
			t.Errorf("size %d: piece length %d is not a power of two", size, pl)
		}
	}
}
