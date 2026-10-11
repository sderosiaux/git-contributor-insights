package ownership

// Every SHA is stored. Deltas avoid repeating unchanged file attribution and
// aggregate rows; periodic complete checkpoints bound reconstruction work. These
// are storage checkpoints, never sampling points in the public history.
import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const revisionCheckpointInterval = 128
const revisionStateCacheSize = 4

type revisionState struct {
	files      map[string]File
	excluded   map[string]string // nil for a legacy snapshot without path details
	exclusions map[string]int
	depth      int
	lines      int
}

type revisionDelta struct {
	Format          string            `json:"format"`
	Parent          string            `json:"parent,omitempty"`
	Depth           int               `json:"depth"`
	Checkpoint      bool              `json:"checkpoint,omitempty"`
	Files           []File            `json:"files,omitempty"`
	Deleted         []string          `json:"deleted,omitempty"`
	Excluded        map[string]string `json:"excluded,omitempty"`
	ExcludedDeleted []string          `json:"excludedDeleted,omitempty"`
	Exclusions      map[string]int    `json:"exclusions"`
	Lines           int               `json:"lines"`
	// Before is transient. It lets persistence compute aggregate differences in
	// work proportional to changed files rather than the entire repository.
	before map[string]File
}

func emptyRevisionState() *revisionState {
	return &revisionState{files: map[string]File{}, excluded: map[string]string{}, exclusions: map[string]int{}}
}

func cloneRevisionState(base *revisionState) *revisionState {
	state := &revisionState{files: make(map[string]File, len(base.files)), exclusions: make(map[string]int, len(base.exclusions)), depth: base.depth, lines: base.lines}
	for path, file := range base.files {
		state.files[path] = file
	}
	if base.excluded != nil {
		state.excluded = make(map[string]string, len(base.excluded))
		for path, reason := range base.excluded {
			state.excluded[path] = reason
		}
	}
	for reason, count := range base.exclusions {
		state.exclusions[reason] = count
	}
	return state
}

func (state *revisionState) snapshot(sha string, date time.Time) Snapshot {
	result := Snapshot{Revision: sha, Date: date.Format(time.RFC3339Nano), Files: make([]File, 0, len(state.files)), Exclusions: map[string]int{}, Warnings: []string{}}
	for _, file := range state.files {
		result.Files = append(result.Files, cloneFile(file))
	}
	for reason, count := range state.exclusions {
		result.Exclusions[reason] = count
		result.ExcludedFiles += count
	}
	sort.Slice(result.Files, func(a, b int) bool { return result.Files[a].Path < result.Files[b].Path })
	return result
}

func (index *Index) cachedState(sha string) *revisionState {
	index.stateMu.Lock()
	defer index.stateMu.Unlock()
	return index.states[sha]
}

func (index *Index) cacheState(sha string, state *revisionState) {
	index.stateMu.Lock()
	defer index.stateMu.Unlock()
	if index.states == nil {
		index.states = map[string]*revisionState{}
	}
	if _, exists := index.states[sha]; exists {
		return
	}
	for len(index.stateOrder) >= revisionStateCacheSize {
		delete(index.states, index.stateOrder[0])
		index.stateOrder = index.stateOrder[1:]
	}
	index.states[sha] = state
	index.stateOrder = append(index.stateOrder, sha)
}

