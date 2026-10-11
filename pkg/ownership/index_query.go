package ownership

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Query returns complete first-parent revision history for a range. Only the two
// endpoint snapshots contain files/evidence; the series is scoped and compact.
// Activity expirations are explicit points even when the Git revision is stable.
func (index *Index) Query(ctx context.Context, from, to, scope string) (*Dataset, error) {
	return index.QueryHistory(ctx, from, to, scope, index.session.base.Repository.Head)
}

// QueryHistory follows the chosen reachable tip's first-parent chain only.
func (index *Index) QueryHistory(ctx context.Context, from, to, scope, tip string) (*Dataset, error) {
	return index.queryHistory(ctx, from, to, scope, tip, false)
}

// QueryCompactHistory reads preaggregated changes without materializing every actor at every state.
func (index *Index) QueryCompactHistory(ctx context.Context, from, to, scope, tip string) (*Dataset, error) {
	return index.queryHistory(ctx, from, to, scope, tip, true)
}

type seriesEvent struct {
	date     time.Time
	revision indexedRevision
	kind     string
	sequence int
}

func (index *Index) queryHistory(ctx context.Context, from, to, scope, tip string, compact bool) (*Dataset, error) {
	if err := index.queryReady(); err != nil {
		return nil, err
	}
	history, err := index.history(tip)
	if err != nil {
		return nil, err
	}
	if len(history.revisions) == 0 {
		return nil, fmt.Errorf("repository has no indexed revisions")
	}
	start, end := history.revisions[0].date, history.revisions[len(history.revisions)-1].date
	if from != "" {
		start, err = parseDate(from, false)
		if err != nil {
			return nil, err
		}
	}
	if to != "" {
		end, err = parseDate(to, true)
		if err != nil {
			return nil, err
		}
	}
	if start.After(end) {
		return nil, fmt.Errorf("from must be at or before to")
	}
	scope = strings.Trim(scope, "/")
	if scope == "." {
		scope = ""
	}
	data := index.datasetForHistory(history)
	data.Timeline = Timeline{Interval: "commit", Since: start.Format(time.RFC3339Nano), Until: end.Format(time.RFC3339Nano), Complete: true, Tip: history.tip, Label: history.label, TotalIndex: len(index.allRevisions)}
	index.mu.RLock()
	activities := index.activities
	index.mu.RUnlock()
	for _, date := range []time.Time{start, end} {
		revision := history.revisionAt(date)
		snapshot, err := index.readSnapshot(ctx, revision.sha, date)
		if err != nil {
			return nil, err
		}
		index.addSnapshotActivity(&snapshot, date, revision.sha, activities, history.reachableAt)
		data.Snapshots = append(data.Snapshots, snapshot)
	}
	var stocks map[string][]SeriesContribution
	if !compact {
		stocks, err = index.readHistoryAggregates(ctx, scope, history, start, end)
		if err != nil {
			return nil, err
		}
	}
	// Activity rows are ordered by date. Scope once, then visit only the live
	// window per point instead of scanning the repository's entire history.
	seriesActivities := activities
	if scope != "" {
		seriesActivities = []indexedActivity{}
		for _, entry := range activities {
			if withinScope(entry.path, scope) {
				seriesActivities = append(seriesActivities, entry)
			}
		}
	}
	events := []seriesEvent{}
	revisionDates := map[int64]bool{}
	for sequence, revision := range history.revisions {
		if revision.date.Before(start) || revision.date.After(end) {
			continue
		}
		events = append(events, seriesEvent{date: revision.date, revision: revision, kind: "revision", sequence: sequence})
		revisionDates[revision.date.UnixNano()] = true
		data.Timeline.Revisions++
	}
	window := time.Duration(index.session.options.WindowDays) * 24 * time.Hour
	expirations := map[int64]bool{}
	entries := map[int64]bool{}
	for _, entry := range seriesActivities {
		// A skewed branch commit can already be reachable from a merge while its
		// recorded committer date is still in the future. Its activity enters at
		// that date even if no first-parent revision is created then.
		if !entry.date.Before(start) && !entry.date.After(end) && !revisionDates[entry.date.UnixNano()] && !entries[entry.date.UnixNano()] && history.reachableAt(entry.sha, history.revisionAt(entry.date).sha) {
			entries[entry.date.UnixNano()] = true
			events = append(events, seriesEvent{date: entry.date, revision: history.revisionAt(entry.date), kind: "activity"})
		}
		date := entry.date.Add(window)
		if date.Before(start) || date.After(end) || revisionDates[date.UnixNano()] || expirations[date.UnixNano()] || !history.reachableAt(entry.sha, history.revisionAt(date).sha) {
			continue
		}
		expirations[date.UnixNano()] = true
		events = append(events, seriesEvent{date: date, revision: history.revisionAt(date), kind: "expiry"})
	}
	for n, date := range []time.Time{start, end} {
		if n == 1 && start.Equal(end) {
			continue
		}
		if revisionDates[date.UnixNano()] || expirations[date.UnixNano()] || entries[date.UnixNano()] {
			continue
		}
		events = append(events, seriesEvent{date: date, revision: history.revisionAt(date), kind: "boundary"})
	}
	sort.SliceStable(events, func(a, b int) bool {
		if !events[a].date.Equal(events[b].date) {
			return events[a].date.Before(events[b].date)
		}
		return events[a].sequence < events[b].sequence
	})
	if compact {
		index.session.engine.groupPeople(data)
		data.Series, err = index.compactHistorySeries(ctx, scope, history, events, seriesActivities, window)
		if err != nil {
			return nil, err
		}
		data.SeriesEncoding = "changes-v1"
		data.Timeline.Points = len(data.Series)
		return data, nil
	}
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		point := SeriesPoint{Date: event.date.Format(time.RFC3339Nano), Revision: event.revision.sha, Kind: event.kind, Contributions: []SeriesContribution{}}
		if event.revision.sha != "" {
			point.CommittedDate = event.revision.committed.Format(time.RFC3339)
		}
		values := map[string]SeriesContribution{}
		for _, value := range stocks[event.revision.sha] {
			values[value.PersonID+"\x00"+value.OrganizationID] = value
			point.Lines += value.Lines
		}
		lower := event.date.Add(-window)
		for _, entry := range activityWindow(seriesActivities, lower, event.date) {
			if !history.reachableAt(entry.sha, event.revision.sha) {
				continue
			}
			key := entry.person + "\x00" + entry.organization
			value := values[key]
			value.PersonID, value.OrganizationID = entry.person, entry.organization
			value.ChangedLines += entry.changed
			values[key] = value
		}
		for _, value := range values {
			point.Contributions = append(point.Contributions, value)
		}
		sort.Slice(point.Contributions, func(a, b int) bool {
			return contributionKey(point.Contributions[a]) < contributionKey(point.Contributions[b])
		})
		data.Series = append(data.Series, point)
	}
	data.Timeline.Points = len(data.Series)
	index.session.engine.groupPeople(data)
	return data, nil
}

