package ownership

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceUsesExactRevisionAndFrozenAttribution(t *testing.T) {
	repo := validationRepository(t)
	validationWrite(t, repo, "config.properties", "original\ncontext\n<script>alert('code')</script>\n")
	first := validationCommit(t, repo, "Alice", "alice@old.test", "2020-01-01T12:00:00Z", "2020-01-01T12:00:00Z", "Initial source")
	validationWrite(t, repo, "config.properties", "original\nBob changed this\n<script>alert('code')</script>\n")
	second := validationCommit(t, repo, "Bob", "bob@new.test", "2020-01-02T12:00:00Z", "2020-01-02T12:00:00Z", "Change one line")
	validationWrite(t, repo, "config.properties", "original\nBob changed this\n<script>alert('code')</script>\nAlice after changing email")
	third := validationCommit(t, repo, "Alice", "alice@new.test", "2020-01-03T12:00:00Z", "2020-01-03T12:00:00Z", "New employer")
	renamed := "folder/colon:and\"quote.properties"
	validationGit(t, repo, nil, "mv", "config.properties", "renamed.properties")
	validationWrite(t, repo, renamed, "other\n")
	validationWrite(t, repo, "empty.txt", "")
	validationWrite(t, repo, "binary.bin", "binary\x00content")
	head := validationCommit(t, repo, "Bob", "bob@new.test", "2020-01-04T12:00:00Z", "2020-01-04T12:00:00Z", "Rename and add files")
	config := filepath.Join(t.TempDir(), "orgs.yaml")
	validationWrite(t, filepath.Dir(config), filepath.Base(config), "vendors:\n  Old:\n    domains: [old.test]\n  New:\n    domains: [new.test]\n")
	index, err := OpenIndex(context.Background(), repo, Options{ConfigPath: config, Workers: 2}, filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	if err := index.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Uncommitted content and identity changes must not leak into historical code.
	validationWrite(t, repo, "renamed.properties", "uncommitted\n")
	validationWrite(t, repo, ".mailmap", "Someone Else <different@test> <alice@old.test>\n")
	for _, sample := range []struct {
		sha, path string
		authors   []string
	}{
		{first, "config.properties", []string{first, first, first}},
		{second, "config.properties", []string{first, second, first}},
		{third, "config.properties", []string{first, second, first, third}},
		{head, "renamed.properties", []string{first, second, first, third}},
		{head, renamed, []string{head}},
		{head, "empty.txt", nil},
	} {
		file, err := index.Source(context.Background(), sample.sha, sample.path)
		if err != nil {
			t.Fatalf("%s:%s: %v", sample.sha, sample.path, err)
		}
		if len(file.Lines) != len(sample.authors) {
			t.Fatalf("line count: %+v", file)
		}
		if len(file.Lines) >= 3 && file.Lines[2] != "<script>alert('code')</script>" {
			t.Fatal("source text changed")
		}
		var aliceID string
		for _, block := range file.Blocks {
			for line := block.Start; line <= block.End; line++ {
				if block.SHA != sample.authors[line-1] {
					t.Fatalf("line %d attributed to wrong commit", line)
				}
			}
			if block.Author == "Alice" {
				if aliceID != "" && aliceID != block.PersonID {
					t.Fatal("same-name aliases were not grouped")
				}
				aliceID = block.PersonID
			}
			wantOrg := "New"
			if block.SHA == first {
				wantOrg = "Old"
			}
			if block.OrganizationID != wantOrg {
				t.Fatal("historical affiliation changed")
			}
			if sample.path == "renamed.properties" && block.Path != "config.properties" {
				t.Fatal("original attribution path was lost")
			}
		}
	}
	for _, sample := range [][2]string{{head, "config.properties"}, {first, "renamed.properties"}, {head, "binary.bin"}, {head, "../config.properties"}, {head, "/etc/passwd"}, {head, "folder"}, {"HEAD", "renamed.properties"}, {strings.Repeat("f", 40), "renamed.properties"}} {
		if _, err := index.Source(context.Background(), sample[0], sample[1]); err == nil {
			t.Fatalf("accepted unavailable source %v", sample)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := index.Source(ctx, head, "renamed.properties"); err == nil {
		t.Fatal("ignored cancellation")
	}
}
