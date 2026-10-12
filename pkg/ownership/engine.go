package ownership

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxBlobBytes = 1024 * 1024

type commitInfo struct {
	sha, name, email, rawEmail, summary string
	ai                                  []AISignal
	authored, committed                 time.Time
	parents                             []string
}

type treeFile struct {
	path, blob, mode string
	size             int
}
type activityEntry struct {
	sha, path string
	changed   int
}
type cutoff struct {
	date time.Time
	sha  string
}
type engine struct {
	ctx         context.Context
	repo        string
	config      *affiliations
	commits     map[string]commitInfo
	people      map[string]Person
	identities  map[string]Person
	activity    []activityEntry
	workers     int
	gitSlots    chan struct{}
	mailmapBlob string
	// Content classification is safe to reuse by immutable blob id. Blame is
	// deliberately not cached by blob: a revert can recreate the same content
	// with a different line attribution.
	classification     map[string]string
	classificationMu   sync.Mutex
	blames             map[string]File
	blamesMu           sync.Mutex
	disableBlameCache  bool
	blameCacheBytes    int
	blameCacheOrder    []string
	firstParentOrder   []string
	firstParentChanges map[string][]string
	anchors            map[string]map[string]string
	trees              map[string][]treeFile
	ancestorSets       map[string]map[string]bool
}

// Build is the one-shot wrapper. Use NewSession when exploring several ranges.
func Build(ctx context.Context, repoPath string, options Options) (*Dataset, error) {
	session, err := NewSession(ctx, repoPath, options)
	if err != nil {
		return nil, err
	}
	return session.Build(ctx, options)
}

// Session freezes HEAD, affiliation configuration, identity metadata and history.
// Builds share immutable caches and are serialized with cancellation-aware waits.
type Session struct {
	engine  *engine
	base    Dataset
	chain   []string
	options Options
	gate    chan struct{}
}

func NewSession(ctx context.Context, repoPath string, options Options) (*Session, error) {
	var err error
	options, err = normalizeOptions(options)
	if err != nil {
		return nil, err
	}
	config, err := loadAffiliations(options.ConfigPath)
	if err != nil {
		return nil, err
	}
	absolutePath, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, err
	}
	e := &engine{ctx: ctx, repo: absolutePath, config: config, workers: options.Workers,
		commits: map[string]commitInfo{}, people: map[string]Person{}, identities: map[string]Person{}, classification: map[string]string{}, blames: map[string]File{}, anchors: map[string]map[string]string{}, trees: map[string][]treeFile{}, ancestorSets: map[string]map[string]bool{}}
	root, err := e.git("rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("open repository: %w", err)
	}
	e.repo = strings.TrimSpace(string(root))
	ref := "HEAD"
	if options.Revision != "" {
		if _, err := hex.DecodeString(options.Revision); err != nil || (len(options.Revision) != 40 && len(options.Revision) != 64) {
			return nil, fmt.Errorf("revision must be a full commit SHA")
		}
		ref = options.Revision
	}
	headBytes, err := e.git("rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("repository has no readable HEAD commit: %w", err)
	}
	head := strings.TrimSpace(string(headBytes))
	if options.Revision != "" {
		if _, err := e.git("cat-file", "-e", head+":.mailmap"); err == nil {
			e.mailmapBlob = head + ":.mailmap"
		}
	}
	data := Dataset{Repository: Repository{Name: filepath.Base(e.repo), Path: e.repo, Head: head},
		People: []Person{}, Organizations: []Organization{}, Snapshots: []Snapshot{}, Warnings: []string{},
		Method: Method{
			Ownership:   "Surviving text lines attributed by git blame at each exact revision, including blank lines and documentation. Whole-file renames are followed; copies, moved blocks and whitespace changes use Git's default blame rules. This is recorded attribution, not expertise, responsibility or decision power.",
			Activity:    "Added + deleted lines in non-merge commits reachable from the selected revision, in (cutoff − window, cutoff], using recorded committer dates (not merge/integration time). Merge conflict resolutions are excluded. Historical paths, including deleted or renamed paths, retain their activity. Pure renames contribute zero changed lines.",
			Affiliation: "At the attribution commit's author date: explicit person periods [from, until) override configured domains, including unknown gaps. Domain matching uses the original commit email, never its mailmapped replacement. Unmatched emails are unknown; configured domains are signals, not verified employment. Existing lines keep their original affiliation after a person's job change.",
			Identity:    "Git mailmap rules at session creation, including an uncommitted working-tree .mailmap, are frozen consistently for all snapshots. People are keyed by canonical email (name only when email is missing). Snapshot dates are UTC cutoffs; each revision is the most recent first-parent ancestor at or before that cutoff. Ownership evidence dates are author dates; activity evidence dates are committer dates. Evidence includes every distinct attribution or activity commit for each person/organization/file.",
			WindowDays:  options.WindowDays,
			Exclusions:  []string{"Dependency/cache directories: node_modules, vendor, .venv, venv, .next, .nuxt, .yarn, __pycache__, .terraform, coverage", "Known lockfiles, source maps, minified assets and generated source filenames", "Files with generated-code header markers", "Binary files containing a NUL byte", "Files larger than 1 MiB", "Symlinks and Git submodules"},
		}}
	if remote, remoteErr := e.git("config", "--get", "remote.origin.url"); remoteErr == nil {
		data.Repository.Remote = sanitizeRemote(strings.TrimSpace(string(remote)))
	}
	if shallow, shallowErr := e.git("rev-parse", "--is-shallow-repository"); shallowErr == nil {
		data.Repository.Shallow = strings.TrimSpace(string(shallow)) == "true"
	}
	if data.Repository.Shallow {
		data.Warnings = append(data.Warnings, "This is a shallow clone: boundary commits inherit attribution from missing history. Deepen the clone for reliable historical ownership.")
	}
	if options.ConfigPath == "" {
		data.Warnings = append(data.Warnings, "No affiliation configuration: all organization shares are explicitly unknown. Configure vendors or dated person affiliations to explore companies.")
	}
	if err := e.readCommits(head); err != nil {
		return nil, err
	}
	chainBytes, err := e.git("rev-list", "--first-parent", head)
	if err != nil {
		return nil, err
	}
	chain := strings.Fields(string(chainBytes))
	if err := e.readActivity(head); err != nil {
		return nil, err
	}
	if err := e.readFirstParentChanges(head); err != nil {
		return nil, err
	}

	return &Session{engine: e, base: data, chain: chain, options: options, gate: make(chan struct{}, 1)}, nil
}