// Revision inspects one exact indexed SHA, including collisions in timestamps.
func (index *Index) Revision(ctx context.Context, sha string) (*Dataset, error) {
	if err := index.queryReady(); err != nil {
		return nil, err
	}
	history, err := index.history(sha)
	if err != nil {
		return nil, err
	}
	selected := history.revisions[len(history.revisions)-1]
	snapshot, err := index.readSnapshot(ctx, sha, selected.date)
	if err != nil {
		return nil, err
	}
	index.mu.RLock()
	activities := index.activities
	index.mu.RUnlock()
	index.addSnapshotActivity(&snapshot, selected.date, sha, activities, history.reachableAt)
	data := index.datasetForHistory(history)
	data.Commit, err = index.commitRecord(ctx, sha)
	if err != nil {
		return nil, err
	}
	data.Snapshots = []Snapshot{snapshot}
	data.Timeline = Timeline{Interval: "commit", Since: snapshot.Date, Until: snapshot.Date, Points: 1, Revisions: 1, Complete: true, Tip: sha, Label: history.label, TotalIndex: len(index.allRevisions)}
	index.session.engine.groupPeople(data)
	return data, nil
}

func (index *Index) queryReady() error {
	index.mu.RLock()
	defer index.mu.RUnlock()
	if index.closed {
		return fmt.Errorf("ownership index is closed")
	}
	if index.status.State != "ready" {
		return fmt.Errorf("ownership index is not ready: %d/%d revisions", index.status.Completed, index.status.Total)
	}
	return nil
}

