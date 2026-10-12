package ownership

// AI provenance is an independent dimension of a Git commit, never a replacement
// for its author. Missing declarations carry no evidence of unassisted work.
import (
	"context"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"time"
)

type AISignal struct {
	Tool     string `json:"tool"`
	Kind     string `json:"kind"`
	Evidence string `json:"evidence"`
}

var aiTools = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"Claude", regexp.MustCompile(`(?i)\bclaude(?:\s+(?:code|sonnet|opus|haiku|fable)\b|\s*<|\s*$)|@anthropic\.com\b`)},
	{"Copilot", regexp.MustCompile(`(?i)\b(?:github[ -])?copilot\b`)},
	{"Cursor", regexp.MustCompile(`(?i)\bcursor\b|@cursor\.(?:com|sh)\b`)},
	{"Codex", regexp.MustCompile(`(?i)\bcodex\b`)},
	{"ChatGPT", regexp.MustCompile(`(?i)\bchatgpt\b`)},
	{"Gemini", regexp.MustCompile(`(?i)\bgemini\b`)},
	{"Aider", regexp.MustCompile(`(?i)\baider\b`)},
	{"Windsurf", regexp.MustCompile(`(?i)\b(?:windsurf|codeium)\b`)},
}
var aiDeclaration = regexp.MustCompile(`(?i)^(co-authored-by|assisted-by|ai-assisted-by|generated-by):\s*(.+)$`)
var aiGenerated = regexp.MustCompile(`(?i)^(?:🤖\s*)?(?:generated|written|created|implemented|assisted)\s+(?:with|by|using)\s+(.+)$`)

func detectAISignals(name, email, body string) []AISignal {
	var signals []AISignal
	seen := map[string]bool{}
	add := func(tool, kind, evidence string) {
		key := tool + "\x00" + strings.ToLower(evidence)
		if !seen[key] {
			signals = append(signals, AISignal{tool, kind, evidence})
			seen[key] = true
		}
	}
	// A known agent identity is stronger evidence than an ordinary human name.
	identity := strings.ToLower(name + " <" + email + ">")
	if strings.Contains(identity, "copilot") && (strings.Contains(identity, "[bot]") || strings.Contains(identity, "@users.noreply.github.com")) {
		add("Copilot", "agent-author", name+" <"+email+">")
	}
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		match, kind := aiDeclaration.FindStringSubmatch(line), "trailer"
		var value string
		if match != nil {
			value = match[2]
		} else if match = aiGenerated.FindStringSubmatch(line); match != nil {
			value, kind = match[1], "declaration"
		}
		if value == "" {
			continue
		}
		for _, tool := range aiTools {
			if tool.pattern.MatchString(value) {
				add(tool.name, kind, line)
			}
		}
	}
	return signals
}

type AIFilter struct {
	From, To, Person, Path, Tool, Evidence string
	Offset, Limit                          int
}
type AICounts struct {
	Commits     int `json:"commits"`
	Declared    int `json:"declared"`
	Represented int `json:"represented"`
}
type AIPerson struct {
	Person
	AICounts
}
type AIBucket struct {
	Key string `json:"key"`
	AICounts
}
type AICommit struct {
	SHA            string     `json:"sha"`
	Date           string     `json:"date"`
	Summary        string     `json:"summary"`
	PersonID       string     `json:"personId"`
	Author         string     `json:"author"`
	Email          string     `json:"email"`
	Merge          bool       `json:"merge"`
	Signals        []AISignal `json:"signals"`
	Paths          []string   `json:"paths"`
	SurvivingPaths []string   `json:"survivingPaths"`
}
type AIReport struct {
	Repository Repository `json:"repository"`
	From       string     `json:"from"`
	To         string     `json:"to"`
	FirstDate  string     `json:"firstDate"`
	LastDate   string     `json:"lastDate"`
	Revision   string     `json:"revision"`
	AICounts
	People        []AIPerson `json:"people"`
	Months        []AIBucket `json:"months"`
	Domains       []AIBucket `json:"domains"`
	Tools         []AIBucket `json:"tools"`
	Evidence      []AICommit `json:"evidence"`
	EvidenceTotal int        `json:"evidenceTotal"`
	Offset        int        `json:"offset"`
	Limit         int        `json:"limit"`
}
type aiCatalog struct {
	commits     []AICommit
	people      map[string]Person
	first, last time.Time
}