// Build resolves a new time range against the session's frozen HEAD. Empty dates
// use repository bounds. WindowDays/Workers zero inherit session defaults;
// Snapshots zero always means calendar sampling. ConfigPath cannot be changed.
func (s *Session) Build(ctx context.Context, options Options) (*Dataset, error) {
	select {
	case s.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-s.gate }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.ConfigPath != "" && options.ConfigPath != s.options.ConfigPath {
		return nil, fmt.Errorf("affiliation configuration is frozen; create a new session to change it")
	}
	if options.WindowDays == 0 {
		options.WindowDays = s.options.WindowDays
	}
	if options.Workers == 0 {
		options.Workers = s.options.Workers
	}
	if options.Interval == "" {
		options.Interval = s.options.Interval
	}
	var err error
	options, err = normalizeOptions(options)
	if err != nil {
		return nil, err
	}
	e := s.engine
	e.ctx, e.workers = ctx, options.Workers
	e.people = map[string]Person{}
	data := s.base
	data.People, data.Organizations, data.Snapshots = []Person{}, []Organization{}, []Snapshot{}
	data.Warnings = append([]string{}, s.base.Warnings...)
	data.Method.WindowDays = options.WindowDays
	cutoffs, timeline, skewed, err := e.selectCutoffs(s.chain, options)
	if err != nil {
		return nil, err
	}
	data.Timeline = timeline
	if skewed {
		data.Warnings = append(data.Warnings, "First-parent committer timestamps are not monotonic. Calendar cutoffs select the newest eligible ancestor in graph order; ancestor timestamps may be later than descendants.")
	}
	hasHistory := false
	for _, selected := range cutoffs {
		if selected.sha != "" {
			hasHistory = true
			break
		}
	}
	if !hasHistory {
		data.Warnings = append(data.Warnings, "No first-parent commit exists at or before the requested period.")
	}
	for _, selected := range cutoffs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		snapshot, err := e.snapshot(selected, options.WindowDays)
		if err != nil {
			return nil, fmt.Errorf("snapshot %s: %w", selected.date.Format(time.RFC3339), err)
		}
		data.Snapshots = append(data.Snapshots, snapshot)
	}
	for _, person := range e.people {
		data.People = append(data.People, person)
	}
	sort.Slice(data.People, func(i, j int) bool { return data.People[i].ID < data.People[j].ID })
	for _, organization := range e.config.organizations {
		data.Organizations = append(data.Organizations, organization)
	}
	sort.Slice(data.Organizations, func(i, j int) bool { return data.Organizations[i].ID < data.Organizations[j].ID })
	e.groupPeople(&data)
	return &data, nil
}

