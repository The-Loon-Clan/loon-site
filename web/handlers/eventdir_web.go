package handlers

import (
	"sort"

	"github.com/the-loon-clan/loon/core"
)

// The event directory, shown beside the contract audit on /admin/contracts.
//
// WHY IT BELONGS THERE. core's own comment on the two puts it best: an
// extension is something you CALL, an event is something that HAPPENS to you,
// and a plugin author needs both to know what they can build. The contracts
// page already answered the first half.
//
// WHY IT WAS MISSING. core has built this table for a while — name, summary,
// emitter, payload, kind, countable, subscribers, and the orphans below — and
// nothing here rendered it, so the directory existed and was invisible. That is
// the same failure SEAMS.md exists to prevent one level up: a seam nobody can
// find is a seam that gets reinvented, and an event nobody can find is one a
// subscriber cannot discover by waiting, because an undeclared event is
// invisible until the moment it happens.
//
// Built from core's EXPORTED accessors — EventDefs, EventSubscribers,
// SubscribedEventNames — rather than from core.AdminHandler's own view, whose
// row types are unexported. No core change was needed; the data was already
// reachable.

// eventRow is one declared event, for the template.
type eventRow struct {
	Name      string
	Summary   string
	Emitter   string
	Payload   string
	Kind      string
	Countable bool
	Unstable  bool
	// Subscribers are the plugins listening. EMPTY IS NORMAL and is not a
	// finding: most events exist so that something MAY react later, and a
	// declaration with no listener today is the framework working as intended.
	// It is shown because "who reacts to this" is the question an author asks
	// before adding a listener of their own.
	Subscribers []string
}

// orphanRow is a subscription to an event nothing declares.
type orphanRow struct {
	Name        string
	Subscribers []string
}

// eventDirectory reads the declared events and who listens to each.
func eventDirectory(c *core.Core) ([]eventRow, []orphanRow) {
	if c == nil {
		return nil, nil
	}
	defs := c.EventDefs() // already sorted by name
	rows := make([]eventRow, 0, len(defs))
	declared := make(map[string]bool, len(defs))
	for _, d := range defs {
		declared[d.Name] = true
		subs := c.EventSubscribers(d.Name)
		sort.Strings(subs)
		rows = append(rows, eventRow{
			Name: d.Name, Summary: d.Summary, Emitter: d.Emitter,
			Payload: d.Payload, Kind: string(d.Kind),
			Countable: d.Countable, Unstable: !d.Stable,
			Subscribers: subs,
		})
	}

	// Orphans: somebody is listening for an event nothing declares.
	//
	// Worth its own list rather than a footnote, because an orphan is
	// INDISTINGUISHABLE FROM WORKING. A listener for an event that never fires
	// is silent, and silence is exactly what it would look like if it were
	// fine. The two causes are a typo in the name and an emitter this host did
	// not install, and neither reports itself anywhere else.
	var orphans []orphanRow
	for _, n := range c.SubscribedEventNames() {
		if declared[n] {
			continue
		}
		subs := c.EventSubscribers(n)
		sort.Strings(subs)
		orphans = append(orphans, orphanRow{Name: n, Subscribers: subs})
	}
	return rows, orphans
}