func (index *Index) dataset() *Dataset {
	data := index.session.base
	data.People, data.Organizations, data.Snapshots, data.Series = []Person{}, []Organization{}, []Snapshot{}, []SeriesPoint{}
	data.Warnings = append([]string{}, index.session.base.Warnings...)
	data.Method.Identity = "Identity and affiliations are frozen at index creation and fingerprinted by configuration, current mailmap, method and activity window. Every HEAD-reachable commit has exact indexed ownership, metadata and activity. Each timeline follows only the first-parent chain of its selected tip; branches are never interleaved. Timeline dates are effective first-parent visibility dates: a running maximum of recorded committer dates, preserving every SHA and same-time revision. Original committer dates are exposed separately. Evidence is complete, without a commit cap."
	data.Method.Activity = "Added + deleted lines of eligible text files in non-merge commits reachable from the selected revision, in (cutoff − window, cutoff], using recorded committer dates. Merge changes are stored in the database but excluded from this metric to avoid counting branch changes twice. Files are classified at the activity commit. Explicit expiry points show activity leaving the window without a new revision."
	for _, person := range index.session.engine.identities {
		data.People = append(data.People, person)
	}
	sort.Slice(data.People, func(a, b int) bool { return data.People[a].ID < data.People[b].ID })
	for _, organization := range index.session.engine.config.organizations {
		data.Organizations = append(data.Organizations, organization)
	}
	sort.Slice(data.Organizations, func(a, b int) bool { return data.Organizations[a].ID < data.Organizations[b].ID })
	return &data
}

func (index *Index) revisionAt(date time.Time) indexedRevision {
	n := sort.Search(len(index.revisions), func(n int) bool { return index.revisions[n].date.After(date) })
	if n == 0 {
		return indexedRevision{}
	}
	return index.revisions[n-1]
}

func (index *Index) reachableAt(sha, revision string) bool {
	position, knownRevision := index.revisionPositions[revision]
	firstVisible, knownCommit := index.visibility[sha]
	return knownRevision && knownCommit && firstVisible <= position
}

func (index *Index) addSnapshotActivity(snapshot *Snapshot, date time.Time, revision string, activities []indexedActivity, reachable func(string, string) bool) {
	files := map[string]*File{}
	for n := range snapshot.Files {
		files[snapshot.Files[n].Path] = &snapshot.Files[n]
	}
	// Map values use independent objects because appending files may relocate the
	// original backing array and invalidate pointers into it.
	for path, file := range files {
		copy := *file
		copy.Contributions = append([]Contribution{}, file.Contributions...)
		files[path] = &copy
	}
	lower := date.Add(-time.Duration(index.session.options.WindowDays) * 24 * time.Hour)
	for _, entry := range activityWindow(activities, lower, date) {
		if !reachable(entry.sha, revision) {
			continue
		}
		file := files[entry.path]
		if file == nil {
			file = &File{Path: entry.path, Contributions: []Contribution{}}
			files[entry.path] = file
		}
		contribution := contributionFor(file, entry.person, entry.organization)
		contribution.ChangedLines += entry.changed
		commit := index.session.engine.commits[entry.sha]
		addEvidence(contribution, Evidence{SHA: entry.sha, Path: entry.path, Date: entry.date.Format(time.RFC3339), Summary: commit.summary, Kind: "activity"})
	}
	snapshot.Files = []File{}
	for _, file := range files {
		sort.Slice(file.Contributions, func(a, b int) bool {
			aValue, bValue := file.Contributions[a], file.Contributions[b]
			return aValue.PersonID+"\x00"+aValue.OrganizationID < bValue.PersonID+"\x00"+bValue.OrganizationID
		})
		snapshot.Files = append(snapshot.Files, *file)
	}
	sort.Slice(snapshot.Files, func(a, b int) bool { return snapshot.Files[a].Path < snapshot.Files[b].Path })
}

// Indexed activity is sorted by recorded committer date. Window bounds are
// (lower, upper], including every row when several commits share a timestamp.
func activityWindow(entries []indexedActivity, lower, upper time.Time) []indexedActivity {
	start := sort.Search(len(entries), func(n int) bool { return entries[n].date.After(lower) })
	end := sort.Search(len(entries), func(n int) bool { return entries[n].date.After(upper) })
	return entries[start:end]
}

func (index *Index) readAggregates(ctx context.Context, scope string) (map[string][]SeriesContribution, error) {
	rows, err := index.db.QueryContext(ctx, "SELECT sha,person,organization,lines FROM aggregates WHERE fingerprint=? AND scope=? ORDER BY sha,person,organization", index.fingerprint, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := map[string][]SeriesContribution{}
	for rows.Next() {
		var sha string
		var value SeriesContribution
		if err := rows.Scan(&sha, &value.PersonID, &value.OrganizationID, &value.Lines); err != nil {
			return nil, err
		}
		values[sha] = append(values[sha], value)
	}
	return values, rows.Err()
}

func withinScope(filename, scope string) bool {
	return scope == "" || filename == scope || strings.HasPrefix(filename, scope+"/")
}
func contributionKey(value SeriesContribution) string {
	return value.PersonID + "\x00" + value.OrganizationID
}