func (e *engine) git(args ...string) ([]byte, error) {
	return e.gitInput(nil, args...)
}

func (e *engine) gitInput(input []byte, args ...string) ([]byte, error) {
	if e.gitSlots != nil {
		select {
		case e.gitSlots <- struct{}{}:
		case <-e.ctx.Done():
			return nil, e.ctx.Err()
		}
		defer func() { <-e.gitSlots }()
	}
	commandArgs := []string{"--literal-pathspecs", "-C", e.repo, "-c", "core.quotePath=false", "-c", "log.showSignature=false"}
	if e.mailmapBlob != "" {
		commandArgs = append(commandArgs, "-c", "mailmap.blob="+e.mailmapBlob)
	}
	commandArgs = append(commandArgs, args...)
	cmd := exec.CommandContext(e.ctx, "git", commandArgs...)
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		if e.ctx.Err() != nil {
			return nil, e.ctx.Err()
		}
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}

func (e *engine) readCommits(head string) error {
	output, err := e.git("log", "-z", "--format=%H%x00%aN%x00%aE%x00%ae%x00%at%x00%ct%x00%s%x00%P%x00%B", head)
	if err != nil {
		return err
	}
	fields := bytes.Split(bytes.TrimSuffix(output, []byte{0}), []byte{0})
	if len(fields)%9 != 0 {
		return fmt.Errorf("unexpected Git commit metadata format")
	}
	for i := 0; i < len(fields); i += 9 {
		authorTime, err := strconv.ParseInt(string(fields[i+4]), 10, 64)
		if err != nil {
			return fmt.Errorf("invalid author timestamp: %w", err)
		}
		commitTime, err := strconv.ParseInt(string(fields[i+5]), 10, 64)
		if err != nil {
			return fmt.Errorf("invalid committer timestamp: %w", err)
		}
		c := commitInfo{sha: string(fields[i]), name: string(fields[i+1]), email: string(fields[i+2]), rawEmail: string(fields[i+3]), authored: time.Unix(authorTime, 0).UTC(), committed: time.Unix(commitTime, 0).UTC(), summary: string(fields[i+6])}
		c.parents = strings.Fields(string(fields[i+7]))
		c.ai = detectAISignals(c.name, c.rawEmail, string(fields[i+8]))
		e.commits[c.sha] = c
	}
	// A canonical email may have several display names. Use the name from its
	// latest recorded commit, with a stable SHA tie-breaker.
	commits := make([]commitInfo, 0, len(e.commits))
	for _, c := range e.commits {
		commits = append(commits, c)
	}
	sort.Slice(commits, func(i, j int) bool {
		if !commits[i].committed.Equal(commits[j].committed) {
			return commits[i].committed.After(commits[j].committed)
		}
		return commits[i].sha < commits[j].sha
	})
	for _, c := range commits {
		id := personID(c)
		if _, exists := e.identities[id]; !exists {
			e.identities[id] = Person{ID: id, Name: c.name, Email: c.email}
		}
		person := e.identities[id]
		person.Emails = appendUniqueEmail(person.Emails, c.email)
		person.Emails = appendUniqueEmail(person.Emails, c.rawEmail)
		e.identities[id] = person
	}
	return nil
}