// All metadata is parsed once when the frozen Git session opens. The lightweight
// path catalogue reuses SQLite activity rows; no blame or revision rebuild runs.
func (index *Index) loadAICatalog(ctx context.Context) (*aiCatalog, error) {
	index.aiMu.Lock()
	defer index.aiMu.Unlock()
	if index.aiCatalog != nil {
		return index.aiCatalog, nil
	}
	resolved, people := index.session.engine.personGroups()
	catalog := &aiCatalog{people: people}
	paths := map[string][]string{}
	rows, err := index.db.QueryContext(ctx, "SELECT sha,path FROM activity WHERE fingerprint=? AND excluded_reason='' ORDER BY sha,path", index.fingerprint)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var sha, path string
		if err := rows.Scan(&sha, &path); err != nil {
			rows.Close()
			return nil, err
		}
		paths[sha] = append(paths[sha], path)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, c := range index.session.engine.commits {
		if catalog.first.IsZero() || c.committed.Before(catalog.first) {
			catalog.first = c.committed
		}
		if c.committed.After(catalog.last) {
			catalog.last = c.committed
		}
		id := resolved[personID(c)]
		catalog.commits = append(catalog.commits, AICommit{SHA: c.sha, Date: c.committed.Format(time.RFC3339), Summary: c.summary, PersonID: id, Author: people[id].Name, Email: people[id].Email, Merge: len(c.parents) > 1, Signals: c.ai, Paths: paths[c.sha]})
	}
	sort.Slice(catalog.commits, func(i, j int) bool {
		a, b := catalog.commits[i], catalog.commits[j]
		if a.Date != b.Date {
			return a.Date > b.Date
		}
		return a.SHA < b.SHA
	})
	index.aiCatalog = catalog
	return catalog, nil
}

func aiPathMatch(path, scope string) bool {
	return scope == "" || path == scope || strings.HasPrefix(path, scope+"/")
}
func aiBucket(values map[string]AICounts, key string, declared, represented bool) {
	v := values[key]
	v.Commits++
	if declared {
		v.Declared++
	}
	if represented {
		v.Represented++
	}
	values[key] = v
}
func aiBuckets(values map[string]AICounts) []AIBucket {
	result := make([]AIBucket, 0, len(values))
	for key, value := range values {
		result = append(result, AIBucket{key, value})
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.Declared != b.Declared {
			return a.Declared > b.Declared
		}
		if a.Commits != b.Commits {
			return a.Commits > b.Commits
		}
		return a.Key < b.Key
	})
	return result
}

