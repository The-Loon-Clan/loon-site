package handlers

import (
	"github.com/the-loon-clan/loon-site/internal/config"
	"github.com/the-loon-clan/loon-site/internal/storage"

	"context"
	"fmt"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/core"
)

// Member statistics for automatic rank promotion — the HOST's half.
//
// The ranks plugin owns the ladder and the rules; it cannot own the FIGURES.
// A class is judged on three numbers living in three places: uploaded and
// downloaded in the tracker plugin's schema, the join date in the host's users
// table. No plugin can read all of them — a plugin schema does not reference
// host tables, and one plugin does not reach into another's — so the host is
// the only component that can answer, and it answers through
// pluginapi.RankStats.
//
// Registered only when the tracker is ON. With it off there are no upload
// figures, every member would report zero, and a ladder gated on uploads would
// either promote nobody or — worse, if an operator set only an age criterion —
// promote everybody on tenure alone while the numbers beside it read zero. The
// plugin reports the absent capability on the job itself, so this degrades to a
// job that says why rather than one that quietly does nothing.

// rankStats implements pluginapi.RankStats over this host's tables.
type rankStats struct{ db storage.Conn }

// allMemberStats is the one query.
//
// A LEFT JOIN, so a member who has never announced still appears — with zeroes
// for traffic and a real join date. That matters for the bottom rung of a
// ladder, which is usually pure tenure: an INNER JOIN would leave every member
// who has not touched the tracker out of the map entirely, and the plugin reads
// absence as "no figures, leave them alone" rather than "zero". They would
// never be promoted to the newcomer rung and nothing would say why.
//
// The traffic sums are per member across every torrent, matching
// storage.ReadTrackerTotals — one aggregate rather than a row per torrent.
const allMemberStats storage.SQL = `
	SELECT u.id,
	       coalesce(s.uploaded, 0)   AS uploaded,
	       coalesce(s.downloaded, 0) AS downloaded,
	       u.created_at
	  FROM users u
	  LEFT JOIN (SELECT user_id, sum(uploaded) AS uploaded, sum(downloaded) AS downloaded
	               FROM tracker.user_stats GROUP BY user_id) s
	    ON s.user_id = u.id`

func (r rankStats) AllStats(ctx context.Context) (map[int64]pluginapi.MemberStats, error) {
	rows, err := r.db.QueryContext(ctx, allMemberStats)
	if err != nil {
		return nil, fmt.Errorf("member stats: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[int64]pluginapi.MemberStats{}
	for rows.Next() {
		var s pluginapi.MemberStats
		var id int64
		if err := rows.Scan(&id, &s.Uploaded, &s.Downloaded, &s.JoinedAt); err != nil {
			return nil, err
		}
		if id <= 0 {
			continue
		}
		// Ratio from the site's own definition rather than a division written
		// here. storage.TrackerTotals.Ratio exists precisely so there is not a
		// second one — including the two cases that look arbitrary and are not:
		// an upload-only member reads as their upload figure, not +Inf, and a
		// member who has done neither reads 0 rather than NaN.
		s.Ratio = storage.TrackerTotals{Uploaded: s.Uploaded, Downloaded: s.Downloaded}.Ratio()
		out[id] = s
	}
	return out, rows.Err()
}

// registerRankStats publishes the capability. MUST be called BEFORE core.Boot:
// the ranks plugin looks it up during its own Provision, so one registered
// afterwards is never seen and every earned rank sits unreachable with nothing
// logged — the same before-Boot rule the achievement metrics carry.
func registerRankStats(c *core.Core, db storage.Conn) error {
	if !config.TrackerEnabled() {
		// Nothing to judge anyone on. Absent rather than stubbed at zero, which
		// is the rule this host already applies to achievement metrics it
		// cannot answer honestly: a stub returning zero is indistinguishable
		// from a real counter for a member who has done nothing.
		return nil
	}
	if !db.Valid() {
		return nil
	}
	if err := c.Register(pluginapi.RankStatsName, rankStats{db}); err != nil {
		return fmt.Errorf("register rank stats: %w", err)
	}
	return nil
}
