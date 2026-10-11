package ownership

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const indexMethodVersion = "ownership-index-v1-all-evidence-effective-first-parent"

type IndexStatus struct {
	Workers        int    `json:"workers"`
	Repository     string `json:"repository"`
	PhaseCompleted int    `json:"phaseCompleted"`
	PhaseTotal     int    `json:"phaseTotal"`
	State          string `json:"state"`
	Phase          string `json:"phase"`
	Completed      int    `json:"completed"`
	Total          int    `json:"total"`
	Commits        int    `json:"commits"`
	Head           string `json:"head"`
	Path           string `json:"path"`
	Error          string `json:"error,omitempty"`
}

type indexedRevision struct {
	sha       string
	date      time.Time
	committed time.Time
}
type indexedActivity struct {
	sha, path, person, organization string
	date                            time.Time
	changed                         int
}

// Index persists every HEAD-reachable revision state and all reachable commit
// metadata/activity in SQLite. A revision becomes visible only after one atomic
// transaction commits its snapshot and aggregate rows. Interrupted runs resume.
type Index struct {
	db                *sql.DB
	session           *Session
	fingerprint       string
	revisions         []indexedRevision
	allRevisions      []indexedRevision
	refs              map[string][]string
	visibility        map[string]int
	revisionPositions map[string]int
	activities        []indexedActivity
	status            IndexStatus
	mu                sync.RWMutex
	gate              chan struct{}
	cancel            context.CancelFunc
	closed            bool
	checkpointEvery   int
	stateMu           sync.Mutex
	states            map[string]*revisionState
	stateOrder        []string
}