func (index *Index) AI(ctx context.Context, filter AIFilter) (*AIReport, error) {
	if err := index.queryReady(); err != nil {
		return nil, err
	}
	if filter.Offset < 0 || filter.Limit < 0 || filter.Limit > 200 {
		return nil, fmt.Errorf("invalid evidence page")
	}
	if filter.Limit == 0 {
		filter.Limit = 30
	}
	if filter.Path != "" && (!fs.ValidPath(filter.Path) || filter.Path == "." || strings.ContainsRune(filter.Path, 0)) {
		return nil, fmt.Errorf("choose a repository path")
	}
	if filter.Evidence != "" && filter.Evidence != "declared" && filter.Evidence != "unidentified" && filter.Evidence != "all" {
		return nil, fmt.Errorf("unknown evidence filter")
	}
	if filter.Tool != "" {
		valid := false
		for _, tool := range aiTools {
			valid = valid || tool.name == filter.Tool
		}
		if !valid {
			return nil, fmt.Errorf("unknown AI tool")
		}
	}
	var from, to time.Time
	var err error
	if filter.From != "" {
		from, err = parseDate(filter.From, false)
		if err != nil {
			return nil, err
		}
	}
	if filter.To != "" {
		to, err = parseDate(filter.To, true)
		if err != nil {
			return nil, err
		}
	}
	if !from.IsZero() && !to.IsZero() && from.After(to) {
		return nil, fmt.Errorf("start must be at or before end")
	}
	catalog, err := index.loadAICatalog(ctx)
	if err != nil {
		return nil, err
	}
	if from.IsZero() {
		from = catalog.first.Truncate(24 * time.Hour)
	}
	if to.IsZero() {
		to = catalog.last.Truncate(24 * time.Hour).Add(24*time.Hour - time.Nanosecond)
	}
	if from.After(to) {
		return nil, fmt.Errorf("start must be at or before end")
	}
	personID := ""
	if filter.Person != "" {
		for id, p := range catalog.people {
			if id == filter.Person || p.Email == filter.Person {
				personID = id
			}
			for _, email := range p.Emails {
				if email == filter.Person {
					personID = id
				}
			}
		}
		if personID == "" {
			return nil, fmt.Errorf("unknown contributor")
		}
	}
	history, err := index.history("")
	if err != nil {
		return nil, err
	}
	revision := history.revisionAt(to)
	state, err := index.readRevisionState(ctx, revision.sha)
	if err != nil {
		return nil, err
	}
	// Existing snapshots contain every distinct surviving attribution SHA. This
	// answers whether a commit is still represented, not how many AI-written lines.
	surviving := map[string][]string{}
	for path, file := range state.files {
		if !aiPathMatch(path, filter.Path) {
			continue
		}
		seen := map[string]bool{}
		for _, contribution := range file.Contributions {
			for _, proof := range contribution.Evidence {
				if proof.Kind == "ownership" && !seen[proof.SHA] {
					surviving[proof.SHA] = append(surviving[proof.SHA], path)
					seen[proof.SHA] = true
				}
			}
		}
	}
	for sha := range surviving {
		sort.Strings(surviving[sha])
	}
	report := &AIReport{Repository: index.session.base.Repository, From: from.Format("2006-01-02"), To: to.Format("2006-01-02"), FirstDate: catalog.first.Format("2006-01-02"), LastDate: catalog.last.Format("2006-01-02"), Revision: revision.sha, People: []AIPerson{}, Months: []AIBucket{}, Domains: []AIBucket{}, Tools: []AIBucket{}, Evidence: []AICommit{}, Offset: filter.Offset, Limit: filter.Limit}
	persons, months, domains, tools := map[string]AICounts{}, map[string]AICounts{}, map[string]AICounts{}, map[string]AICounts{}
	for month := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.UTC); !month.After(to); month = month.AddDate(0, 1, 0) {
		months[month.Format("2006-01")] = AICounts{}
		if len(months) > 2400 {
			return nil, fmt.Errorf("choose a period of at most 200 years")
		}
	}
	for _, c := range catalog.commits {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		date := index.session.engine.commits[c.SHA].committed
		if date.Before(from) || date.After(to) || personID != "" && c.PersonID != personID {
			continue
		}
		matchedPaths := []string{}
		for _, path := range c.Paths {
			if aiPathMatch(path, filter.Path) {
				matchedPaths = append(matchedPaths, path)
			}
		}
		if filter.Path != "" && len(matchedPaths) == 0 {
			continue
		}
		declared := false
		toolSet := map[string]bool{}
		for _, signal := range c.Signals {
			toolSet[signal.Tool] = true
			if filter.Tool == "" || filter.Tool == signal.Tool {
				declared = true
			}
		}
		represented := declared && len(surviving[c.SHA]) > 0
		report.Commits++
		if declared {
			report.Declared++
		}
		if represented {
			report.Represented++
		}
		aiBucket(persons, c.PersonID, declared, represented)
		aiBucket(months, date.Format("2006-01"), declared, represented)
		for tool := range toolSet {
			aiBucket(tools, tool, true, represented)
		}
		domainSet := map[string]bool{}
		for _, path := range matchedPaths {
			relative := path
			if filter.Path != "" {
				relative = strings.TrimPrefix(path, filter.Path+"/")
			}
			part, _, _ := strings.Cut(relative, "/")
			if filter.Path != "" && path != filter.Path {
				part = filter.Path + "/" + part
			}
			domainSet[part] = true
		}
		for domain := range domainSet {
			aiBucket(domains, domain, declared, represented)
		}
		show := declared
		if filter.Evidence == "all" {
			show = true
		} else if filter.Evidence == "unidentified" {
			show = len(c.Signals) == 0
		}
		if !show {
			continue
		}
		position := report.EvidenceTotal
		report.EvidenceTotal++
		if position >= filter.Offset && len(report.Evidence) < filter.Limit {
			c.Paths = matchedPaths
			c.SurvivingPaths = surviving[c.SHA]
			report.Evidence = append(report.Evidence, c)
		}
	}
	for _, bucket := range aiBuckets(persons) {
		report.People = append(report.People, AIPerson{catalog.people[bucket.Key], bucket.AICounts})
	}
	report.Months = aiBuckets(months)
	sort.Slice(report.Months, func(i, j int) bool { return report.Months[i].Key < report.Months[j].Key })
	report.Domains = aiBuckets(domains)
	report.Tools = aiBuckets(tools)
	return report, nil
}