func (e *engine) readActivity(head string) error {
	output, err := e.git("log", "-z", "--format=%H", "--numstat", "--diff-merges=first-parent", "--find-renames=50%", "--no-ext-diff", "--no-textconv", head)
	if err != nil {
		return err
	}
	fields := bytes.Split(output, []byte{0})
	sha := ""
	for i := 0; i < len(fields); i++ {
		field := strings.TrimPrefix(string(fields[i]), "\n")
		if field == "" {
			continue
		}
		if _, exists := e.commits[field]; exists {
			sha = field
			continue
		}
		columns := strings.SplitN(field, "\t", 3)
		if len(columns) != 3 || sha == "" {
			return fmt.Errorf("unexpected Git numstat format")
		}
		filename := columns[2]
		if filename == "" {
			if i+2 >= len(fields) {
				return fmt.Errorf("incomplete renamed path in Git numstat")
			}
			filename = string(fields[i+2])
			i += 2
		}
		if columns[0] == "-" || columns[1] == "-" {
			continue
		}
		added, err := strconv.Atoi(columns[0])
		if err != nil {
			return fmt.Errorf("invalid numstat additions: %w", err)
		}
		deleted, err := strconv.Atoi(columns[1])
		if err != nil {
			return fmt.Errorf("invalid numstat deletions: %w", err)
		}
		if added+deleted > 0 {
			e.activity = append(e.activity, activityEntry{sha: sha, path: filename, changed: added + deleted})
		}
	}
	return nil
}

func (e *engine) tree(revision string) ([]treeFile, error) {
	if files, exists := e.trees[revision]; exists {
		return files, nil
	}
	output, err := e.git("ls-tree", "-r", "-l", "-z", revision)
	if err != nil {
		return nil, err
	}
	files := []treeFile{}
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
		size, _ := strconv.Atoi(metadata[3]) // submodules use "-", classified by mode
		files = append(files, treeFile{path: string(columns[1]), blob: metadata[2], mode: metadata[0], size: size})
	}
	e.trees[revision] = files
	if len(e.trees) > 4 {
		for sha := range e.trees {
			if sha != revision {
				delete(e.trees, sha)
				break
			}
		}
	}
	return files, nil
}

func (e *engine) snapshot(selected cutoff, windowDays int) (Snapshot, error) {
	return e.snapshotWithActivity(selected, windowDays, true)
}

func (e *engine) snapshotWithActivity(selected cutoff, windowDays int, includeActivity bool) (Snapshot, error) {
	snapshot := Snapshot{Date: selected.date.UTC().Format(time.RFC3339Nano), Revision: selected.sha, Files: []File{}, Exclusions: map[string]int{}, Warnings: []string{}}
	if selected.sha == "" {
		snapshot.Warnings = append(snapshot.Warnings, "No commit existed at this cutoff.")
		return snapshot, nil
	}
	files, err := e.tree(selected.sha)
	if err != nil {
		return snapshot, err
	}
	anchors := e.blameAnchors(selected.sha)
	type fileResult struct {
		index  int
		file   File
		reason string
		err    error
	}
	jobs := make(chan int)
	results := make(chan fileResult, len(files))
	var workers sync.WaitGroup
	for worker := 0; worker < e.workers; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				file := files[index]
				reason, err := e.classify(file)
				result := fileResult{index: index, reason: reason, err: err}
				if err == nil && reason == "" {
					result.file, result.err = e.blame(selected.sha, file.path, anchors[file.path])
				}
				results <- result
			}
		}()
	}
	go func() {
		defer close(jobs)
		for index := range files {
			jobs <- index
		}
	}()
	go func() { workers.Wait(); close(results) }()
	ordered := make([]fileResult, len(files))
	for result := range results {
		ordered[result.index] = result
	}
	currentReasons := map[string]string{}
	byPath := map[string]*File{}
	for _, result := range ordered {
		if result.err != nil {
			return snapshot, fmt.Errorf("file %q: %w", files[result.index].path, result.err)
		}
		currentReasons[files[result.index].path] = result.reason
		if result.reason != "" {
			snapshot.ExcludedFiles++
			snapshot.Exclusions[result.reason]++
			continue
		}
		file := result.file
		byPath[file.Path] = &file
	}
	if includeActivity {
		ancestors, exists := e.ancestorSets[selected.sha]
		if !exists {
			ancestorOutput, err := e.git("rev-list", selected.sha)
			if err != nil {
				return snapshot, err
			}
			ancestors = map[string]bool{}
			for _, sha := range strings.Fields(string(ancestorOutput)) {
				ancestors[sha] = true
			}
			e.ancestorSets[selected.sha] = ancestors
			if len(e.ancestorSets) > 4 {
				for sha := range e.ancestorSets {
					if sha != selected.sha {
						delete(e.ancestorSets, sha)
						break
					}
				}
			}
		}
		windowStart := selected.date.Add(-time.Duration(windowDays) * 24 * time.Hour)
		historicalReasons := map[string]string{}
		for _, entry := range e.activity {
			c := e.commits[entry.sha]
			if len(c.parents) > 1 {
				continue
			}
			if !ancestors[entry.sha] || !c.committed.After(windowStart) || c.committed.After(selected.date) {
				continue
			}
			reason, exists := currentReasons[entry.path]
			if !exists {
				key := entry.sha + ":" + entry.path
				var checked bool
				reason, checked = historicalReasons[key]
				if !checked {
					reason, err = e.classifyHistorical(entry.sha, entry.path)
					if err != nil {
						return snapshot, err
					}
					historicalReasons[key] = reason
				}
			}
			if reason != "" {
				continue
			}
			file := byPath[entry.path]
			if file == nil {
				file = &File{Path: entry.path, Contributions: []Contribution{}}
				byPath[entry.path] = file
			}
			personID := personID(c)
			organizationID := e.config.organization(c)
			contribution := contributionFor(file, personID, organizationID)
			contribution.ChangedLines += entry.changed
			addEvidence(contribution, Evidence{SHA: c.sha, Date: c.committed.Format(time.RFC3339), Summary: c.summary, Kind: "activity", Path: entry.path})
		}
	}
	for _, file := range byPath {
		sort.Slice(file.Contributions, func(i, j int) bool {
			a, b := file.Contributions[i], file.Contributions[j]
			if a.PersonID != b.PersonID {
				return a.PersonID < b.PersonID
			}
			return a.OrganizationID < b.OrganizationID
		})
		for _, contribution := range file.Contributions {
			e.people[contribution.PersonID] = e.identities[contribution.PersonID]
		}
		snapshot.Files = append(snapshot.Files, *file)
	}
	sort.Slice(snapshot.Files, func(i, j int) bool { return snapshot.Files[i].Path < snapshot.Files[j].Path })
	return snapshot, nil
}

