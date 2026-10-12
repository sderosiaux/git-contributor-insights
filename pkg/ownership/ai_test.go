package ownership

import (
	"context"
	"path/filepath"
	"testing"
)

func TestAIProvenanceRequiresExplicitEvidence(t *testing.T) {
	for _, test := range []struct {
		name, email, body string
		count             int
	}{
		{"Alice", "alice@example.test", "Fix Claude integration and Cursor pagination", 0},
		{"Alice", "alice@example.test", "Co-authored-by: Claude Dubois <claude@example.test>", 0},
		{"Alice", "alice@example.test", "Co-authored-by: Claude <noreply@anthropic.com>\nCo-Authored-By: Claude <noreply@anthropic.com>", 1},
		{"Alice", "alice@example.test", "Generated with [Claude Code](https://claude.ai/code)", 1},
		{"Alice", "alice@example.test", "Assisted-by: Cursor\nAI-Assisted-By: Codex", 2},
		{"Copilot", "123+copilot@users.noreply.github.com", "Fix an issue", 1},
		{"Alice", "alice@example.test", "Discuss whether this was generated with Claude Code", 0},
	} {
		if got := detectAISignals(test.name, test.email, test.body); len(got) != test.count {
			t.Errorf("%q: %+v", test.body, got)
		}
	}
}

func TestAIReportHistoricalSurvivalAliasesScopeAndResume(t *testing.T) {
	repo := validationRepository(t)
	validationWrite(t, repo, "src/main.go", "original\nsurvives\n")
	first := validationCommit(t, repo, "Alice", "alice@one.test", "2020-01-01T12:00:00Z", "2020-01-01T12:00:00Z", "Initial\n\nCo-authored-by: Claude <noreply@anthropic.com>\nCo-authored-by: Claude <noreply@anthropic.com>")
	validationWrite(t, repo, "src/main.go", "changed\nsurvives\n")
	validationCommit(t, repo, "Alice", "alice@two.test", "2020-01-02T12:00:00Z", "2020-01-02T12:00:00Z", "Mention Claude without claiming assistance")
	validationWrite(t, repo, "src/main.go", "changed\nreplacement\n")
	validationWrite(t, repo, "docs/readme.md", "documentation\n")
	last := validationCommit(t, repo, "Bob", "bob@example.test", "2020-02-03T12:00:00Z", "2020-02-03T12:00:00Z", "Update\n\nAssisted-by: Cursor")
	db := filepath.Join(t.TempDir(), "index.sqlite")
	index, err := OpenIndex(context.Background(), repo, Options{}, db)
	if err != nil {
		t.Fatal(err)
	}
	if err = index.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	query := func(filter AIFilter) *AIReport {
		t.Helper()
		r, err := index.AI(context.Background(), filter)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := query(AIFilter{})
	if r.Commits != 3 || r.Declared != 2 || r.Represented != 1 || len(r.People) != 2 || len(r.Months) != 2 || r.Revision != last {
		t.Fatalf("report=%+v", r)
	}
	if r.EvidenceTotal != 2 || len(r.Evidence) != 2 || len(r.Evidence[1].Signals) != 1 {
		t.Fatalf("duplicate evidence: %+v", r.Evidence)
	}
	r = query(AIFilter{To: "2020-01-02", Person: "alice@two.test"})
	if r.Commits != 2 || r.Declared != 1 || r.Represented != 1 || len(r.People) != 1 || len(r.Evidence[0].SurvivingPaths) != 1 {
		t.Fatalf("historical/alias report=%+v", r)
	}
	r = query(AIFilter{Path: "docs"})
	if r.Commits != 1 || r.Declared != 1 || r.Represented != 1 {
		t.Fatalf("scope report=%+v", r)
	}
	r = query(AIFilter{Tool: "Claude"})
	if r.Commits != 3 || r.Declared != 1 || r.Represented != 0 {
		t.Fatalf("tool denominator=%+v", r)
	}
	r = query(AIFilter{Evidence: "unidentified"})
	if r.EvidenceTotal != 1 || len(r.Evidence[0].Signals) != 0 {
		t.Fatal("unmarked commit was omitted")
	}
	r = query(AIFilter{Evidence: "all", Offset: 1, Limit: 1})
	if r.EvidenceTotal != 3 || len(r.Evidence) != 1 || r.Offset != 1 {
		t.Fatal("pagination lost commits")
	}
	for _, filter := range []AIFilter{{From: "2020-04-01", To: "2020-01-01"}, {Path: "../secret"}, {Person: "nobody"}, {Tool: "fake"}, {Offset: -1}, {From: "bad"}, {Evidence: "human"}} {
		if _, err := index.AI(context.Background(), filter); err == nil {
			t.Fatalf("invalid filter accepted: %+v", filter)
		}
	}
	source, err := index.Source(context.Background(), first, "src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(source.Blocks) != 1 || len(source.Blocks[0].AI) != 1 {
		t.Fatal("exact source lost AI provenance")
	}
	commit, err := index.commitRecord(context.Background(), first)
	if err != nil || len(commit.AI) != 1 {
		t.Fatal("commit inspector lost AI provenance")
	}
	if err = index.Close(); err != nil {
		t.Fatal(err)
	}
	index, err = OpenIndex(context.Background(), repo, Options{}, db)
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	if index.Status().State != "ready" {
		t.Fatal("AI extension rebuilt an existing index")
	}
	if r = query(AIFilter{}); r.Declared != 2 || r.Represented != 1 {
		t.Fatal("resume changed provenance")
	}
}