func (index *Index) readRevisionState(ctx context.Context, sha string) (*revisionState, error) {
	if sha == "" {
		return emptyRevisionState(), nil
	}
	if state := index.cachedState(sha); state != nil {
		return state, nil
	}
	// Iteration avoids a recursive stack on a mixed legacy/delta history.
	type pendingDelta struct {
		sha   string
		delta revisionDelta
	}
	pending := []pendingDelta{}
	current := sha
	var state *revisionState
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if state = index.cachedState(current); state != nil {
			break
		}
		var payload []byte
		if err := index.db.QueryRowContext(ctx, "SELECT snapshot FROM revisions WHERE fingerprint=? AND sha=?", index.fingerprint, current).Scan(&payload); err != nil {
			return nil, err
		}
		reader, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		decoded, err := io.ReadAll(reader)
		closeErr := reader.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		var delta revisionDelta
		if err := json.Unmarshal(decoded, &delta); err != nil {
			return nil, err
		}
		if delta.Format == "" {
			var snapshot Snapshot
			if err := json.Unmarshal(decoded, &snapshot); err != nil {
				return nil, err
			}
			state = emptyRevisionState()
			state.excluded = nil
			state.exclusions = snapshot.Exclusions
			for _, file := range snapshot.Files {
				state.files[file.Path] = file
				state.lines += file.Lines
			}
			index.cacheState(current, state)
			break
		}
		if delta.Format != "delta-v1" {
			return nil, fmt.Errorf("unsupported revision storage format %q", delta.Format)
		}
		pending = append(pending, pendingDelta{sha: current, delta: delta})
		if delta.Checkpoint || delta.Parent == "" {
			state = emptyRevisionState()
			break
		}
		current = delta.Parent
	}
	state = cloneRevisionState(state)
	for n := len(pending) - 1; n >= 0; n-- {
		delta := pending[n].delta
		if delta.Checkpoint {
			state = emptyRevisionState()
		}
		for _, path := range delta.Deleted {
			delete(state.files, path)
		}
		for _, file := range delta.Files {
			state.files[file.Path] = file
		}
		if state.excluded != nil {
			for _, path := range delta.ExcludedDeleted {
				delete(state.excluded, path)
			}
			for path, reason := range delta.Excluded {
				state.excluded[path] = reason
			}
		}
		state.depth, state.lines, state.exclusions = delta.Depth, delta.Lines, delta.Exclusions
	}
	index.cacheState(sha, state)
	return state, nil
}

type preparedFile struct {
	file         File
	path, reason string
	err          error
}

func (index *Index) buildRevisionState(ctx context.Context, revision indexedRevision) (*revisionState, revisionDelta, error) {
	updates, err := index.prepareRevision(ctx, revision)
	if err != nil {
		return nil, revisionDelta{}, err
	}
	return index.applyPreparedRevision(ctx, revision, updates)
}