func OpenIndex(ctx context.Context, repoPath string, options Options, dbPath string) (*Index, error) {
	// Sampling flags are intentionally irrelevant to a complete index.
	options.Snapshots, options.Interval = 0, "auto"
	session, err := NewSession(ctx, repoPath, options)
	if err != nil {
		return nil, err
	}
	if dbPath == "" {
		return nil, fmt.Errorf("an index database path is required")
	}
	absolute, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", absolute)
	if err != nil {
		return nil, err
	}
	// A single connection keeps transaction boundaries and PRAGMA settings
	// consistent; WAL still allows independent processes to read the index.
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, `PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS configurations(fingerprint TEXT PRIMARY KEY, repository TEXT NOT NULL, metadata BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS indexed_heads(fingerprint TEXT NOT NULL, head TEXT NOT NULL, PRIMARY KEY(fingerprint,head));
CREATE TABLE IF NOT EXISTS commits(fingerprint TEXT NOT NULL, sha TEXT NOT NULL, author_name TEXT NOT NULL, author_email TEXT NOT NULL, raw_email TEXT NOT NULL, author_date TEXT NOT NULL, commit_date TEXT NOT NULL, summary TEXT NOT NULL, parents TEXT NOT NULL, PRIMARY KEY(fingerprint,sha));
CREATE TABLE IF NOT EXISTS activity(fingerprint TEXT NOT NULL, sha TEXT NOT NULL, path TEXT NOT NULL, person TEXT NOT NULL, organization TEXT NOT NULL, date_ns INTEGER NOT NULL, changed_lines INTEGER NOT NULL, is_merge INTEGER NOT NULL, excluded_reason TEXT NOT NULL, PRIMARY KEY(fingerprint,sha,path));
CREATE TABLE IF NOT EXISTS revisions(fingerprint TEXT NOT NULL, sha TEXT NOT NULL, date_ns INTEGER NOT NULL, snapshot BLOB NOT NULL, PRIMARY KEY(fingerprint,sha));
CREATE TABLE IF NOT EXISTS revision_stats(fingerprint TEXT NOT NULL, sha TEXT NOT NULL, lines INTEGER NOT NULL, files INTEGER NOT NULL, PRIMARY KEY(fingerprint,sha));
CREATE TABLE IF NOT EXISTS aggregates(fingerprint TEXT NOT NULL, sha TEXT NOT NULL, scope TEXT NOT NULL, person TEXT NOT NULL, organization TEXT NOT NULL, lines INTEGER NOT NULL, PRIMARY KEY(fingerprint,scope,sha,person,organization));
CREATE TABLE IF NOT EXISTS revision_deltas(fingerprint TEXT NOT NULL, sha TEXT NOT NULL, parent TEXT NOT NULL, depth INTEGER NOT NULL, checkpoint INTEGER NOT NULL, PRIMARY KEY(fingerprint,sha));
CREATE TABLE IF NOT EXISTS aggregate_deltas(fingerprint TEXT NOT NULL, sha TEXT NOT NULL, scope TEXT NOT NULL, person TEXT NOT NULL, organization TEXT NOT NULL, lines INTEGER NOT NULL, PRIMARY KEY(fingerprint,scope,sha,person,organization));
CREATE INDEX IF NOT EXISTS activity_range ON activity(fingerprint,date_ns);
CREATE INDEX IF NOT EXISTS aggregate_revision ON aggregates(fingerprint,sha);
CREATE INDEX IF NOT EXISTS aggregate_delta_revision ON aggregate_deltas(fingerprint,sha);
CREATE INDEX IF NOT EXISTS aggregate_scope ON aggregates(fingerprint,scope,sha);`); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize ownership index: %w", err)
	}
	fingerprint, err := indexFingerprint(session)
	if err != nil {
		db.Close()
		return nil, err
	}
	index := &Index{db: db, session: session, fingerprint: fingerprint, gate: make(chan struct{}, 1), visibility: map[string]int{}, revisionPositions: map[string]int{}, checkpointEvery: revisionCheckpointInterval, states: map[string]*revisionState{}}
	var effective time.Time
	for n := len(session.chain) - 1; n >= 0; n-- {
		sha := session.chain[n]
		committed := session.engine.commits[sha].committed
		if effective.IsZero() || committed.After(effective) {
			effective = committed
		}
		index.revisions = append(index.revisions, indexedRevision{sha: sha, date: effective, committed: committed})
	}
	visited := map[string]bool{}
	effectiveDates := map[string]time.Time{}
	var visit func(string)
	visit = func(sha string) {
		if visited[sha] {
			return
		}
		commit, exists := session.engine.commits[sha]
		if !exists {
			return
		}
		visited[sha] = true
		for _, parent := range commit.parents {
			visit(parent)
		}
		date := commit.committed
		if len(commit.parents) > 0 && effectiveDates[commit.parents[0]].After(date) {
			date = effectiveDates[commit.parents[0]]
		}
		effectiveDates[sha] = date
		index.allRevisions = append(index.allRevisions, indexedRevision{sha: sha, date: date, committed: commit.committed})
	}
	visit(session.base.Repository.Head)
	// Reachability on one first-parent line is monotonic. Assign each reachable
	// commit its first visible revision once instead of retaining a quadratic
	// ancestor set for every revision in every query.
	for position, revision := range index.revisions {
		index.revisionPositions[revision.sha] = position
		stack := []string{revision.sha}
		for len(stack) > 0 {
			last := len(stack) - 1
			sha := stack[last]
			stack = stack[:last]
			if _, exists := index.visibility[sha]; exists {
				continue
			}
			commit, exists := session.engine.commits[sha]
			if !exists {
				continue
			}
			index.visibility[sha] = position
			stack = append(stack, commit.parents...)
		}
	}
	index.status = IndexStatus{Workers: session.options.Workers, Repository: session.base.Repository.Name, State: "indexing", Phase: "metadata", Total: len(index.allRevisions), Commits: len(session.engine.commits), Head: session.base.Repository.Head, Path: absolute}
	if err := index.readRefs(); err != nil {
		db.Close()
		return nil, err
	}
	metadata, _ := json.Marshal(session.base)
	if _, err := db.ExecContext(ctx, "INSERT OR IGNORE INTO configurations(fingerprint,repository,metadata) VALUES(?,?,?)", fingerprint, session.base.Repository.Path, metadata); err != nil {
		db.Close()
		return nil, err
	}
	completed, err := index.completedRevisions(ctx)
	if err != nil {
		db.Close()
		return nil, err
	}
	index.status.Completed = len(completed)
	var metadataReady int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM indexed_heads WHERE fingerprint=? AND head=?", fingerprint, index.status.Head).Scan(&metadataReady); err != nil {
		db.Close()
		return nil, err
	}
	if metadataReady > 0 && len(completed) == len(index.allRevisions) {
		if err := index.loadActivities(ctx); err != nil {
			db.Close()
			return nil, err
		}
		index.status.State, index.status.Phase = "ready", "ready"
	}
	return index, nil
}