func personID(c commitInfo) string {
	identity := strings.ToLower(strings.TrimSpace(c.email))
	if identity == "" {
		identity = "name:" + c.name
	}
	digest := sha256.Sum256([]byte(identity))
	return "person-" + hex.EncodeToString(digest[:8])
}

func contributionFor(file *File, personID, organizationID string) *Contribution {
	for i := range file.Contributions {
		if file.Contributions[i].PersonID == personID && file.Contributions[i].OrganizationID == organizationID {
			return &file.Contributions[i]
		}
	}
	file.Contributions = append(file.Contributions, Contribution{PersonID: personID, OrganizationID: organizationID, Evidence: []Evidence{}})
	return &file.Contributions[len(file.Contributions)-1]
}

func addEvidence(contribution *Contribution, evidence Evidence) {
	for _, current := range contribution.Evidence {
		if current.Kind == evidence.Kind {
			if current.SHA == evidence.SHA && current.Path == evidence.Path {
				return
			}
		}
	}
	contribution.Evidence = append(contribution.Evidence, evidence)
}

func (e *engine) blame(revision, filename, anchor string) (File, error) {
	if anchor == "" {
		anchor = revision
	}
	cacheKey := anchor + ":" + filename
	e.blamesMu.Lock()
	cached, exists := e.blames[cacheKey]
	e.blamesMu.Unlock()
	if exists && !e.disableBlameCache {
		return cloneFile(cached), nil
	}
	file := File{Path: filename, Contributions: []Contribution{}}
	// Incremental output has the same attribution as line-porcelain, with one
	// count per contiguous block and an original filename terminating each
	// entry. It avoids copying source text and repeated metadata for every line.
	// https://git-scm.com/docs/git-blame#_incremental_output
	output, err := e.git("-c", "blame.ignoreRevsFile=", "blame", "--no-textconv", "--incremental", revision, "--", filename)
	if err != nil {
		return file, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 4096), maxBlobBytes+65536)
	var c commitInfo
	seenEvidence := map[string]bool{}
	blockLines := 0
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) == 4 {
			if commit, exists := e.commits[fields[0]]; exists {
				if blockLines != 0 {
					return file, fmt.Errorf("unterminated blame block")
				}
				blockLines, err = strconv.Atoi(fields[3])
				if err != nil || blockLines <= 0 {
					return file, fmt.Errorf("invalid blame block count")
				}
				c = commit
				continue
			}
		}
		if strings.HasPrefix(line, "filename ") {
			if blockLines == 0 || c.sha == "" {
				return file, fmt.Errorf("missing commit metadata in blame block")
			}
			originalPath := strings.TrimPrefix(line, "filename ")
			if strings.HasPrefix(originalPath, "\"") {
				if unquoted, err := strconv.Unquote(originalPath); err == nil {
					originalPath = unquoted
				}
			}
			contribution := contributionFor(&file, personID(c), e.config.organization(c))
			contribution.Lines += blockLines
			file.Lines += blockLines
			key := c.sha + "\x00" + originalPath
			if !seenEvidence[key] {
				addEvidence(contribution, Evidence{SHA: c.sha, Date: c.authored.Format(time.RFC3339), Summary: c.summary, Kind: "ownership", Path: originalPath})
				seenEvidence[key] = true
			}
			blockLines = 0
		}
	}
	if err := scanner.Err(); err != nil {
		return file, fmt.Errorf("parse blame: %w", err)
	}
	if blockLines != 0 {
		return file, fmt.Errorf("unterminated blame block")
	}
	for n := range file.Contributions {
		sort.Slice(file.Contributions[n].Evidence, func(a, b int) bool {
			x, y := file.Contributions[n].Evidence[a], file.Contributions[n].Evidence[b]
			return x.SHA+"\x00"+x.Path < y.SHA+"\x00"+y.Path
		})
	}
	if !e.disableBlameCache {
		e.blamesMu.Lock()
		cost := fileMemory(file)
		for e.blameCacheBytes+cost > 64*1024*1024 && len(e.blameCacheOrder) > 0 {
			key := e.blameCacheOrder[0]
			e.blameCacheOrder = e.blameCacheOrder[1:]
			e.blameCacheBytes -= fileMemory(e.blames[key])
			delete(e.blames, key)
		}
		if cost <= 64*1024*1024 {
			e.blames[cacheKey] = cloneFile(file)
			e.blameCacheOrder = append(e.blameCacheOrder, cacheKey)
			e.blameCacheBytes += cost
		}
		e.blamesMu.Unlock()
	}
	return file, nil
}

