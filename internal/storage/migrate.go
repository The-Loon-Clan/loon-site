package storage

// Schema. Every table this site owns is created here.
//
// These were 24 functions scattered through web/handlers — DDL in the HTTP
// layer, one migration filed beside whichever page first needed the table.
// communitiesMigrate alone was 121 lines and the fourth-longest function in
// the codebase.
//
// They are methods rather than one Migrate() that runs the lot, because they
// are NOT all called from one place and running them together would change
// when they run. Seven execute inside their plugin's wiring, and donations
// only migrates when donations are switched on — a host that never enables it
// should not grow its tables. Moving the SQL without moving that decision is
// the whole point.

// MigrateUserDisplay replaces the baseline's user_display with one that reads
// the real columns.
//
// This is the seam, not a workaround. loon-baseline builds the view with
//
//	''::text     AS avatar_path,
//	0::smallint  AS reputation_tier
//
// and says why in a comment above it: "avatar is empty and reputation zero
// until the corresponding facet packages land — at which point only this view
// changes, no plugin." The facets have landed on this host — messages added
// users.avatar_path, the points work added users.reputation_tier — and nothing
// ever changed the view. So both columns were real, populated by the host, and
// discarded on the way out to every plugin that reads the contract.
//
// It cost more than avatars. The communities plugin joins user_display for
// exactly these fields, so its member lists have been rendering an empty
// avatar and tier 0 for every member since the day they were wired, with
// nothing anywhere reporting a problem — the fourth instance of the pattern in
// docs/BACKLOG.md #1, and the one that hid the longest, because the fallback
// (initials, tier 0) is what a real new account looks like.
//
// MUST run after the columns exist and after userStore.Migrate has created the
// baseline version, hence its call site late in main. CREATE OR REPLACE keeps
// the column names, types and order identical — Postgres rejects a replacement
// that changes them, which is a useful guard: if the baseline's shape ever
// moves, this fails loudly at boot instead of quietly serving a stale view.
func (st *Store) MigrateUserDisplay() error {
	// Belt and braces: both columns are added elsewhere (messages and points),
	// and adding them here as well means this file does not silently depend on
	// which plugins a host happens to wire.
	for _, q := range []SQL{
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS avatar_path TEXT`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS reputation_tier INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			return err
		}
	}
	_, err := st.db.Exec(`CREATE OR REPLACE VIEW user_display AS
		SELECT id,
		       username,
		       CASE role
		           WHEN -2 THEN 'banned'
		           WHEN -1 THEN 'disabled'
		           WHEN  1 THEN 'contributor'
		           WHEN  2 THEN 'mod'
		           WHEN  3 THEN 'admin'
		           ELSE 'user'
		       END AS role,
		       COALESCE(avatar_path, '')::text        AS avatar_path,
		       COALESCE(reputation_tier, 0)::smallint AS reputation_tier
		FROM users`)
	if err != nil {
		return err
	}
	return st.migrateUserStats()
}

// migrateUserStats publishes the per-user figures a PLUGIN needs and cannot
// get: things that live on the users table but are not identity.
//
// A SECOND view, rather than two more columns on user_display, and that is not
// a style choice. joined_at went on user_display first and the site would not
// boot: loon-baseline's own migration runs earlier and issues its own CREATE OR
// REPLACE VIEW user_display with the original four columns, which Postgres
// rejects against a wider view —
//
//	users migrate: pq: cannot drop columns from view (42P16)
//
// So user_display's shape belongs to the baseline, and anything this host wants
// to add to it has to live somewhere the baseline does not also write. The
// existing comment above had it half right: it warned that a change to the
// baseline's shape would fail loudly here. It fails just as loudly in the other
// direction, one migration earlier, and takes the whole boot with it.
func (st *Store) migrateUserStats() error {
	_, err := st.db.Exec(`CREATE OR REPLACE VIEW user_stats AS
		SELECT id,
		       created_at AS joined_at
		FROM users`)
	return err
}

// MigrateAvatarMod adds the two timestamps. Idempotent, like every other host
// migration.
func (st *Store) MigrateAvatarMod() error {
	for _, q := range []SQL{
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS avatar_updated_at TIMESTAMPTZ`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS avatar_reviewed_at TIMESTAMPTZ`,
		// The queue reads "pending, newest first" and nothing else.
		`CREATE INDEX IF NOT EXISTS users_avatar_review
		   ON users (avatar_updated_at DESC)
		 WHERE avatar_path <> ''`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// MigrateBookmarks creates the table. Idempotent.
func (st *Store) MigrateBookmarks() error {
	stmts := []SQL{
		// UNIQUE(user_id, release_id) makes the toggle idempotent in the
		// DATABASE rather than in a read-then-write the double-click of an
		// impatient user can slip between.
		`CREATE TABLE IF NOT EXISTS release_bookmark (
		    id         BIGSERIAL PRIMARY KEY,
		    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    release_id BIGINT NOT NULL,
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    UNIQUE (user_id, release_id)
		)`,
		// "my bookmarks, newest first" is the only listing query.
		`CREATE INDEX IF NOT EXISTS idx_release_bookmark_user
		     ON release_bookmark (user_id, created_at DESC)`,
	}
	for _, q := range stmts {
		if _, err := st.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// MigrateCommunities creates the plugin's eight tables. Columns are taken from
// its INSERT lists and model st.db tags.
func (st *Store) MigrateCommunities() error {
	stmts := []SQL{
		`CREATE TABLE IF NOT EXISTS communities (
		    id                   SERIAL PRIMARY KEY,
		    slug                 TEXT NOT NULL UNIQUE,
		    name                 TEXT NOT NULL,
		    description          TEXT NOT NULL DEFAULT '',
		    sidebar_md           TEXT NOT NULL DEFAULT '',
		    banner_url           TEXT NOT NULL DEFAULT '',
		    banner_position      INTEGER NOT NULL DEFAULT 50,
		    icon_url             TEXT NOT NULL DEFAULT '',
		    accent_color         TEXT NOT NULL DEFAULT '',
		    owner_user_id        BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    release_group_id     INTEGER,
		    nsfw                 BOOLEAN NOT NULL DEFAULT false,
		    -- Join gating. join_type is checked in Go against the plugin's
		    -- CommunityJoin* constants, so no CHECK here: a constraint that
		    -- disagrees with the plugin's enum breaks writes the plugin
		    -- considers valid.
		    join_type            TEXT NOT NULL DEFAULT 'open',
		    min_account_age_days INTEGER NOT NULL DEFAULT 0,
		    min_role_level       INTEGER NOT NULL DEFAULT 0,
		    join_points_cost     INTEGER NOT NULL DEFAULT 0,
		    hidden_at            TIMESTAMPTZ,
		    hidden_by            BIGINT,
		    hidden_reason        TEXT NOT NULL DEFAULT '',
		    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
		    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS community_subscribers (
		    community_id INTEGER NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
		    user_id      BIGINT  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
		    PRIMARY KEY (community_id, user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS community_mods (
		    community_id INTEGER NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
		    user_id      BIGINT  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    added_by     BIGINT,
		    added_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
		    PRIMARY KEY (community_id, user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS community_rules (
		    id           SERIAL PRIMARY KEY,
		    community_id INTEGER NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
		    position     INTEGER NOT NULL DEFAULT 0,
		    title        TEXT NOT NULL,
		    body         TEXT NOT NULL DEFAULT '',
		    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS community_threads (
		    id             SERIAL PRIMARY KEY,
		    community_id   INTEGER NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
		    user_id        BIGINT  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    title          TEXT NOT NULL,
		    body           TEXT NOT NULL DEFAULT '',
		    pinned         BOOLEAN NOT NULL DEFAULT false,
		    locked         BOOLEAN NOT NULL DEFAULT false,
		    reply_count    INTEGER NOT NULL DEFAULT 0,
		    last_post_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
		    removed_at     TIMESTAMPTZ,
		    removed_by     BIGINT,
		    removed_reason TEXT NOT NULL DEFAULT '',
		    hidden_at      TIMESTAMPTZ,
		    hidden_by      BIGINT,
		    hidden_reason  TEXT NOT NULL DEFAULT '',
		    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
		    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_community_threads_community
		     ON community_threads (community_id, last_post_at DESC)`,
		`CREATE TABLE IF NOT EXISTS community_posts (
		    id             SERIAL PRIMARY KEY,
		    thread_id      INTEGER NOT NULL REFERENCES community_threads(id) ON DELETE CASCADE,
		    user_id        BIGINT  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    body           TEXT NOT NULL,
		    quoted_post_id INTEGER REFERENCES community_posts(id) ON DELETE SET NULL,
		    removed_at     TIMESTAMPTZ,
		    removed_by     BIGINT,
		    removed_reason TEXT NOT NULL DEFAULT '',
		    hidden_at      TIMESTAMPTZ,
		    hidden_by      BIGINT,
		    hidden_reason  TEXT NOT NULL DEFAULT '',
		    edited_at      TIMESTAMPTZ,
		    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_community_posts_thread
		     ON community_posts (thread_id, created_at ASC)`,
		`CREATE TABLE IF NOT EXISTS community_join_requests (
		    id               SERIAL PRIMARY KEY,
		    community_id     INTEGER NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
		    user_id          BIGINT  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    message          TEXT NOT NULL DEFAULT '',
		    status           TEXT NOT NULL DEFAULT 'pending',
		    response_message TEXT NOT NULL DEFAULT '',
		    -- points_held is points ESCROWED with the request: the plugin
		    -- deducts on apply and refunds on denial, so this is the amount to
		    -- give back, not a display figure.
		    points_held      INTEGER NOT NULL DEFAULT 0,
		    decided_by       BIGINT,
		    decided_at       TIMESTAMPTZ,
		    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_community_join_requests_community
		     ON community_join_requests (community_id, status, created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS community_invites (
		    id           SERIAL PRIMARY KEY,
		    community_id INTEGER NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
		    code         TEXT NOT NULL UNIQUE,
		    note         TEXT NOT NULL DEFAULT '',
		    created_by   BIGINT,
		    max_uses     INTEGER NOT NULL DEFAULT 0,
		    use_count    INTEGER NOT NULL DEFAULT 0,
		    expires_at   TIMESTAMPTZ,
		    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
	}
	for _, q := range stmts {
		if _, err := st.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// MigrateCommunityMod creates the two tables.
func (st *Store) MigrateCommunityMod() error {
	stmts := []SQL{
		`CREATE TABLE IF NOT EXISTS moderation_items (
		    id              BIGSERIAL PRIMARY KEY,
		    kind            TEXT   NOT NULL,
		    subject_user_id BIGINT NOT NULL,
		    -- What was reported, captured AT REPORT TIME. Without it a member
		    -- changes their avatar mid-vote and everybody after that is voting
		    -- on a different picture from everybody before.
		    subject_ref     TEXT   NOT NULL DEFAULT '',
		    reason          TEXT   NOT NULL DEFAULT '',
		    reported_by     BIGINT NOT NULL,
		    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
		    resolved_at     TIMESTAMPTZ,
		    resolution      TEXT   NOT NULL DEFAULT '',
		    -- Null when the vote decided it. That distinction is the record of
		    -- whether the community or a moderator made the call.
		    resolved_by     BIGINT
		)`,
		// One OPEN item per subject per kind. This is what turns the fiftieth
		// reporter into a vote instead of a fiftieth row.
		`CREATE UNIQUE INDEX IF NOT EXISTS moderation_items_one_open
		   ON moderation_items (kind, subject_user_id) WHERE resolved_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS moderation_items_open
		   ON moderation_items (created_at DESC) WHERE resolved_at IS NULL`,
		`CREATE TABLE IF NOT EXISTS moderation_votes (
		    item_id    BIGINT NOT NULL REFERENCES moderation_items(id) ON DELETE CASCADE,
		    user_id    BIGINT NOT NULL,
		    remove     BOOLEAN NOT NULL,
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    -- One vote each, enforced by the database rather than by a check
		    -- the handler could race with itself on a double click.
		    PRIMARY KEY (item_id, user_id)
		)`,
	}
	for _, q := range stmts {
		if _, err := st.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// MigrateDonations creates the four tables the plugin queries. Columns come
// from its own INSERT/SELECT lists and model st.db tags.
//
// site_settings is shared, not donation-specific — it is a plain key/value
// store the plugin keeps donate_* config and BTCPay credentials in. A restart
// must not silently discard a webhook secret and start accepting callbacks it
// can no longer verify, which is why it is a table rather than a map.
func (st *Store) MigrateDonations() error {
	stmts := []SQL{
		`CREATE TABLE IF NOT EXISTS site_costs (
		    id         SERIAL PRIMARY KEY,
		    label      TEXT NOT NULL,
		    category   TEXT NOT NULL DEFAULT 'other',
		    goal_group TEXT NOT NULL DEFAULT 'site',
		    period     TEXT NOT NULL DEFAULT 'monthly',
		    amount_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
		    notes      TEXT NOT NULL DEFAULT '',
		    sort_order INTEGER NOT NULL DEFAULT 0,
		    active     BOOLEAN NOT NULL DEFAULT true,
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS donation_packages (
		    id           BIGSERIAL PRIMARY KEY,
		    label        TEXT NOT NULL,
		    amount_usd   DOUBLE PRECISION NOT NULL DEFAULT 0,
		    stock_total  INTEGER NOT NULL DEFAULT 0,
		    reward       TEXT NOT NULL DEFAULT '',
		    description  TEXT NOT NULL DEFAULT '',
		    reset_period TEXT NOT NULL DEFAULT '',
		    sort_order   INTEGER NOT NULL DEFAULT 0,
		    active       BOOLEAN NOT NULL DEFAULT true,
		    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
		    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		// donor_user_id and package_id are NULLABLE on purpose: a tip-jar
		// donation has no claimed slot, and an admin fiat entry may have no
		// account to attribute to. The model types them as pointers for the
		// same reason — do not add NOT NULL here.
		`CREATE TABLE IF NOT EXISTS donations (
		    id            BIGSERIAL PRIMARY KEY,
		    asset         TEXT NOT NULL DEFAULT '',
		    txid          TEXT NOT NULL DEFAULT '',
		    amount_native DOUBLE PRECISION NOT NULL DEFAULT 0,
		    amount_usd    DOUBLE PRECISION NOT NULL DEFAULT 0,
		    donor_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
		    donor_label   TEXT NOT NULL DEFAULT '',
		    received_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
		    note          TEXT NOT NULL DEFAULT '',
		    overfunded    BOOLEAN NOT NULL DEFAULT false,
		    package_id    BIGINT REFERENCES donation_packages(id) ON DELETE SET NULL
		)`,
		// The webhook dedupes settlements by transaction id, so this is a
		// correctness index, not just a speed one — without it a retried
		// callback can double-credit.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_donations_txid
		     ON donations (txid) WHERE txid <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_donations_received ON donations (received_at DESC)`,
		// The donor's running total, WITHOUT WHICH THE WEBHOOK CANNOT RECORD AN
		// ATTRIBUTED DONATION AT ALL.
		//
		// donations.CreateDonation commits the donation row and then, for a
		// donation carrying a member, runs
		//
		//	UPDATE users SET donation_count = donation_count + 1,
		//	                 donation_total_usd = donation_total_usd + $2,
		//	                 donator = donator OR (...) >= $3
		//
		// against these three columns. This host created the donations tables
		// and never created them, so that statement failed with
		// `column "donation_count" does not exist`, the whole transaction
		// rolled back, and the webhook answered 500 — which is the CORRECT
		// answer to a failed write, so BTCPay retried it, forever, and the
		// member's money settled while the site recorded nothing.
		//
		// Anonymous donations were unaffected (the UPDATE is skipped when there
		// is no donor), which is why the tip jar worked and nothing looked
		// wrong. Only a signed-in member's donation broke — the only kind that
		// could ever unlock anything.
		//
		// donation_total_usd is the LIFETIME SUM, and that is the whole point:
		// ten payments of $5 reach the $50 tier, where counting settlements
		// would rank fifty $1 tips above one $500 gift. donations/events.go
		// makes the same argument about why a donor badge must be scored on
		// this figure rather than on the donations.received event.
		//
		// DOUBLE PRECISION to match donations.amount_usd above rather than
		// NUMERIC — two money columns that add together should not disagree
		// about their type, and the comparison that flips `donator` adds them.
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS donation_count     INTEGER          NOT NULL DEFAULT 0`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS donation_total_usd DOUBLE PRECISION NOT NULL DEFAULT 0`,
		// Sticky by design: CreateDonation only ever ORs it true, so a member
		// who donated once keeps the flag even if the threshold is raised
		// later. Reproduced here as a default rather than a rule this column
		// enforces, because the rule lives in the statement above.
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS donator            BOOLEAN          NOT NULL DEFAULT false`,
	}
	for _, q := range stmts {
		if _, err := st.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// MigrateFollows creates the table. Idempotent.
func (st *Store) MigrateFollows() error {
	stmts := []SQL{
		// The pair is the primary key, so following twice is a no-op in the
		// DATABASE rather than in a read-then-write. CASCADE on both sides:
		// a deleted account should not leave dangling edges.
		`CREATE TABLE IF NOT EXISTS user_follow (
		    follower_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    followee_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
		    PRIMARY KEY (follower_id, followee_id),
		    -- Following yourself is not a state worth having; the CHECK means
		    -- no handler has to remember to reject it.
		    CHECK (follower_id <> followee_id)
		)`,
		// "who follows X" — the reverse of the primary key, which only covers
		// "who does X follow".
		`CREATE INDEX IF NOT EXISTS idx_user_follow_followee
		     ON user_follow (followee_id)`,
	}
	for _, q := range stmts {
		if _, err := st.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// MigrateForum creates the plugin's tables (idempotent). Same shape as prod's
// numbered migrations; when the plugin ships its own migrations (planned for
// the PG17 consolidation window) this moves there and no-ops here.
func (st *Store) MigrateForum() error {
	stmts := []SQL{
		`CREATE TABLE IF NOT EXISTS forum_categories (
		    id          SERIAL PRIMARY KEY,
		    name        TEXT NOT NULL UNIQUE,
		    description TEXT NOT NULL DEFAULT '',
		    ordinal     INTEGER NOT NULL DEFAULT 0,
		    color       TEXT NOT NULL DEFAULT 'blue',
		    icon        TEXT NOT NULL DEFAULT 'chat-square-text',
		    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS forum_threads (
		    id            SERIAL PRIMARY KEY,
		    category_id   INTEGER NOT NULL REFERENCES forum_categories(id) ON DELETE CASCADE,
		    user_id       BIGINT NOT NULL REFERENCES users(id),
		    title         TEXT NOT NULL,
		    thread_type   TEXT NOT NULL DEFAULT 'discussion' CHECK (thread_type IN ('discussion','recruitment')),
		    pinned        BOOLEAN NOT NULL DEFAULT false,
		    locked        BOOLEAN NOT NULL DEFAULT false,
		    reply_count   INTEGER NOT NULL DEFAULT 0,
		    last_post_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
		    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
		    hidden_at     TIMESTAMPTZ,
		    hidden_reason TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS forum_posts (
		    id             SERIAL PRIMARY KEY,
		    thread_id      INTEGER NOT NULL REFERENCES forum_threads(id) ON DELETE CASCADE,
		    user_id        BIGINT NOT NULL REFERENCES users(id),
		    body           TEXT NOT NULL,
		    quoted_post_id INTEGER REFERENCES forum_posts(id) ON DELETE SET NULL,
		    edited_at      TIMESTAMPTZ,
		    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
		    hidden_at      TIMESTAMPTZ,
		    hidden_reason  TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS forum_post_reactions (
		    post_id    INTEGER NOT NULL REFERENCES forum_posts(id) ON DELETE CASCADE,
		    user_id    BIGINT  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    emoji      TEXT    NOT NULL,
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    PRIMARY KEY (post_id, user_id, emoji)
		)`,
		// Access gates (prod migration 278) — ALTERs so pre-gate demo
		// installs pick them up; the CREATE above carries them implicitly
		// for fresh installs via these same statements running after it.
		`ALTER TABLE forum_categories ADD COLUMN IF NOT EXISTS see_role   TEXT NOT NULL DEFAULT 'all'`,
		`ALTER TABLE forum_categories ADD COLUMN IF NOT EXISTS read_role  TEXT NOT NULL DEFAULT 'all'`,
		`ALTER TABLE forum_categories ADD COLUMN IF NOT EXISTS write_role TEXT NOT NULL DEFAULT 'user'`,
		`ALTER TABLE forum_categories ADD COLUMN IF NOT EXISTS see_tier   SMALLINT NOT NULL DEFAULT 0`,
		`ALTER TABLE forum_categories ADD COLUMN IF NOT EXISTS read_tier  SMALLINT NOT NULL DEFAULT 0`,
		`ALTER TABLE forum_categories ADD COLUMN IF NOT EXISTS write_tier SMALLINT NOT NULL DEFAULT 0`,
		`CREATE INDEX IF NOT EXISTS idx_forum_threads_category ON forum_threads (category_id, last_post_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_forum_posts_thread ON forum_posts (thread_id, created_at ASC)`,
		`CREATE INDEX IF NOT EXISTS idx_forum_post_reactions_post_emoji ON forum_post_reactions (post_id, emoji)`,
	}
	for _, q := range stmts {
		if _, err := st.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// MigrateGifts creates the record of who gave what to whom.
func (st *Store) MigrateGifts() error {
	stmts := []SQL{
		`CREATE TABLE IF NOT EXISTS point_gifts (
		    id          BIGSERIAL PRIMARY KEY,
		    from_user   BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    to_user     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    amount      INTEGER NOT NULL CHECK (amount > 0),
		    note        TEXT NOT NULL DEFAULT '',
		    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS point_gifts_from ON point_gifts (from_user, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS point_gifts_to   ON point_gifts (to_user,   created_at DESC)`,
	}
	for _, q := range stmts {
		if _, err := st.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// MigrateGrabs creates the table. One row per download, not a counter column:
// a counter cannot answer "this week", which is the question trending asks.
func (st *Store) MigrateGrabs() error {
	stmts := []SQL{
		// user_id is NULLABLE: /nzb/:id is reachable by an anonymous visitor
		// and by an API key, and a grab still happened. Making it NOT NULL
		// would silently drop exactly the traffic a public indexer sees most.
		`CREATE TABLE IF NOT EXISTS release_grab (
		    id         BIGSERIAL PRIMARY KEY,
		    release_id BIGINT NOT NULL,
		    user_id    BIGINT REFERENCES users(id) ON DELETE SET NULL,
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		// The two questions asked of this table: "how many for this release"
		// and "what was grabbed most recently". One index each.
		`CREATE INDEX IF NOT EXISTS idx_release_grab_release ON release_grab (release_id)`,
		`CREATE INDEX IF NOT EXISTS idx_release_grab_recent ON release_grab (created_at DESC)`,
	}
	for _, q := range stmts {
		if _, err := st.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// MigrateInviteCodes creates the table. Idempotent, like the other host
// migrations.
func (st *Store) MigrateInviteCodes() error {
	stmts := []SQL{
		`CREATE TABLE IF NOT EXISTS invite_codes (
		    code        TEXT PRIMARY KEY,
		    created_by  BIGINT NOT NULL,
		    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
		    expires_at  TIMESTAMPTZ NOT NULL,
		    -- Set on redemption and never cleared. The invite chain is the only
		    -- accountability an invite-only site has.
		    used_by     BIGINT,
		    used_at     TIMESTAMPTZ
		)`,
		`CREATE INDEX IF NOT EXISTS invite_codes_creator ON invite_codes (created_by, created_at DESC)`,
	}
	for _, q := range stmts {
		if _, err := st.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// MigrateInvites adds the balance column. Idempotent.
func (st *Store) MigrateInvites() error {
	_, err := st.db.Exec(
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS invites INTEGER NOT NULL DEFAULT 0`)
	return err
}

// MigrateMessages creates the plugin's five tables (idempotent). Columns come
// from pg.go's INSERT/SELECT lists — those are what actually fail on a
// mismatch, not the model structs.
func (st *Store) MigrateMessages() error {
	stmts := []SQL{
		// One row per PAIR: user_lo_id/user_hi_id are stored in canonical
		// (LEAST, GREATEST) order, so the unique index below is what actually
		// enforces "one thread per pair regardless of who started it".
		`CREATE TABLE IF NOT EXISTS dm_threads (
		    id              BIGSERIAL PRIMARY KEY,
		    user_lo_id      BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    user_hi_id      BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    last_message_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    lo_deleted_at   TIMESTAMPTZ,
		    hi_deleted_at   TIMESTAMPTZ,
		    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
		    CHECK (user_lo_id < user_hi_id)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_dm_threads_pair
		     ON dm_threads (user_lo_id, user_hi_id)`,
		`CREATE TABLE IF NOT EXISTS dm_messages (
		    id         BIGSERIAL PRIMARY KEY,
		    thread_id  BIGINT NOT NULL REFERENCES dm_threads(id) ON DELETE CASCADE,
		    sender_id  BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    body       TEXT NOT NULL,
		    read_at    TIMESTAMPTZ,
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		// "unread for me" is `sender_id != me AND read_at IS NULL`, so the
		// index leads with the thread and carries both.
		`CREATE INDEX IF NOT EXISTS idx_dm_messages_thread
		     ON dm_messages (thread_id, created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS dm_blocks (
		    blocker_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    blocked_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    PRIMARY KEY (blocker_id, blocked_id)
		)`,
		// Admin announcements. target selects the audience; expires_at is
		// nullable for "until dismissed".
		`CREATE TABLE IF NOT EXISTS messages (
		    id         BIGSERIAL PRIMARY KEY,
		    from_name  TEXT NOT NULL DEFAULT '',
		    title      TEXT NOT NULL,
		    body       TEXT NOT NULL,
		    target     TEXT NOT NULL DEFAULT 'all',
		    expires_at TIMESTAMPTZ,
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		// Per-user read/dismiss state for an announcement.
		`CREATE TABLE IF NOT EXISTS message_reads (
		    message_id BIGINT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
		    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    dismissed  BOOLEAN NOT NULL DEFAULT false,
		    read_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
		    PRIMARY KEY (message_id, user_id)
		)`,
		// The plugin's thread-list query selects COALESCE(u.avatar_path, '')
		// from the HOST's users table. loon-baseline's users table has no such
		// column, and the handler discards the error (`threads, _ = ...`), so
		// the whole inbox silently rendered empty while the rows sat in the
		// database. Add the column the plugin's SQL assumes rather than patch
		// the query: prod's users table has it, so this is the demo catching
		// up, and an unset value COALESCEs to the initial-letter fallback the
		// templates already draw.
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS avatar_path TEXT`,
	}
	for _, q := range stmts {
		if _, err := st.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// MigrateNews creates the plugin's table (idempotent). Mirrors the DDL the
// plugin's store_pg.go queries and its integration test creates.
func (st *Store) MigrateNews() error {
	stmts := []SQL{
		`CREATE TABLE IF NOT EXISTS news_posts (
		    id         BIGSERIAL PRIMARY KEY,
		    title      TEXT NOT NULL,
		    slug       TEXT NOT NULL UNIQUE,
		    body       TEXT NOT NULL,
		    published  BOOLEAN NOT NULL DEFAULT false,
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		// The public feed reads published posts newest-first; the admin list
		// reads all of them. One partial index covers the hot path.
		`CREATE INDEX IF NOT EXISTS idx_news_posts_published
		     ON news_posts (created_at DESC) WHERE published`,
	}
	for _, q := range stmts {
		if _, err := st.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// MigratePoints adds the columns and table. Idempotent.
func (st *Store) MigratePoints() error {
	stmts := []SQL{
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS points INTEGER NOT NULL DEFAULT 0`,
		// reputation_tier is read by the communities plugin for display chrome.
		// Nothing in this stack computes reputation, so it stays 0 — a column
		// that exists to satisfy a join, not a feature. If reputation ever
		// becomes real it gets a plugin, not an UPDATE here.
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS reputation_tier INTEGER NOT NULL DEFAULT 0`,
		`CREATE TABLE IF NOT EXISTS points_ledger (
		    id           BIGSERIAL PRIMARY KEY,
		    user_id      BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    amount       INTEGER NOT NULL,
		    balance      INTEGER NOT NULL,
		    kind         TEXT NOT NULL DEFAULT '',
		    description  TEXT NOT NULL DEFAULT '',
		    reference_id BIGINT,
		    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_points_ledger_user
		     ON points_ledger (user_id, created_at DESC)`,
	}
	for _, q := range stmts {
		if _, err := st.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// MigrateLastSeen adds the column. Idempotent.
func (st *Store) MigrateLastSeen() error {
	_, err := st.db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS last_seen_at TIMESTAMPTZ`)
	return err
}

// MigrateProfileBio adds the column. IF NOT EXISTS so it is safe on every boot,
// the same shape messages_web.go used to add avatar_path.
func (st *Store) MigrateProfileBio() error {
	_, err := st.db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS bio TEXT NOT NULL DEFAULT ''`)
	return err
}

// MigrateSecurity adds the columns and the recovery-code table.
func (st *Store) MigrateSecurity() error {
	stmts := []SQL{
		// Pending and active are separate columns so an abandoned setup cannot
		// half-enable anything: totp_secret is authoritative and is only
		// written when a code has been verified.
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_secret TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_pending TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_enabled_at TIMESTAMPTZ`,
		`CREATE TABLE IF NOT EXISTS totp_recovery_codes (
		    id       BIGSERIAL PRIMARY KEY,
		    user_id  BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    -- The HASH, never the code. See hashRecoveryCode.
		    code_hash TEXT  NOT NULL,
		    used_at  TIMESTAMPTZ
		)`,
		`CREATE INDEX IF NOT EXISTS totp_recovery_user ON totp_recovery_codes (user_id) WHERE used_at IS NULL`,
		// Email changes are confirmed at the NEW address before they take
		// effect, so a typo cannot move the reset destination somewhere the
		// member cannot read.
		`CREATE TABLE IF NOT EXISTS email_changes (
		    token      TEXT PRIMARY KEY,
		    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    new_email  TEXT   NOT NULL,
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    expires_at TIMESTAMPTZ NOT NULL,
		    used_at    TIMESTAMPTZ
		)`,
	}
	for _, q := range stmts {
		if _, err := st.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// MigrateSettings adds the privacy column and the preference table.
func (st *Store) MigrateSettings() error {
	stmts := []SQL{
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS private_profile BOOLEAN NOT NULL DEFAULT false`,
		// A row per DISABLED kind would be smaller, but an explicit enabled
		// flag survives the default changing: if this host ever flips a kind to
		// off-by-default, existing opt-ins stay opted in.
		`CREATE TABLE IF NOT EXISTS notification_prefs (
		    user_id BIGINT  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    kind    TEXT    NOT NULL,
		    enabled BOOLEAN NOT NULL DEFAULT true,
		    PRIMARY KEY (user_id, kind)
		)`,
	}
	for _, q := range stmts {
		if _, err := st.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// MigrateTickets creates the plugin's tables (idempotent). Columns come from
// store_pg.go's INSERT/SELECT lists.
func (st *Store) MigrateTickets() error {
	stmts := []SQL{
		// username is denormalised on the row because the plugin's list
		// queries select it directly rather than joining users — keep it.
		`CREATE TABLE IF NOT EXISTS support_tickets (
		    id         BIGSERIAL PRIMARY KEY,
		    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    username   TEXT NOT NULL DEFAULT '',
		    subject    TEXT NOT NULL,
		    body       TEXT NOT NULL,
		    priority   TEXT NOT NULL DEFAULT 'normal' CHECK (priority IN ('low','normal','high')),
		    status     TEXT NOT NULL DEFAULT 'open'   CHECK (status IN ('open','in_progress','closed')),
		    admin_note TEXT NOT NULL DEFAULT '',
		    -- Owner-controlled opt-in: true exposes the ticket and its replies
		    -- on /support/public. Default private.
		    public     BOOLEAN NOT NULL DEFAULT false,
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_support_tickets_user
		     ON support_tickets (user_id, created_at DESC)`,
		// The admin list filters by status and the public list by the flag,
		// both newest-first.
		`CREATE INDEX IF NOT EXISTS idx_support_tickets_status
		     ON support_tickets (status, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_support_tickets_public
		     ON support_tickets (created_at DESC) WHERE public`,
		`CREATE TABLE IF NOT EXISTS ticket_replies (
		    id         BIGSERIAL PRIMARY KEY,
		    ticket_id  BIGINT NOT NULL REFERENCES support_tickets(id) ON DELETE CASCADE,
		    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    username   TEXT NOT NULL DEFAULT '',
		    body       TEXT NOT NULL,
		    is_admin   BOOLEAN NOT NULL DEFAULT false,
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ticket_replies_ticket
		     ON ticket_replies (ticket_id, created_at ASC)`,
	}
	for _, q := range stmts {
		if _, err := st.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// MigrateUndo creates the table.
func (st *Store) MigrateUndo() error {
	stmts := []SQL{
		`CREATE TABLE IF NOT EXISTS undo_actions (
		    token      TEXT PRIMARY KEY,
		    user_id    BIGINT NOT NULL,
		    kind       TEXT   NOT NULL,
		    -- What is needed to reverse it, shaped by the kind. JSON rather
		    -- than columns because the kinds have nothing in common: an avatar
		    -- needs a path and a bio needs a body, and a table with both would
		    -- be half null forever.
		    payload    JSONB  NOT NULL,
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    expires_at TIMESTAMPTZ NOT NULL,
		    used_at    TIMESTAMPTZ
		)`,
		`CREATE INDEX IF NOT EXISTS undo_actions_expiry ON undo_actions (expires_at)`,
	}
	for _, q := range stmts {
		if _, err := st.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// MigrateWidgets creates the placement table. Idempotent.
func (st *Store) MigrateWidgets() error {
	stmts := []SQL{
		// PRIMARY KEY (region, slug): one widget appears at most once in a
		// region. Placing it twice is never what an operator meant, and
		// enforcing it here means the editor's "add" can be a plain upsert
		// rather than a read-then-write two clicks can slip between.
		//
		// No foreign key to anything. A slug names a widget in a REGISTRY that
		// exists only in memory and changes with which plugins are switched
		// on, so the database cannot check it — resolution happens at render
		// through core.WidgetBySlug, which reports missing rather than
		// guessing.
		`CREATE TABLE IF NOT EXISTS widget_placement (
		    region   TEXT    NOT NULL,
		    slug     TEXT    NOT NULL,
		    position INT     NOT NULL DEFAULT 0,
		    enabled  BOOLEAN NOT NULL DEFAULT TRUE,
		    PRIMARY KEY (region, slug)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_widget_placement_region
		     ON widget_placement (region, position)`,
		// Added after the table shipped, so ADD COLUMN IF NOT EXISTS rather
		// than a changed CREATE — an existing deployment already has rows.
		// The setting an operator typed for THIS placement; see
		// core.WidgetConfig. Empty means not configured, which a widget must
		// treat as "render nothing".
		`ALTER TABLE widget_placement ADD COLUMN IF NOT EXISTS config TEXT NOT NULL DEFAULT ''`,
	}
	for _, q := range stmts {
		if _, err := st.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// MigrateWiki creates the plugin's tables (idempotent). Columns are taken from
// pg.go's own INSERT/SELECT lists rather than from the model structs, since the
// queries are what will actually fail if a column is missing.
func (st *Store) MigrateWiki() error {
	stmts := []SQL{
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
		if _, err := st.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// MigrateWishlist creates the table.
func (st *Store) MigrateWishlist() error {
	stmts := []SQL{
		`CREATE TABLE IF NOT EXISTS wishlist_items (
		    id         BIGSERIAL PRIMARY KEY,
		    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    title      TEXT   NOT NULL,
		    note       TEXT   NOT NULL DEFAULT '',
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    -- Set by a PERSON deciding it turned up, never by a matcher. See
		    -- the note above about guessing.
		    filled_at  TIMESTAMPTZ
		)`,
		`CREATE INDEX IF NOT EXISTS wishlist_open ON wishlist_items (created_at DESC) WHERE filled_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS wishlist_user ON wishlist_items (user_id, created_at DESC)`,
	}
	for _, q := range stmts {
		if _, err := st.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// MigrateSiteSettings creates the shared key/value table.
//
// It lived inside MigrateDonations, whose own comment noted that the table is
// "shared, not donation-specific" — and donations migrates ONLY when the
// plugin is switched on. So on any default deployment the table was never
// created, and the two settings the site loads at boot failed against it:
//
//	level=ERROR msg="load access settings" err="relation \"site_settings\" does not exist"
//	level=ERROR msg="load cover mode"      err="relation \"site_settings\" does not exist"
//
// Invisible in development, because a database that has ever had donations
// enabled keeps the table forever. Found by booting against an empty one.
// A shared table cannot live behind a feature flag.
func (st *Store) MigrateSiteSettings() error {
	_, err := st.db.Exec(`CREATE TABLE IF NOT EXISTS site_settings (
	    key   TEXT PRIMARY KEY,
	    value TEXT NOT NULL DEFAULT ''
	)`)
	return err
}
