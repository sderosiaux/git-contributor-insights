package ownership

import (
	"context"
	"sort"
	"time"
)

// compactHistorySeries traverses the preaggregated stock deltas and activity
// transitions once. Work and allocation follow changes, not points × people.
// Every original event survives, including timestamp collisions and expirations.
func (index *Index) compactHistorySeries(ctx context.Context, scope string, history *revisionHistory, events []seriesEvent, activities []indexedActivity, window time.Duration) ([]SeriesPoint, error) {
	full, deltas, compact, err := index.readHistoryChanges(ctx, scope)
	if err != nil {
		return nil, err
	}
	resolved, _ := index.session.engine.personGroups()
	type transition struct {
		date                 time.Time
		position             int
		person, organization string
		changed              int
	}
	transitions := make([]transition, 0, 2*len(activities))
	for _, entry := range activities {
		position, reachable := history.visibility[entry.sha]
		if !reachable {
			continue
		}
		// Activity enters only once both its recorded date and graph visibility
		// have been reached. An already-expired branch contribution never enters.
		visible := history.revisions[position].date
		date, at := entry.date, -1
		if !date.After(visible) {
			date, at = visible, position
		}
		expiry := entry.date.Add(window)
		if !date.Before(expiry) {
			continue
		}
		transitions = append(transitions,
			transition{date, at, entry.person, entry.organization, entry.changed},
			transition{expiry, -1, entry.person, entry.organization, -entry.changed})
	}
	sort.Slice(transitions, func(i, j int) bool {
		if !transitions[i].date.Equal(transitions[j].date) {
			return transitions[i].date.Before(transitions[j].date)
		}
		return transitions[i].position < transitions[j].position
	})
	current := map[string]SeriesContribution{}
	published := map[string]SeriesContribution{}
	dirty := map[string]bool{}
	lines := 0
	update := func(person, organization string, stock, activity int) {
		if id, ok := resolved[person]; ok {
			person = id
		}
		key := person + "\x00" + organization
		value := current[key]
		value.PersonID, value.OrganizationID = person, organization
		value.Lines += stock
		value.ChangedLines += activity
		lines += stock
		current[key] = value
		dirty[key] = true
	}
	stockCursor, activityCursor := 0, 0
	result := make([]SeriesPoint, 0, len(events))
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		position := -1
		if p, ok := history.positions[event.revision.sha]; ok {
			position = p
		}
		for stockCursor <= position {
			sha := history.revisions[stockCursor].sha
			if !compact[sha] {
				// Legacy full snapshots reset only stock, never the activity accumulator.
				for key, value := range current {
					if value.Lines != 0 {
						lines -= value.Lines
						value.Lines = 0
						current[key] = value
						dirty[key] = true
					}
				}
				for _, value := range full[sha] {
					update(value.PersonID, value.OrganizationID, value.Lines, 0)
				}
			} else {
				for _, value := range deltas[sha] {
					update(value.PersonID, value.OrganizationID, value.Lines, 0)
				}
			}
			stockCursor++
		}
		for activityCursor < len(transitions) {
			action := transitions[activityCursor]
			if action.date.After(event.date) || (action.date.Equal(event.date) && action.position > position) {
				break
			}
			update(action.person, action.organization, 0, action.changed)
			activityCursor++
		}
		point := SeriesPoint{Date: event.date.Format(time.RFC3339Nano), Revision: event.revision.sha, Kind: event.kind, Lines: lines, Contributions: []SeriesContribution{}}
		if event.revision.sha != "" {
			point.CommittedDate = event.revision.committed.Format(time.RFC3339)
		}
		for key := range dirty {
			value := current[key]
			prior, exists := published[key]
			if value.Lines == 0 && value.ChangedLines == 0 {
				if exists {
					point.Contributions = append(point.Contributions, value)
					delete(published, key)
				}
				delete(current, key)
			} else if !exists || prior != value {
				point.Contributions = append(point.Contributions, value)
				published[key] = value
			}
			delete(dirty, key)
		}
		sort.Slice(point.Contributions, func(i, j int) bool {
			return contributionKey(point.Contributions[i]) < contributionKey(point.Contributions[j])
		})
		result = append(result, point)
	}
	return result, nil
}