// Git attributes each changed file at the exact SHA independently of parent
// projection state. Several revisions can therefore prepare concurrently; only
// applying deltas and committing SQLite transactions require topological order.
func (index *Index) prepareRevision(ctx context.Context, revision indexedRevision) ([]preparedFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e := index.session.engine
	paths := e.firstParentChanges[revision.sha]
	files, err := e.changedTree(revision.sha, paths)
	if err != nil {
		return nil, err
	}
	if err := e.classifyBatch(files); err != nil {
		return nil, err
	}
	results := make(chan preparedFile, len(files))
	jobs := make(chan treeFile)
	var workers sync.WaitGroup
	for n := 0; n < min(e.workers, len(files)); n++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for file := range jobs {
				entry := preparedFile{path: file.path}
				entry.reason, entry.err = e.classify(file)
				if entry.err == nil && entry.reason == "" {
					entry.file, entry.err = e.blame(revision.sha, file.path, revision.sha)
				}
				results <- entry
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, file := range files {
			jobs <- file
		}
	}()
	go func() { workers.Wait(); close(results) }()
	var firstErr error
	updates := make([]preparedFile, 0, len(files))
	for entry := range results {
		if entry.err != nil && firstErr == nil {
			firstErr = fmt.Errorf("file %q: %w", entry.path, entry.err)
		}
		updates = append(updates, entry)
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return updates, nil
}

func (index *Index) applyPreparedRevision(ctx context.Context, revision indexedRevision, updates []preparedFile) (*revisionState, revisionDelta, error) {
	e := index.session.engine
	parent := ""
	if parents := e.commits[revision.sha].parents; len(parents) > 0 {
		if _, available := e.commits[parents[0]]; available {
			parent = parents[0]
		}
	}
	base, err := index.readRevisionState(ctx, parent)
	if err != nil {
		return nil, revisionDelta{}, err
	}
	state := cloneRevisionState(base)
	if state.excluded == nil {
		// Old full snapshots contain counts but no excluded paths. Resolve that
		// one legacy boundary once; existing ownership is never recomputed.
		state.excluded = map[string]string{}
		files, err := e.tree(parent)
		if err != nil {
			return nil, revisionDelta{}, err
		}
		for _, file := range files {
			if _, included := state.files[file.path]; included {
				continue
			}
			reason, err := e.classify(file)
			if err != nil {
				return nil, revisionDelta{}, err
			}
			if reason != "" {
				state.excluded[file.path] = reason
			}
		}
	}
	state.depth++
	delta := revisionDelta{Format: "delta-v1", Parent: parent, Depth: state.depth, Excluded: map[string]string{}, before: map[string]File{}}
	paths := e.firstParentChanges[revision.sha]
	for _, path := range paths {
		if file, exists := state.files[path]; exists {
			delta.before[path] = file
			state.lines -= file.Lines
			delete(state.files, path)
			delta.Deleted = append(delta.Deleted, path)
		}
		if reason, exists := state.excluded[path]; exists {
			state.exclusions[reason]--
			if state.exclusions[reason] == 0 {
				delete(state.exclusions, reason)
			}
			delete(state.excluded, path)
			delta.ExcludedDeleted = append(delta.ExcludedDeleted, path)
		}
	}
	for _, update := range updates {
		if update.reason != "" {
			state.excluded[update.path] = update.reason
			state.exclusions[update.reason]++
			delta.Excluded[update.path] = update.reason
			continue
		}
		sort.Slice(update.file.Contributions, func(a, b int) bool {
			x, y := update.file.Contributions[a], update.file.Contributions[b]
			return x.PersonID+"\x00"+x.OrganizationID < y.PersonID+"\x00"+y.OrganizationID
		})
		state.files[update.path] = update.file
		state.lines += update.file.Lines
		delta.Files = append(delta.Files, update.file)
	}
	delta.Exclusions, delta.Lines = state.exclusions, state.lines
	sort.Slice(delta.Files, func(a, b int) bool { return delta.Files[a].Path < delta.Files[b].Path })
	sort.Strings(delta.Deleted)
	sort.Strings(delta.ExcludedDeleted)
	return state, delta, nil
}

func (e *engine) changedTree(revision string, paths []string) ([]treeFile, error) {
	files := []treeFile{}
	requested, seen := map[string]bool{}, map[string]bool{}
	for _, path := range paths {
		requested[path] = true
	}
	for offset := 0; offset < len(paths); offset += 256 {
		args := []string{"ls-tree", "-r", "-l", "-z", revision, "--"}
		args = append(args, paths[offset:min(offset+256, len(paths))]...)
		output, err := e.git(args...)
		if err != nil {
			return nil, err
		}
		for _, record := range bytes.Split(output, []byte{0}) {
			if len(record) == 0 {
				continue
			}
			columns := bytes.SplitN(record, []byte{'\t'}, 2)
			if len(columns) != 2 {
				return nil, fmt.Errorf("unexpected Git tree record")
			}
			metadata := strings.Fields(string(columns[0]))
			if len(metadata) != 4 {
				return nil, fmt.Errorf("unexpected Git tree metadata")
			}
			size, _ := strconv.Atoi(metadata[3])
			path := string(columns[1])
			if !requested[path] || seen[path] {
				continue
			}
			seen[path] = true
			files = append(files, treeFile{path: path, blob: metadata[2], mode: metadata[0], size: size})
		}
	}
	return files, nil
}

func fileScopes(path string) []string {
	scopes := []string{"", path}
	parts := strings.Split(path, "/")
	for n := 1; n < len(parts); n++ {
		scopes = append(scopes, strings.Join(parts[:n], "/"))
	}
	return scopes
}

func (index *Index) persistRevisionState(ctx context.Context, revision indexedRevision, state *revisionState, delta revisionDelta) error {
	aggregates := map[string]map[string]SeriesContribution{}
	add := func(file File, sign int) {
		for _, scope := range fileScopes(file.Path) {
			if aggregates[scope] == nil {
				aggregates[scope] = map[string]SeriesContribution{}
			}
			for _, contribution := range file.Contributions {
				key := contribution.PersonID + "\x00" + contribution.OrganizationID
				value := aggregates[scope][key]
				value.PersonID, value.OrganizationID = contribution.PersonID, contribution.OrganizationID
				value.Lines += sign * contribution.Lines
				aggregates[scope][key] = value
			}
		}
	}
	for _, file := range delta.before {
		add(file, -1)
	}
	for _, file := range delta.Files {
		add(file, 1)
	}
	interval := index.checkpointEvery
	if interval < 1 {
		interval = revisionCheckpointInterval
	}
	delta.Checkpoint = delta.Parent == "" || state.depth >= interval
	if delta.Checkpoint {
		state.depth, delta.Depth = 0, 0
		delta.Files = make([]File, 0, len(state.files))
		for _, file := range state.files {
			delta.Files = append(delta.Files, file)
		}
		sort.Slice(delta.Files, func(a, b int) bool { return delta.Files[a].Path < delta.Files[b].Path })
		delta.Deleted, delta.ExcludedDeleted = nil, nil
		delta.Excluded = state.excluded
	}
	payload, err := json.Marshal(delta)
	if err != nil {
		return err
	}
	var compressed bytes.Buffer
	writer, err := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
	if err != nil {
		return err
	}
	if _, err := writer.Write(payload); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	tx, err := index.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO revisions(fingerprint,sha,date_ns,snapshot) VALUES(?,?,?,?)", index.fingerprint, revision.sha, revision.date.UnixNano(), compressed.Bytes()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO revision_stats(fingerprint,sha,lines,files) VALUES(?,?,?,?)", index.fingerprint, revision.sha, state.lines, len(state.files)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO revision_deltas(fingerprint,sha,parent,depth,checkpoint) VALUES(?,?,?,?,?)", index.fingerprint, revision.sha, delta.Parent, delta.Depth, delta.Checkpoint); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM aggregate_deltas WHERE fingerprint=? AND sha=?", index.fingerprint, revision.sha); err != nil {
		return err
	}
	statement, err := tx.PrepareContext(ctx, "INSERT INTO aggregate_deltas(fingerprint,sha,scope,person,organization,lines) VALUES(?,?,?,?,?,?)")
	if err != nil {
		return err
	}
	defer statement.Close()
	for scope, values := range aggregates {
		for _, value := range values {
			if value.Lines == 0 {
				continue
			}
			if _, err := statement.ExecContext(ctx, index.fingerprint, revision.sha, scope, value.PersonID, value.OrganizationID, value.Lines); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	index.cacheState(revision.sha, state)
	return nil
}

// Reconstruct only the requested first-parent chain. Legacy full aggregates are
// reset points; every delta is relative to precisely the preceding chain SHA.
func (index *Index) readHistoryAggregates(ctx context.Context, scope string, history *revisionHistory, start, end time.Time) (map[string][]SeriesContribution, error) {
	full, deltas, compact, err := index.readHistoryChanges(ctx, scope)
	if err != nil {
		return nil, err
	}
	result := map[string][]SeriesContribution{}
	current := map[string]SeriesContribution{}
	first := history.revisionAt(start).sha
	for _, revision := range history.revisions {
		if revision.date.After(end) {
			break
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !compact[revision.sha] {
			current = map[string]SeriesContribution{}
			for _, value := range full[revision.sha] {
				current[contributionKey(value)] = value
			}
		} else {
			for _, change := range deltas[revision.sha] {
				key := contributionKey(change)
				value := current[key]
				value.PersonID, value.OrganizationID = change.PersonID, change.OrganizationID
				value.Lines += change.Lines
				if value.Lines == 0 {
					delete(current, key)
				} else {
					current[key] = value
				}
			}
		}
		if revision.date.Before(start) && revision.sha != first {
			continue
		}
		values := make([]SeriesContribution, 0, len(current))
		for _, value := range current {
			values = append(values, value)
		}
		sort.Slice(values, func(a, b int) bool { return contributionKey(values[a]) < contributionKey(values[b]) })
		result[revision.sha] = values
	}
	return result, nil
}

func (index *Index) readHistoryChanges(ctx context.Context, scope string) (map[string][]SeriesContribution, map[string][]SeriesContribution, map[string]bool, error) {
	full, err := index.readAggregates(ctx, scope)
	if err != nil {
		return nil, nil, nil, err
	}
	rows, err := index.db.QueryContext(ctx, "SELECT sha,person,organization,lines FROM aggregate_deltas WHERE fingerprint=? AND scope=?", index.fingerprint, scope)
	if err != nil {
		return nil, nil, nil, err
	}
	deltas := map[string][]SeriesContribution{}
	for rows.Next() {
		var sha string
		var value SeriesContribution
		if err := rows.Scan(&sha, &value.PersonID, &value.OrganizationID, &value.Lines); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		deltas[sha] = append(deltas[sha], value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, nil, err
	}
	rows, err = index.db.QueryContext(ctx, "SELECT sha FROM revision_deltas WHERE fingerprint=?", index.fingerprint)
	if err != nil {
		return nil, nil, nil, err
	}
	compact := map[string]bool{}
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		compact[sha] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, nil, err
	}
	return full, deltas, compact, nil
}