func fileMemory(file File) int {
	bytes := len(file.Path) + 64
	for _, contribution := range file.Contributions {
		bytes += len(contribution.PersonID) + len(contribution.OrganizationID) + 64
		for _, evidence := range contribution.Evidence {
			bytes += len(evidence.SHA) + len(evidence.Date) + len(evidence.Summary) + len(evidence.Path) + len(evidence.Kind) + 80
		}
	}
	return bytes
}

func cloneFile(file File) File {
	copy := File{Path: file.Path, Lines: file.Lines, Contributions: make([]Contribution, len(file.Contributions))}
	for i, contribution := range file.Contributions {
		copy.Contributions[i] = contribution
		copy.Contributions[i].Evidence = append([]Evidence{}, contribution.Evidence...)
	}
	return copy
}

var generatedHeader = regexp.MustCompile(`(?im)^(?://|#|/\*|\*|<!--|;)[ \t]*(?:code generated .+do not edit|auto(?:matically)?[- ]generated|@generated\b|this file (?:is|was) (?:auto[- ]?)?generated)`)

func excludedPath(filename string) string {
	for _, segment := range strings.Split(filename, "/") {
		switch segment {
		case "node_modules", "vendor", ".venv", "venv", ".next", ".nuxt", ".yarn", "__pycache__", ".terraform", "coverage":
			return "dependency or cache directory"
		}
	}
	base := strings.ToLower(path.Base(filename))
	switch base {
	case "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "cargo.lock", "go.sum", "composer.lock", "gemfile.lock", "poetry.lock", "uv.lock", "bun.lockb", "bun.lock":
		return "generated or lockfile"
	}
	if strings.HasSuffix(base, ".map") || strings.HasSuffix(base, ".min.js") || strings.HasSuffix(base, ".min.css") || strings.Contains(base, ".generated.") || strings.HasSuffix(base, ".pb.go") {
		return "generated or lockfile"
	}
	return ""
}

