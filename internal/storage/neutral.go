package storage

import (
	"context"
	"time"
)

// Neutral leech: traffic that counts in NEITHER direction.
//
// The download is free and the upload earns nothing. Both halves together are
// the point -- a "neutral" torrent that still credited upload would be a ratio
// farm, which is why this is not a freeleech variant and not a multiplier.
//
// It is a RESTRICTION, and the plugin contract it feeds (pluginapi's
// PolicySource / FlagNeutral) exists because restrictions cannot be expressed
// as multipliers at all: promotions combine by "the best offer wins", so a
// source asking for upload × 0 always loses to the 1.0 floor and neutral
// silently became ordinary freeleech -- free downloads AND full upload credit,
// which is more generous than anybody asked for.
//
// Two scopes, because operators want both and they are different tools:
//
//	per torrent  an upload nobody should farm ratio on -- a re-seed of
//	             something already widely held, an internal release.
//	site-wide    a window in which nothing counts either way. The neutral
//	             equivalent of a freeleech event.

// NeutralTorrent is one torrent marked neutral, and why.
type NeutralTorrent struct {
	InfoHash string `db:"info_hash"`
	// Reason is shown to staff, not to members. An economy state with no
	// recorded reason is one nobody can safely remove later.
	Reason    string     `db:"reason"`
	SetBy     string     `db:"set_by"`
	CreatedAt time.Time  `db:"created_at"`
	ExpiresAt *time.Time `db:"expires_at"`
}

// MigrateNeutral creates the table.
func (st *Store) MigrateNeutral() error {
	_, err := st.db.Exec(
		`CREATE TABLE IF NOT EXISTS neutral_torrents (
		    info_hash  CHAR(40) PRIMARY KEY,
		    reason     TEXT NOT NULL DEFAULT '',
		    set_by     TEXT NOT NULL DEFAULT '',
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    expires_at TIMESTAMPTZ NULL
		)`)
	return err
}

// NeutralTorrents lists every marked torrent, unexpired first.
//
// Expired rows are RETURNED rather than deleted, and shown struck through: an
// operator who wonders "was this neutral last week" is asking a question the
// row can answer and a deletion cannot. A sweep can prune them later; nothing
// reads them as active because ActiveNeutralHashes filters on the same clause.
func (st *Store) NeutralTorrents(ctx context.Context) ([]NeutralTorrent, error) {
	rows := []NeutralTorrent{}
	err := st.db.SelectContext(ctx, &rows,
		`SELECT info_hash, reason, set_by, created_at, expires_at
		   FROM neutral_torrents
		  ORDER BY (expires_at IS NOT NULL AND expires_at <= now()), created_at DESC`)
	return rows, err
}

// ActiveNeutralHashes is the set the announce path checks, as a set because
// that check runs per peer per announce.
func (st *Store) ActiveNeutralHashes(ctx context.Context) (map[string]bool, error) {
	var hashes []string
	if err := st.db.SelectContext(ctx, &hashes,
		`SELECT info_hash FROM neutral_torrents
		  WHERE expires_at IS NULL OR expires_at > now()`); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(hashes))
	for _, h := range hashes {
		out[h] = true
	}
	return out, nil
}

// SetNeutralTorrent marks one torrent neutral, or updates the mark.
func (st *Store) SetNeutralTorrent(ctx context.Context, infoHash, reason, setBy string, expires *time.Time) error {
	_, err := st.db.ExecContext(ctx,
		`INSERT INTO neutral_torrents (info_hash, reason, set_by, expires_at)
		 VALUES ($1,$2,$3,$4)
		 ON CONFLICT (info_hash) DO UPDATE
		    SET reason = EXCLUDED.reason,
		        set_by = EXCLUDED.set_by,
		        expires_at = EXCLUDED.expires_at`,
		infoHash, reason, setBy, expires)
	return err
}

// ClearNeutralTorrent removes the mark entirely.
func (st *Store) ClearNeutralTorrent(ctx context.Context, infoHash string) error {
	_, err := st.db.ExecContext(ctx,
		`DELETE FROM neutral_torrents WHERE info_hash = $1`, infoHash)
	return err
}
