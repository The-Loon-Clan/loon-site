package handlers

import (
	"github.com/the-loon-clan/loon-site/internal/markdown"

	"context"
	"fmt"
	"html/template"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/tickets"
)

// Tickets (loon-plugins/tickets) host wiring — UNIT3D's helpdesk. The plugin
// owns /support/* and /admin/tickets/*, and renders the HOST's templates
// through gin's set (web/templates/plugin/).
//
// Eight seams, but only four are load-bearing: chrome, the two paging helpers,
// and Viewer. OwnerRole/RoleBadge are display chrome, and the two notification
// callbacks are optional as a pair — a host with no notification system still
// has a working ticket surface, it just does not announce.
//
// Ships no migration, so the host creates the tables.

// ticketsMigrate creates the plugin's tables (idempotent). Columns come from
// store_pg.go's INSERT/SELECT lists.
func ticketsMigrate(db *sqlx.DB) error {
	stmts := []string{
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
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// wireTicketsPlugin installs the SetDeps seams.
func wireTicketsPlugin(c *core.Core, w *web) error {
	if db := c.Storage.DB(); db != nil {
		if err := ticketsMigrate(db); err != nil {
			return fmt.Errorf("tickets migrate: %w", err)
		}
	}
	tickets.SetDeps(tickets.Deps{
		// The plugin owns its four pages now and wants chrome, not a data map.
		// status is passed through rather than fixed at 200: three of these
		// pages re-render on a validation failure, and a page saying "your
		// ticket was rejected" must not tell every client it succeeded.
		RenderPage: func(gc *gin.Context, status int, title string, body template.HTML) {
			w.renderStatus(gc, status, "site_page.html",
				map[string]any{"Title": title, "Fragment": body})
		},
		// The site's editor and pager, rendered from the HOST's set — a plugin
		// fragment cannot reach the host's partials, and a copy per plugin
		// works only until the partial changes.
		RenderEditor: w.renderEditor,
		RenderPagination: func(page, pageSize, totalItems int, baseURL string) template.HTML {
			return w.renderPagination(hostPagination(page, pageSize, totalItems, baseURL))
		},
		// Crosses the seam because it SANITISES: a second renderer here would
		// be a second allowlist, and the laxer of two is a stored-XSS bug.
		Markdown: markdown.Render,
		PageOffset: func(page, pageSize int) int {
			if page < 1 {
				page = 1
			}
			return (page - 1) * pageSize
		},
		// nil means "not signed in" — the plugin gates on that, so resolving a
		// missing user to a zero Viewer would grant an anonymous request the
		// authority of user 0.
		Viewer: func(gc *gin.Context) *tickets.Viewer {
			u, ok := w.currentUser(gc)
			if !ok || u == nil {
				return nil
			}
			return &tickets.Viewer{
				ID:       int(u.ID),
				Username: u.Username,
				Role:     roleLabel(u.Role),
				Staff:    u.AtLeast(core.RoleMod),
				Admin:    u.AtLeast(core.RoleAdmin),
			}
		},
		// Display chrome only. The plugin's own comment notes that an error
		// here falls back to the default role rather than blanking the page —
		// which is what keeps a deleted account's ticket readable.
		OwnerRole: func(ctx context.Context, userID int) (string, error) {
			u, err := w.store.ByID(ctx, int64(userID))
			if err != nil || u == nil {
				return "", err
			}
			return roleLabel(u.ToCore().Role), nil
		},
		// RoleBadge WAS a bare slug, because support_ticket.html used to be the
		// host's markup and fed it into the user-tag block's Role field. The
		// plugin owns that template now and reads .DisplayName and .Color off
		// it, so a string rendered as "can't evaluate field DisplayName" and
		// took the whole ticket page down with it — a 500 whose only visible
		// symptom was the plugin's own "this page failed to render".
		//
		// The field names match the plugin's roleFallback exactly; that is the
		// contract, and it is what makes the page look the same whether or not
		// this seam is wired.
		RoleBadge: func(_ context.Context, roleName string) any { return ticketRoleBadge(roleName) },
		// Notifications are optional as a pair. Wired through the same fan-out
		// main.go publishes as the "notify.fanout" capability, so a ticket
		// lands in the same inbox as everything else.
		//
		// A new ticket has no single recipient — it is for whoever is on duty
		// — and core.Notify addresses ONE user, so this notifies the ticket's
		// author that it was received. Fanning out to every staff account
		// would need a staff-list query the host does not have, and inventing
		// one here would be the same unbounded query ListUsers exists to avoid.
		NotifyNewTicket: func(ctx context.Context, ticketID int, username, subject, body string, userID int) {
			if c.Notifications == nil || userID == 0 {
				return
			}
			_ = c.Notifications.Notify(ctx, int64(userID), core.Notification{
				Kind:  "ticket_created",
				Title: "Ticket received: " + subject,
				Body:  "We will reply here.",
				Link:  fmt.Sprintf("/support/%d", ticketID),
			})
		},
		NotifyReply: func(ctx context.Context, ticketID, ownerID, recipientID, authorID int, username, subject string, staff bool) {
			// Never notify someone about their own reply. Core skips the case
			// where ActorID equals the recipient too, but the plugin passes
			// both ids precisely so the host can decide, and relying on a
			// downstream guard for something this cheap is how duplicate
			// notifications happen.
			if c.Notifications == nil || recipientID == 0 || recipientID == authorID {
				return
			}
			_ = c.Notifications.Notify(ctx, int64(recipientID), core.Notification{
				Kind:      "ticket_reply",
				Title:     username + " replied to: " + subject,
				Link:      fmt.Sprintf("/support/%d", ticketID),
				ActorID:   int64(authorID),
				ActorName: username,
			})
		},
	})
	return nil
}

// roleBadgeVM is the display data the tickets plugin renders for a role. Field
// names are fixed by that plugin's roleFallback — see RoleBadge above.
type roleBadgeVM struct {
	Name        string
	DisplayName string
	Color       string
}

// ticketRoleBadge maps a host role name onto the badge the plugin's template
// wants. Color is a BOOTSTRAP colour word, because the template writes it into
// both `bg-{{.Color}}` and `var(--bs-{{.Color}})` — theme.css defines the .bg-*
// classes and now the matching --bs-* aliases too.
func ticketRoleBadge(roleName string) roleBadgeVM {
	color := "secondary"
	switch roleSlug(roleName) {
	case "admin":
		color = "danger"
	case "mod":
		color = "info"
	case "contributor":
		color = "warning"
	}
	return roleBadgeVM{
		Name:        roleName,
		DisplayName: roleLabel(roleName),
		Color:       color,
	}
}