func indexFingerprint(session *Session) (string, error) {
	e := session.engine
	hash := sha256.New()
	fmt.Fprintf(hash, "%s\x00%s\x00%d\x00", indexMethodVersion, e.repo, session.options.WindowDays)
	config, err := json.Marshal(e.config)
	if err != nil {
		return "", err
	}
	// affiliation internals are unexported; hash the original configuration bytes.
	if session.options.ConfigPath != "" {
		config, err = os.ReadFile(session.options.ConfigPath)
		if err != nil {
			return "", err
		}
	}
	hash.Write(config)
	for _, filename := range []string{filepath.Join(e.repo, ".mailmap")} {
		content, err := os.ReadFile(filename)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		hash.Write(content)
	}
	if config, err := e.git("config", "--get-regexp", "^(mailmap\\.|blame\\.|diff\\.)"); err == nil {
		hash.Write(config)
	}
	if filename, err := e.git("config", "--path", "--get", "mailmap.file"); err == nil {
		path := strings.TrimSpace(string(filename))
		if !filepath.IsAbs(path) {
			path = filepath.Join(e.repo, path)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		hash.Write(content)
	}
	if object, err := e.git("config", "--get", "mailmap.blob"); err == nil {
		content, err := e.git("show", strings.TrimSpace(string(object)))
		if err != nil {
			return "", err
		}
		hash.Write(content)
	}
	if shallowPath, err := e.git("rev-parse", "--git-path", "shallow"); err == nil {
		path := strings.TrimSpace(string(shallowPath))
		if !filepath.IsAbs(path) {
			path = filepath.Join(e.repo, path)
		}
		content, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		hash.Write(content)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (index *Index) Status() IndexStatus {
	index.mu.RLock()
	defer index.mu.RUnlock()
	return index.status
}

func (index *Index) Run(ctx context.Context) (runErr error) {
	select {
	case index.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-index.gate }()
	index.mu.Lock()
	if index.closed {
		index.mu.Unlock()
		return fmt.Errorf("ownership index is closed")
	}
	ctx, cancel := context.WithCancel(ctx)
	index.cancel = cancel
	index.status.State, index.status.Phase, index.status.Error = "indexing", "metadata", ""
	index.mu.Unlock()
	defer func() {
		cancel()
		index.mu.Lock()
		index.cancel = nil
		if runErr != nil {
			index.status.State, index.status.Error = "error", runErr.Error()
		}
		index.mu.Unlock()
	}()
	index.session.engine.ctx = ctx
	// Incremental states already retain unchanged immutable file attribution;
	// keeping a second history-wide blame cache would only duplicate evidence.
	index.session.engine.disableBlameCache = true
	index.session.engine.gitSlots = make(chan struct{}, index.session.engine.workers)
	if err := index.persistMetadata(ctx); err != nil {
		return err
	}
	if err := index.loadActivities(ctx); err != nil {
		return err
	}
	completed, err := index.completedRevisions(ctx)
	if err != nil {
		return err
	}
	index.mu.Lock()
	index.status.Completed, index.status.Phase = len(completed), "revisions"
	index.mu.Unlock()
	if err := index.runRevisionPipeline(ctx, cancel, completed); err != nil {
		return err
	}
	index.mu.Lock()
	index.status.State, index.status.Phase = "ready", "ready"
	index.mu.Unlock()
	return nil
}

func (index *Index) completedRevisions(ctx context.Context) (map[string]bool, error) {
	rows, err := index.db.QueryContext(ctx, "SELECT sha FROM revisions WHERE fingerprint=?", index.fingerprint)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	current := map[string]bool{}
	for _, revision := range index.allRevisions {
		current[revision.sha] = true
	}
	completed := map[string]bool{}
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, err
		}
		if current[sha] {
			completed[sha] = true
		}
	}
	return completed, rows.Err()
}

func (index *Index) persistMetadata(ctx context.Context) error {
	e := index.session.engine
	index.mu.Lock()
	index.status.PhaseTotal, index.status.PhaseCompleted = len(e.activity), 0
	index.mu.Unlock()
	var ready int
	if err := index.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM indexed_heads WHERE fingerprint=? AND head=?", index.fingerprint, index.status.Head).Scan(&ready); err != nil {
		return err
	}
	if ready > 0 {
		index.mu.Lock()
		index.status.PhaseCompleted = len(e.activity)
		index.mu.Unlock()
		return nil
	}
	existing := map[string]bool{}
	rows, err := index.db.QueryContext(ctx, "SELECT sha,path FROM activity WHERE fingerprint=?", index.fingerprint)
	if err != nil {
		return err
	}
	for rows.Next() {
		var sha, path string
		if err := rows.Scan(&sha, &path); err != nil {
			rows.Close()
			return err
		}
		existing[sha+"\x00"+path] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Commit metadata is small and independent of the expensive blob scan.
	// Activity batches are independently atomic; cancellation resumes at the
	// first missing batch instead of discarding an entire large repository scan.
	if err := index.persistCommitMetadata(ctx); err != nil {
		return err
	}
	trees, err := index.activityTrees()
	if err != nil {
		return err
	}
	for offset := 0; offset < len(e.activity); offset += 256 {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(offset+256, len(e.activity))
		entries := []activityEntry{}
		files := []treeFile{}
		for _, entry := range e.activity[offset:end] {
			if existing[entry.sha+"\x00"+entry.path] {
				continue
			}
			file, exists := trees[entry.sha+"\x00"+entry.path]
			if !exists {
				return fmt.Errorf("missing Git activity blob for %s:%s", entry.sha, entry.path)
			}
			entries = append(entries, entry)
			files = append(files, file)
		}
		if err := e.classifyBatch(files); err != nil {
			return err
		}
		reasons := make([]string, len(files))
		for n, file := range files {
			reason, err := e.classify(file)
			if err != nil {
				return err
			}
			reasons[n] = reason
		}
		if err := index.persistActivityBatch(ctx, entries, reasons); err != nil {
			return err
		}
		index.mu.Lock()
		index.status.PhaseCompleted = end
		index.mu.Unlock()
	}
	_, err = index.db.ExecContext(ctx, "INSERT OR IGNORE INTO indexed_heads(fingerprint,head) VALUES(?,?)", index.fingerprint, index.status.Head)
	return err
}

func (index *Index) persistCommitMetadata(ctx context.Context) error {
	tx, err := index.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, "INSERT OR REPLACE INTO commits(fingerprint,sha,author_name,author_email,raw_email,author_date,commit_date,summary,parents) VALUES(?,?,?,?,?,?,?,?,?)")
	if err != nil {
		return err
	}
	defer statement.Close()
	for _, commit := range index.session.engine.commits {
		parents, _ := json.Marshal(commit.parents)
		if _, err := statement.ExecContext(ctx, index.fingerprint, commit.sha, commit.name, commit.email, commit.rawEmail, commit.authored.Format(time.RFC3339), commit.committed.Format(time.RFC3339), commit.summary, string(parents)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (index *Index) persistActivityBatch(ctx context.Context, entries []activityEntry, reasons []string) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := index.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, "INSERT OR IGNORE INTO activity(fingerprint,sha,path,person,organization,date_ns,changed_lines,is_merge,excluded_reason) VALUES(?,?,?,?,?,?,?,?,?)")
	if err != nil {
		return err
	}
	defer statement.Close()
	for n, entry := range entries {
		commit := index.session.engine.commits[entry.sha]
		merge := len(commit.parents) > 1
		if _, err := statement.ExecContext(ctx, index.fingerprint, entry.sha, entry.path, personID(commit), index.session.engine.config.organization(commit), commit.committed.UnixNano(), entry.changed, merge, reasons[n]); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (index *Index) loadActivities(ctx context.Context) error {
	rows, err := index.db.QueryContext(ctx, "SELECT sha,path,person,organization,date_ns,changed_lines FROM activity WHERE fingerprint=? AND is_merge=0 AND excluded_reason='' ORDER BY date_ns,sha,path", index.fingerprint)
	if err != nil {
		return err
	}
	defer rows.Close()
	entries := []indexedActivity{}
	for rows.Next() {
		var entry indexedActivity
		var date int64
		if err := rows.Scan(&entry.sha, &entry.path, &entry.person, &entry.organization, &date, &entry.changed); err != nil {
			return err
		}
		if _, reachable := index.session.engine.commits[entry.sha]; !reachable {
			continue
		}
		entry.date = time.Unix(0, date).UTC()
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	index.mu.Lock()
	index.activities = entries
	index.mu.Unlock()
	return nil
}

func (index *Index) persistRevision(ctx context.Context, snapshot Snapshot, date time.Time) error {
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(payload); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	aggregates := map[string]map[string]SeriesContribution{}
	for _, file := range snapshot.Files {
		scopes := []string{"", file.Path}
		parts := strings.Split(file.Path, "/")
		for n := 1; n < len(parts); n++ {
			scopes = append(scopes, strings.Join(parts[:n], "/"))
		}
		for _, scope := range scopes {
			if aggregates[scope] == nil {
				aggregates[scope] = map[string]SeriesContribution{}
			}
			for _, contribution := range file.Contributions {
				key := contribution.PersonID + "\x00" + contribution.OrganizationID
				value := aggregates[scope][key]
				value.PersonID, value.OrganizationID = contribution.PersonID, contribution.OrganizationID
				value.Lines += contribution.Lines
				aggregates[scope][key] = value
			}
		}
	}
	tx, err := index.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO revisions(fingerprint,sha,date_ns,snapshot) VALUES(?,?,?,?)", index.fingerprint, snapshot.Revision, date.UnixNano(), compressed.Bytes()); err != nil {
		return err
	}
	lines := 0
	for _, file := range snapshot.Files {
		lines += file.Lines
	}
	if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO revision_stats(fingerprint,sha,lines,files) VALUES(?,?,?,?)", index.fingerprint, snapshot.Revision, lines, len(snapshot.Files)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM aggregates WHERE fingerprint=? AND sha=?", index.fingerprint, snapshot.Revision); err != nil {
		return err
	}
	statement, err := tx.PrepareContext(ctx, "INSERT INTO aggregates(fingerprint,sha,scope,person,organization,lines) VALUES(?,?,?,?,?,?)")
	if err != nil {
		return err
	}
	defer statement.Close()
	for scope, values := range aggregates {
		for _, value := range values {
			if _, err := statement.ExecContext(ctx, index.fingerprint, snapshot.Revision, scope, value.PersonID, value.OrganizationID, value.Lines); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (index *Index) readSnapshot(ctx context.Context, sha string, date time.Time) (Snapshot, error) {
	if sha == "" {
		return Snapshot{Date: date.Format(time.RFC3339Nano), Files: []File{}, Exclusions: map[string]int{}, Warnings: []string{"No commit existed at this cutoff."}}, nil
	}
	state, err := index.readRevisionState(ctx, sha)
	if err != nil {
		return Snapshot{}, err
	}
	return state.snapshot(sha, date), nil
}

func (index *Index) Close() error {
	index.mu.Lock()
	index.closed = true
	if index.cancel != nil {
		index.cancel()
	}
	index.mu.Unlock()
	index.gate <- struct{}{}
	defer func() { <-index.gate }()
	return index.db.Close()
}

func (index *Index) activityTrees() (map[string]treeFile, error) {
	e := index.session.engine
	output, err := e.git("log", "-z", "--format=%x00%H", "--raw", "--no-abbrev", "--no-renames", "--diff-merges=first-parent", "--no-ext-diff", "--no-textconv", index.status.Head)
	if err != nil {
		return nil, err
	}
	fields := bytes.Split(output, []byte{0})
	result := map[string]treeFile{}
	blobs := map[string]bool{}
	sha := ""
	header := false
	for n := 0; n < len(fields); n++ {
		field := string(fields[n])
		if field == "" {
			header = true
			continue
		}
		if header {
			sha = field
			header = false
			continue
		}
		field = strings.TrimPrefix(field, "\n")
		if !strings.HasPrefix(field, ":") {
			return nil, fmt.Errorf("invalid raw Git activity format")
		}
		parts := strings.Fields(strings.TrimPrefix(field, ":"))
		if len(parts) != 5 || n+1 >= len(fields) {
			return nil, fmt.Errorf("invalid raw Git activity metadata")
		}
		n++
		filename := string(fields[n])
		mode, blob := parts[1], parts[3]
		if strings.Trim(blob, "0") == "" {
			mode, blob = parts[0], parts[2]
		}
		result[sha+"\x00"+filename] = treeFile{path: filename, blob: blob, mode: mode}
		if mode != "160000" {
			blobs[blob] = true
		}
	}
	ids := make([]string, 0, len(blobs))
	for blob := range blobs {
		ids = append(ids, blob)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return result, nil
	}
	sizesOutput, err := e.gitInput([]byte(strings.Join(ids, "\n")+"\n"), "cat-file", "--batch-check=%(objectname) %(objecttype) %(objectsize)")
	if err != nil {
		return nil, err
	}
	sizes := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(sizesOutput)), "\n") {
		var sha, kind string
		var size int
		if _, err := fmt.Sscanf(line, "%s %s %d", &sha, &kind, &size); err != nil {
			return nil, err
		}
		sizes[sha] = size
	}
	for key, file := range result {
		file.size = sizes[file.blob]
		result[key] = file
	}
	return result, nil
}
