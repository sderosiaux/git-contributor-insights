package ownership

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

type revisionHistory struct {
	tip, label            string
	revisions             []indexedRevision
	visibility, positions map[string]int
}

func (index *Index) history(tip string) (*revisionHistory, error) {
	if tip == "" {
		tip = index.session.base.Repository.Head
	}
	if _, exists := index.session.engine.commits[tip]; !exists {
		return nil, fmt.Errorf("revision is not reachable from the indexed HEAD")
	}
	label := strings.Join(index.refs[tip], ", ")
	if label == "" {
		label = tip[:min(len(tip), 12)]
	}
	history := &revisionHistory{tip: tip, label: label, visibility: map[string]int{}, positions: map[string]int{}}
	if tip == index.session.base.Repository.Head {
		history.revisions = index.revisions
		history.visibility = index.visibility
		history.positions = index.revisionPositions
		return history, nil
	}
	chain := []string{}
	for sha := tip; sha != ""; {
		commit, exists := index.session.engine.commits[sha]
		if !exists {
			break
		}
		chain = append(chain, sha)
		if len(commit.parents) == 0 {
			break
		}
		sha = commit.parents[0]
	}
	var effective time.Time
	for n := len(chain) - 1; n >= 0; n-- {
		commit := index.session.engine.commits[chain[n]]
		if effective.IsZero() || commit.committed.After(effective) {
			effective = commit.committed
		}
		history.revisions = append(history.revisions, indexedRevision{sha: commit.sha, date: effective, committed: commit.committed})
	}
	for position, revision := range history.revisions {
		history.positions[revision.sha] = position
		stack := []string{revision.sha}
		for len(stack) > 0 {
			last := len(stack) - 1
			sha := stack[last]
			stack = stack[:last]
			if _, exists := history.visibility[sha]; exists {
				continue
			}
			commit, exists := index.session.engine.commits[sha]
			if !exists {
				continue
			}
			history.visibility[sha] = position
			stack = append(stack, commit.parents...)
		}
	}
	return history, nil
}

func (history *revisionHistory) revisionAt(date time.Time) indexedRevision {
	n := sort.Search(len(history.revisions), func(n int) bool { return history.revisions[n].date.After(date) })
	if n == 0 {
		return indexedRevision{}
	}
	return history.revisions[n-1]
}

func (history *revisionHistory) reachableAt(sha, revision string) bool {
	position, knownRevision := history.positions[revision]
	firstVisible, knownCommit := history.visibility[sha]
	return knownRevision && knownCommit && firstVisible <= position
}

func (index *Index) datasetForHistory(history *revisionHistory) *Dataset {
	data := index.dataset()
	for _, revision := range history.revisions {
		if !revision.date.Equal(revision.committed) {
			data.Warnings = append(data.Warnings, "Recorded committer dates go backwards along this history. Graph order is preserved with effective visibility dates; original committer dates remain available.")
			break
		}
	}
	return data
}

func (index *Index) readRefs() error {
	index.refs = map[string][]string{index.session.base.Repository.Head: {"HEAD"}}
	output, err := index.session.engine.git("for-each-ref", "--format=%(objectname)%00%(*objectname)%00%(refname:short)")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(output), "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\x00")
		if len(fields) != 3 {
			return fmt.Errorf("invalid Git ref metadata")
		}
		sha := fields[0]
		if fields[1] != "" {
			sha = fields[1]
		}
		if _, exists := index.session.engine.commits[sha]; exists {
			index.refs[sha] = append(index.refs[sha], fields[2])
		}
	}
	for sha := range index.refs {
		sort.Strings(index.refs[sha])
	}
	return nil
}

// Commits pages every reachable SHA; sorting uses recorded committer date and a
// SHA tie-breaker, never a timestamp as identity. Filtering does not sample data.
func (index *Index) Commits(ctx context.Context, filter CommitFilter) (CommitPage, error) {
	page := CommitPage{Commits: []Commit{}, Offset: filter.Offset, Limit: filter.Limit}
	if err := index.queryReady(); err != nil {
		return page, err
	}
	if filter.Offset < 0 {
		return page, fmt.Errorf("offset must be nonnegative")
	}
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	if filter.Limit < 1 || filter.Limit > 500 {
		return page, fmt.Errorf("limit must be between 1 and 500")
	}
	page.Limit = filter.Limit
	var from, to time.Time
	var err error
	if filter.From != "" {
		from, err = parseDate(filter.From, false)
		if err != nil {
			return page, err
		}
	}
	if filter.To != "" {
		to, err = parseDate(filter.To, true)
		if err != nil {
			return page, err
		}
	}
	if !from.IsZero() && !to.IsZero() && from.After(to) {
		return page, fmt.Errorf("from must be at or before to")
	}
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	matches := []commitInfo{}
	for _, commit := range index.session.engine.commits {
		if !from.IsZero() && commit.committed.Before(from) || !to.IsZero() && commit.committed.After(to) {
			continue
		}
		text := commit.sha + "\n" + commit.name + "\n" + commit.email + "\n" + commit.rawEmail + "\n" + commit.summary + "\n" + strings.Join(index.refs[commit.sha], "\n")
		if query != "" && !strings.Contains(strings.ToLower(text), query) {
			continue
		}
		matches = append(matches, commit)
	}
	sort.Slice(matches, func(a, b int) bool {
		if !matches[a].committed.Equal(matches[b].committed) {
			return matches[a].committed.After(matches[b].committed)
		}
		return matches[a].sha < matches[b].sha
	})
	page.Total = len(matches)
	start := min(filter.Offset, len(matches))
	end := min(start+filter.Limit, len(matches))
	page.HasMore = end < len(matches)
	for _, commit := range matches[start:end] {
		if err := ctx.Err(); err != nil {
			return page, err
		}
		item, err := index.commitRecord(ctx, commit.sha)
		if err != nil {
			return page, err
		}
		page.Commits = append(page.Commits, *item)
	}
	return page, nil
}

func (index *Index) commitRecord(ctx context.Context, sha string) (*Commit, error) {
	commit, exists := index.session.engine.commits[sha]
	if !exists {
		return nil, fmt.Errorf("revision is not reachable from indexed HEAD")
	}
	_, mainline := index.revisionPositions[sha]
	result := &Commit{SHA: sha, Parents: append([]string{}, commit.parents...), Date: commit.committed.Format(time.RFC3339), AuthorName: commit.name, AuthorEmail: commit.email, Summary: commit.summary, Mainline: mainline, Refs: append([]string{}, index.refs[sha]...)}
	result.AI = commit.ai
	err := index.db.QueryRowContext(ctx, "SELECT lines,files FROM revision_stats WHERE fingerprint=? AND sha=?", index.fingerprint, sha).Scan(&result.Lines, &result.Files)
	if err == sql.ErrNoRows {
		// Earlier index versions already have exact ownership blobs. Derive only
		// catalogue statistics lazily; never recompute Git blame during migration.
		snapshot, readErr := index.readSnapshot(ctx, sha, commit.committed)
		if readErr != nil {
			return nil, readErr
		}
		result.Files = len(snapshot.Files)
		for _, file := range snapshot.Files {
			result.Lines += file.Lines
		}
		_, err = index.db.ExecContext(ctx, "INSERT OR IGNORE INTO revision_stats(fingerprint,sha,lines,files) VALUES(?,?,?,?)", index.fingerprint, sha, result.Lines, result.Files)
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}
