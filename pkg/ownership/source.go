package ownership

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SourceFile is the text and surviving-line attribution at one immutable SHA.
// Read on demand; opening a file never rebuilds or changes the ownership index.
type SourceFile struct {
	Revision string        `json:"revision"`
	Path     string        `json:"path"`
	Lines    []string      `json:"lines"`
	Blocks   []SourceBlock `json:"blocks"`
}

type SourceBlock struct {
	AI             []AISignal `json:"ai,omitempty"`
	Start          int        `json:"start"`
	End            int        `json:"end"`
	PersonID       string     `json:"personId"`
	OrganizationID string     `json:"organizationId"`
	Author         string     `json:"author"`
	SHA            string     `json:"sha"`
	Path           string     `json:"path"`
	Date           string     `json:"date"`
	Summary        string     `json:"summary"`
}

func (index *Index) Source(ctx context.Context, sha, filename string) (*SourceFile, error) {
	if err := index.queryReady(); err != nil {
		return nil, err
	}
	if !fs.ValidPath(filename) || filename == "." || strings.ContainsRune(filename, 0) {
		return nil, fmt.Errorf("choose an included repository file")
	}
	if _, exists := index.session.engine.commits[sha]; !exists {
		return nil, fmt.Errorf("revision is not reachable from the indexed HEAD")
	}
	state, err := index.readRevisionState(ctx, sha)
	if err != nil {
		return nil, err
	}
	file, included := state.files[filename]
	if !included {
		return nil, fmt.Errorf("file is absent or excluded at this revision")
	}
	// A request-local engine gives Git the request's cancellation context without
	// mutating the shared session or its frozen identity/affiliation metadata.
	base := index.session.engine
	e := &engine{ctx: ctx, repo: base.repo, gitSlots: base.gitSlots}
	content, err := e.git("cat-file", "blob", sha+":"+filename)
	if err != nil {
		return nil, err
	}
	if len(content) > maxBlobBytes || bytes.IndexByte(content, 0) >= 0 {
		return nil, fmt.Errorf("file is not an included text file")
	}
	result := &SourceFile{Revision: sha, Path: filename, Lines: []string{}, Blocks: []SourceBlock{}}
	if len(content) > 0 {
		result.Lines = strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	}
	output, err := e.git("-c", "blame.ignoreRevsFile=", "blame", "--no-textconv", "--incremental", sha, "--", filename)
	if err != nil {
		return nil, err
	}
	resolved, people := base.personGroups()
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 4096), maxBlobBytes+65536)
	var block SourceBlock
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) == 4 {
			if commit, exists := base.commits[fields[0]]; exists {
				start, startErr := strconv.Atoi(fields[2])
				count, countErr := strconv.Atoi(fields[3])
				if block.SHA != "" || startErr != nil || countErr != nil || start < 1 || count < 1 || start+count-1 > len(result.Lines) {
					return nil, fmt.Errorf("invalid source attribution block")
				}
				person := resolved[personID(commit)]
				block = SourceBlock{Start: start, End: start + count - 1, SHA: commit.sha, PersonID: person, OrganizationID: base.config.organization(commit), Author: people[person].Name, Date: commit.authored.Format(time.RFC3339), Summary: commit.summary}
				block.AI = commit.ai
				continue
			}
		}
		if strings.HasPrefix(line, "filename ") {
			if block.SHA == "" {
				return nil, fmt.Errorf("missing source attribution metadata")
			}
			block.Path = strings.TrimPrefix(line, "filename ")
			if strings.HasPrefix(block.Path, "\"") {
				block.Path, err = strconv.Unquote(block.Path)
				if err != nil {
					return nil, err
				}
			}
			result.Blocks = append(result.Blocks, block)
			block = SourceBlock{}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Slice(result.Blocks, func(i, j int) bool { return result.Blocks[i].Start < result.Blocks[j].Start })
	// Verify every line, and the attribution totals, against the saved snapshot.
	// Never show a plausible highlight that disagrees with the ownership bars.
	counts, expected := map[string]int{}, map[string]int{}
	next := 1
	for _, block := range result.Blocks {
		if block.Start != next {
			return nil, fmt.Errorf("incomplete source attribution")
		}
		next = block.End + 1
		counts[block.PersonID+"\x00"+block.OrganizationID] += block.End - block.Start + 1
	}
	for _, contribution := range file.Contributions {
		if contribution.Lines > 0 {
			expected[resolved[contribution.PersonID]+"\x00"+contribution.OrganizationID] += contribution.Lines
		}
	}
	if block.SHA != "" || next != len(result.Lines)+1 || len(result.Lines) != file.Lines || len(counts) != len(expected) {
		return nil, fmt.Errorf("source attribution differs from the indexed snapshot")
	}
	for key, value := range counts {
		if expected[key] != value {
			return nil, fmt.Errorf("source attribution differs from the indexed snapshot")
		}
	}
	return result, nil
}
