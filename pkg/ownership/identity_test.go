package ownership

import (
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMatchingNamesUnifyOwnershipWithoutRewritingHistory(t *testing.T) {
	ctx := context.Background()
	repo := validationRepository(t)
	validationWrite(t, repo, "code.go", "work\n")
	first := validationCommit(t, repo, "Benjamin Wang", "benjamin@work.test", "2020-01-01T12:00:00Z", "2020-01-01T12:00:00Z", "Work contribution")
	validationWrite(t, repo, "code.go", "work\npersonal\n")
	validationCommit(t, repo, "Benjamin Wang", "benjamin@personal.test", "2020-01-02T12:00:00Z", "2020-01-02T12:00:00Z", "Personal contribution")
	validationWrite(t, repo, "code.go", "work\npersonal\nwork alias\n")
	validationCommit(t, repo, "BENJAMIN   WANG", "alias@work.test", "2020-01-03T12:00:00Z", "2020-01-03T12:00:00Z", "Work alias contribution")
	validationWrite(t, repo, "code.go", "work\npersonal\nwork alias\nother\n")
	head := validationCommit(t, repo, "Ben Johnson", "other@work.test", "2020-01-04T12:00:00Z", "2020-01-04T12:00:00Z", "Another person")
	config := filepath.Join(t.TempDir(), "affiliations.yaml")
	validationWrite(t, filepath.Dir(config), filepath.Base(config), "vendors:\n  Work:\n    domains: [work.test]\n")
	options := Options{ConfigPath: config, WindowDays: 30, Snapshots: 2}
	database := filepath.Join(t.TempDir(), "index.sqlite")
	index, err := OpenIndex(ctx, repo, options, database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { index.Close() })
	if err := index.Run(ctx); err != nil {
		t.Fatal(err)
	}
	var original []byte
	if err := index.db.QueryRow("SELECT snapshot FROM revisions WHERE fingerprint=? AND sha=?", index.fingerprint, head).Scan(&original); err != nil {
		t.Fatal(err)
	}
	check := func(data *Dataset) string {
		t.Helper()
		if len(data.People) != 2 {
			t.Fatalf("people=%+v, want two people, not four emails", data.People)
		}
		var person Person
		for _, candidate := range data.People {
			if candidate.IdentityBasis == "matching-name" {
				person = candidate
			}
		}
		if person.ID == "" || len(person.Emails) != 3 || len(person.AliasIDs) != 3 {
			t.Fatalf("missing grouped identity and aliases: %+v", person)
		}
		final := validationFile(t, &data.Snapshots[len(data.Snapshots)-1], "code.go")
		lines, changed, evidence := 0, 0, 0
		organizations := map[string]int{}
		for _, c := range final.Contributions {
			if c.PersonID != person.ID {
				continue
			}
			lines += c.Lines
			changed += c.ChangedLines
			organizations[c.OrganizationID] += c.Lines
			for _, e := range c.Evidence {
				if e.Kind == "ownership" {
					evidence++
				}
			}
		}
		if final.Lines != 4 || lines != 3 || changed != 3 || evidence != 3 || organizations["Work"] != 2 || organizations["unknown"] != 1 {
			t.Fatalf("grouped totals changed: file=%+v, person lines=%d activity=%d evidence=%d org=%v", final, lines, changed, evidence, organizations)
		}
		if len(data.Series) > 0 {
			point := data.Series[len(data.Series)-1]
			if point.Lines != 4 || len(point.Contributions) != 3 {
				t.Fatalf("series totals=%+v", point)
			}
			personLines, personChanges := 0, 0
			for _, c := range point.Contributions {
				if c.PersonID == person.ID {
					personLines += c.Lines
					personChanges += c.ChangedLines
				}
			}
			if personLines != 3 || personChanges != 3 {
				t.Fatalf("series person=%d/%d, want 3/3", personLines, personChanges)
			}
		}
		return person.ID
	}
	data, err := index.Query(ctx, "", "", "code.go")
	if err != nil {
		t.Fatal(err)
	}
	id := check(data)
	exact, err := index.Revision(ctx, head)
	if err != nil {
		t.Fatal(err)
	}
	if check(exact) != id {
		t.Fatal("inspector identity differs from timeline")
	}
	past, err := index.QueryHistory(ctx, "", "", "code.go", first)
	if err != nil {
		t.Fatal(err)
	}
	if validationFile(t, &past.Snapshots[0], "code.go").Contributions[0].PersonID != id {
		t.Fatal("person identity changes on an earlier history")
	}
	var after []byte
	if err := index.db.QueryRow("SELECT snapshot FROM revisions WHERE fingerprint=? AND sha=?", index.fingerprint, head).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, after) {
		t.Fatal("name grouping rewrote persisted attribution")
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
	index, err = OpenIndex(ctx, repo, options, database)
	if err != nil {
		t.Fatal(err)
	}
	if status := index.Status(); status.State != "ready" || status.Completed != 4 {
		t.Fatalf("grouping required reindex: %+v", status)
	}
	reopened, err := index.Query(ctx, "", "", "code.go")
	if err != nil {
		t.Fatal(err)
	}
	if check(reopened) != id {
		t.Fatal("person identity changes on restart")
	}
	export, err := Build(ctx, repo, options)
	if err != nil {
		t.Fatal(err)
	}
	if check(export) != id {
		t.Fatal("export identity differs from indexed identity")
	}
}

func TestMatchingNamesKeepUnknownPeopleSeparateAndDeduplicateEvidence(t *testing.T) {
	e := &engine{identities: map[string]Person{
		"a": {ID: "a", Name: "Alice", Email: "old@test", Emails: []string{"raw@test", "OLD@test"}},
		"b": {ID: "b", Name: " alice ", Email: "new@test"},
		"x": {ID: "x", Name: "", Email: "unknown1@test"},
		"y": {ID: "y", Name: "  ", Email: "unknown2@test"},
	}}
	evidence := Evidence{SHA: "sha", Path: "code.go", Kind: "ownership"}
	data := &Dataset{People: []Person{{ID: "a"}, {ID: "b"}, {ID: "x"}, {ID: "y"}}, Snapshots: []Snapshot{{Files: []File{{Path: "code.go", Lines: 3, Contributions: []Contribution{
		{PersonID: "a", OrganizationID: "unknown", Lines: 1, Evidence: []Evidence{evidence}},
		{PersonID: "b", OrganizationID: "unknown", Lines: 2, Evidence: []Evidence{evidence, {SHA: "sha", Path: "code.go", Kind: "activity"}}},
	}}}}}}
	e.groupPeople(data)
	if len(data.People) != 3 || !reflect.DeepEqual(data.People[0].Emails, []string{"new@test", "old@test", "raw@test"}) {
		t.Fatalf("unexpected identities: %+v", data.People)
	}
	contributions := data.Snapshots[0].Files[0].Contributions
	if len(contributions) != 1 || contributions[0].Lines != 3 || len(contributions[0].Evidence) != 2 {
		t.Fatalf("group lost or duplicated evidence: %+v", contributions)
	}
	if !reflect.DeepEqual(e.identities["a"].Emails, []string{"raw@test", "OLD@test"}) {
		t.Fatal("read-time grouping mutated frozen identity aliases")
	}
}