func (e *engine) classify(file treeFile) (string, error) {
	if file.mode == "160000" {
		return "submodule", nil
	}
	if file.mode == "120000" {
		return "symlink", nil
	}
	if reason := excludedPath(file.path); reason != "" {
		return reason, nil
	}
	if file.size > maxBlobBytes {
		return "larger than 1 MiB", nil
	}
	e.classificationMu.Lock()
	reason, exists := e.classification[file.blob]
	e.classificationMu.Unlock()
	if exists {
		return reason, nil
	}
	content, err := e.git("cat-file", "blob", file.blob)
	if err != nil {
		return "", err
	}
	reason = classifyContent(content)
	e.classificationMu.Lock()
	e.classification[file.blob] = reason
	e.classificationMu.Unlock()
	return reason, nil
}

func classifyContent(content []byte) string {
	if bytes.IndexByte(content, 0) >= 0 {
		return "binary"
	}
	if generatedHeader.Match(content[:min(len(content), 8192)]) {
		return "generated header"
	}
	return ""
}

// Read bounded batches through one Git process. A large history can have
// hundreds of thousands of blobs; starting cat-file once per blob dominates
// metadata preparation. Parsing uses byte sizes, so arbitrary file content is
// never mistaken for a batch delimiter.
func (e *engine) classifyBatch(files []treeFile) error {
	ids := []string{}
	seen := map[string]bool{}
	e.classificationMu.Lock()
	for _, file := range files {
		if file.mode == "160000" || file.mode == "120000" || file.size > maxBlobBytes || excludedPath(file.path) != "" {
			continue
		}
		if _, exists := e.classification[file.blob]; exists || seen[file.blob] {
			continue
		}
		seen[file.blob] = true
		ids = append(ids, file.blob)
	}
	e.classificationMu.Unlock()
	for offset := 0; offset < len(ids); offset += 64 {
		batch := ids[offset:min(offset+64, len(ids))]
		output, err := e.gitInput([]byte(strings.Join(batch, "\n")+"\n"), "cat-file", "--batch")
		if err != nil {
			return err
		}
		parsed := map[string]string{}
		for _, sha := range batch {
			newline := bytes.IndexByte(output, '\n')
			if newline < 0 {
				return fmt.Errorf("invalid Git blob batch header")
			}
			header := strings.Fields(string(output[:newline]))
			if len(header) != 3 || header[0] != sha || header[1] != "blob" {
				return fmt.Errorf("invalid Git blob batch object %s", sha)
			}
			size, err := strconv.Atoi(header[2])
			if err != nil || size < 0 || size > maxBlobBytes || len(output) < newline+size+2 {
				return fmt.Errorf("invalid Git blob batch size for %s", sha)
			}
			content := output[newline+1 : newline+1+size]
			parsed[sha] = classifyContent(content)
			output = output[newline+size+2:]
		}
		e.classificationMu.Lock()
		for sha, reason := range parsed {
			e.classification[sha] = reason
		}
		e.classificationMu.Unlock()
	}
	return nil
}

func (e *engine) classifyHistorical(sha, filename string) (string, error) {
	if reason := excludedPath(filename); reason != "" {
		return reason, nil
	}
	for _, revision := range []string{sha, sha + "^"} {
		output, err := e.git("ls-tree", "-l", "-z", revision, "--", filename)
		if err != nil {
			return "", err
		}
		if len(output) == 0 {
			continue
		}
		columns := bytes.SplitN(bytes.TrimSuffix(output, []byte{0}), []byte{'\t'}, 2)
		if len(columns) != 2 {
			return "", fmt.Errorf("invalid historical tree entry for %q", filename)
		}
		metadata := strings.Fields(string(columns[0]))
		if len(metadata) != 4 {
			return "", fmt.Errorf("invalid historical file metadata for %q", filename)
		}
		size, _ := strconv.Atoi(metadata[3])
		return e.classify(treeFile{path: filename, blob: metadata[2], mode: metadata[0], size: size})
	}
	return "", fmt.Errorf("cannot classify historical path %q at %s", filename, sha)
}

func sanitizeRemote(remote string) string {
	// A remote can contain HTTPS credentials. The UI needs only its public form.
	if scheme := strings.Index(remote, "://"); scheme >= 0 {
		rest := remote[scheme+3:]
		if at := strings.Index(rest, "@"); at >= 0 {
			remote = remote[:scheme+3] + rest[at+1:]
		}
	}
	return remote
}
