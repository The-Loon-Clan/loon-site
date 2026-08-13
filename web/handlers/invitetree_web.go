package handlers

import "github.com/the-loon-clan/loon-site/internal/storage"

// The invite tree — who brought whom, and who brought them.
//
// docs/UNIT3D-PARITY.md called this the feature our invite balance "declines to
// be", and that was true when invites were only a NUMBER on users.invites: a
// count records that somebody had the right to invite, and nothing about who
// they actually invited.
//
// It stopped being true when invitecodes_web.go landed. That file records
// created_by and used_by on every code and says why: "on an invite-only site
// that chain is the only accountability there is — if an account turns out to
// be a problem, 'who vouched for them' is the first question, and a schema that
// cannot answer it makes invite-only a formality."
//
// So this is a READ. No table, no migration, no new column: the chain has been
// accumulating since the day codes were added, and this is the page that asks
// it the question it was recorded for.

func summariseTree(rows []storage.InviteTreeRow) storage.InviteTreeTotals {
	t := storage.InviteTreeTotals{People: len(rows)}
	for _, r := range rows {
		if r.Depth > t.Generations {
			t.Generations = r.Depth
		}
	}
	return t
}
