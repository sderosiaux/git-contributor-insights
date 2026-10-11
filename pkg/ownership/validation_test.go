package ownership

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These fixtures deliberately distinguish the date a change was authored from
// the date it reached this branch. That distinction is observable to users.
func TestOwnershipHistoricalAffiliationsSurviveMailmapAndLateIntegration(t *testing.T) {
	repo := validationRepository(t)
	validationWrite(t, repo, "engine.go", "first\nsecond\n")
	first := validationCommit(t, repo, "Alice", "alice@old.test", "2020-01-10T12:00:00Z", "2020-01-10T12:00:00Z", "Start engine")
	validationWrite(t, repo, "engine.go", "first\nsecond\nthird\nfourth\n")
	validationCommit(t, repo, "Alice", "alice@new.test", "2020-02-10T12:00:00Z", "2020-02-10T12:00:00Z", "Extend engine after employer change")
	validationWrite(t, repo, "engine.go", "first\nsecond\nthird\nfourth\nfifth\nsixth\n")
	validationWrite(t, repo, ".mailmap", "Alice Canonical <alice@new.test> <alice@old.test>\nAlice Canonical <alice@new.test> <alice@new.test>\n")
	last := validationCommit(t, repo, "Alice", "alice@new.test", "2020-01-20T12:00:00Z", "2020-03-10T12:00:00Z", "Integrate older work")
	config := filepath.Join(t.TempDir(), "affiliations.yaml")
	validationWrite(t, filepath.Dir(config), filepath.Base(config), `vendors:
  New Company:
    domains: [new.test]
people:
  - email: alice@new.test
    affiliations:
      - organization: Old Company
        until: '2020-02-01'
      - organization: New Company
        from: '2020-02-01'
`)
	dataset, err := Build(context.Background(), repo, Options{Since: "2020-01-10T23:59:59Z", Until: "2020-03-31", Snapshots: 2, WindowDays: 30, ConfigPath: config})
	if err != nil {
		t.Fatal(err)
	}
	if len(dataset.Snapshots) != 2 {
		t.Fatalf("snapshots = %d, want two comparison endpoints", len(dataset.Snapshots))
	}
	if dataset.Snapshots[0].Revision != first || dataset.Snapshots[1].Revision != last {
		t.Fatalf("snapshot revisions must follow integration time: %s, %s", dataset.Snapshots[0].Revision, dataset.Snapshots[1].Revision)
	}
	initial := validationFile(t, &dataset.Snapshots[0], "engine.go")
	final := validationFile(t, &dataset.Snapshots[1], "engine.go")
	if initial.Lines != 2 || final.Lines != 6 {
		t.Fatalf("engine line counts = %d -> %d; want 2 -> 6", initial.Lines, final.Lines)
	}
	if got := validationOrganizationLines(final, "Old Company"); got != 4 {
		t.Fatalf("old employer has %d lines, want 4: late integration must use author date", got)
	}
	if got := validationOrganizationLines(final, "New Company"); got != 2 {
		t.Fatalf("new employer has %d lines, want 2", got)
	}
	if got := validationOrganizationLines(initial, "Old Company"); got != 2 {
		t.Fatalf("initial employer has %d lines, want 2", got)
	}
	ids := map[string]bool{}
	for _, snapshot := range dataset.Snapshots {
		for _, file := range snapshot.Files {
			for _, contribution := range file.Contributions {
				ids[contribution.PersonID] = true
			}
		}
	}
	if len(ids) != 1 {
		t.Fatalf("mailmap aliases yielded %d identities, want one: %v", len(ids), ids)
	}
	for _, person := range dataset.People {
		if ids[person.ID] && person.Email != "alice@new.test" {
			t.Errorf("canonical email = %q", person.Email)
		}
	}
	var oldActivity, newActivity int
	for _, contribution := range final.Contributions {
		if contribution.OrganizationID == "Old Company" {
			oldActivity += contribution.ChangedLines
		} else if contribution.OrganizationID == "New Company" {
			newActivity += contribution.ChangedLines
		}
	}
	if oldActivity != 2 || newActivity != 0 {
		t.Fatalf("30-day activity old/new = %d/%d, want 2/0: activity follows integration time", oldActivity, newActivity)
	}
	validationConservation(t, dataset.Snapshots)
}

func TestOwnershipRawDomainDoesNotFollowMailmapEmployer(t *testing.T) {
	repo := validationRepository(t)
	validationWrite(t, repo, "code.go", "old line\n")
	validationCommit(t, repo, "Alice Old", "alice@old.test", "2020-01-10T12:00:00Z", "2020-01-10T12:00:00Z", "Old employer contribution")
	validationWrite(t, repo, "code.go", "old line\nnew line\n")
	validationWrite(t, repo, ".mailmap", "Alice <alice@new.test> <alice@old.test>\n")
	validationCommit(t, repo, "Alice", "alice@new.test", "2020-02-10T12:00:00Z", "2020-02-10T12:00:00Z", "New employer contribution")
	config := filepath.Join(t.TempDir(), "affiliations.yaml")
	validationWrite(t, filepath.Dir(config), filepath.Base(config), "vendors:\n  Old Company:\n    domains: [old.test]\n  New Company:\n    domains: [new.test]\n")
	dataset, err := Build(context.Background(), repo, Options{Since: "2020-02-10", Until: "2020-02-10", Snapshots: 2, ConfigPath: config})
	if err != nil {
		t.Fatal(err)
	}
	final := validationFile(t, &dataset.Snapshots[len(dataset.Snapshots)-1], "code.go")
	if validationOrganizationLines(final, "Old Company") != 1 || validationOrganizationLines(final, "New Company") != 1 {
		t.Fatalf("mailmap must unify the person without rewriting historical domains: %+v", final.Contributions)
	}
}

func TestOwnershipChangesPreserveSurvivingLinesAndExplainExclusions(t *testing.T) {
	repo := validationRepository(t)
	validationWrite(t, repo, "src/parser.go", "alpha\nbeta\ngamma\ndelta\n")
	validationWrite(t, repo, "src/removed.go", "obsolete one\nobsolete two\n")
	validationWrite(t, repo, "vendor/dependency.go", "external dependency\n")
	validationWrite(t, repo, "generated.go", "// Code generated by validation. DO NOT EDIT.\npackage generated\n")
	validationWrite(t, repo, "picture.bin", "\x00\x01\x02")
	validationCommit(t, repo, "Alice", "alice@gmail.com", "2020-01-10T12:00:00Z", "2020-01-10T12:00:00Z", "Create parser")
	validationWrite(t, repo, "src/parser.go", "alpha\nreplacement\ngamma\ndelta\nepsilon\nzeta\n")
	if err := os.Remove(filepath.Join(repo, "src/removed.go")); err != nil {
		t.Fatal(err)
	}
	validationCommit(t, repo, "Bob", "bob@unmapped.test", "2020-02-10T12:00:00Z", "2020-02-10T12:00:00Z", "Extend parser and delete obsolete file")
	if err := os.MkdirAll(filepath.Join(repo, "core"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(repo, "src/parser.go"), filepath.Join(repo, "core/parser.go")); err != nil {
		t.Fatal(err)
	}
	validationCommit(t, repo, "Bob", "bob@unmapped.test", "2020-03-10T12:00:00Z", "2020-03-10T12:00:00Z", "Move parser without changing it")
	dataset, err := Build(context.Background(), repo, Options{Since: "2020-01-10T23:59:59Z", Until: "2020-03-31", Snapshots: 2, WindowDays: 60})
	if err != nil {
		t.Fatal(err)
	}
	if len(dataset.Snapshots) != 2 {
		t.Fatalf("snapshot count = %d", len(dataset.Snapshots))
	}
	final := &dataset.Snapshots[1]
	file := validationFile(t, final, "core/parser.go")
	if file.Lines != 6 {
		t.Fatalf("renamed parser has %d lines, want 6", file.Lines)
	}
	byPerson := map[string]int{}
	for _, contribution := range file.Contributions {
		byPerson[contribution.PersonID] += contribution.Lines
		if contribution.OrganizationID != "unknown" {
			t.Errorf("unmapped or personal email inferred as %q, want unknown", contribution.OrganizationID)
		}
	}
	for _, person := range dataset.People {
		if person.Name == "Alice" || person.Name == "Bob" {
			if byPerson[person.ID] != 3 {
				t.Errorf("%s surviving lines = %d, want 3 despite rename", person.Name, byPerson[person.ID])
			}
		}
	}
	var changed int
	for _, f := range final.Files {
		if f.Path == "src/removed.go" && f.Lines != 0 {
			t.Errorf("deleted file still has %d surviving lines", f.Lines)
		}
		if strings.HasPrefix(f.Path, "vendor/") || f.Path == "generated.go" || f.Path == "picture.bin" {
			t.Errorf("excluded path present: %s", f.Path)
		}
		for _, contribution := range f.Contributions {
			changed += contribution.ChangedLines
		}
	}
	if changed != 6 {
		t.Errorf("recent changed lines = %d, want 6 (3 additions, 1 replacement deletion, 2 removed lines); pure rename is zero", changed)
	}
	if final.ExcludedFiles < 3 {
		t.Errorf("excluded files = %d, want at least vendor + generated + binary", final.ExcludedFiles)
	}
	if len(final.Exclusions) == 0 {
		t.Error("exclusions must explain the omitted denominator")
	}
	validationConservation(t, dataset.Snapshots)
}

func TestOwnershipRejectsInvalidDateRanges(t *testing.T) {
	repo := validationRepository(t)
	validationWrite(t, repo, "main.go", "package main\n")
	validationCommit(t, repo, "Alice", "alice@example.test", "2020-01-10T12:00:00Z", "2020-01-10T12:00:00Z", "Initial")
	for _, options := range []Options{{Since: "yesterday-ish"}, {Until: "2020-13-01"}, {Since: "2021-01-01", Until: "2020-01-01"}} {
		t.Run(options.Since+"_"+options.Until, func(t *testing.T) {
			if _, err := Build(context.Background(), repo, options); err == nil {
				t.Errorf("accepted invalid interval: %+v", options)
			}
		})
	}
}

func TestOwnershipSnapshotsDoNotLeakUnmergedBranchWork(t *testing.T) {
	repo := validationRepository(t)
	validationWrite(t, repo, "main.go", "package main\n")
	validationCommit(t, repo, "Alice", "alice@example.test", "2020-01-10T12:00:00Z", "2020-01-10T12:00:00Z", "Initial branch state")
	validationGit(t, repo, nil, "checkout", "-q", "-b", "feature")
	validationWrite(t, repo, "feature.go", "feature one\nfeature two\n")
	validationCommit(t, repo, "Carol", "carol@example.test", "2020-01-12T12:00:00Z", "2020-01-12T12:00:00Z", "Implement feature before its merge")
	validationGit(t, repo, nil, "checkout", "-q", "main")
	validationGit(t, repo, []string{"GIT_AUTHOR_NAME=Integrator", "GIT_AUTHOR_EMAIL=integrator@example.test", "GIT_AUTHOR_DATE=2020-03-10T12:00:00Z", "GIT_COMMITTER_NAME=Integrator", "GIT_COMMITTER_EMAIL=integrator@example.test", "GIT_COMMITTER_DATE=2020-03-10T12:00:00Z"}, "merge", "--no-ff", "-q", "feature", "-m", "Merge old feature")
	dataset, err := Build(context.Background(), repo, Options{Since: "2020-01-15", Until: "2020-03-31", Snapshots: 2, WindowDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	if len(dataset.Snapshots) != 2 {
		t.Fatalf("snapshot count = %d", len(dataset.Snapshots))
	}
	for _, file := range dataset.Snapshots[0].Files {
		if file.Path == "feature.go" {
			t.Error("unmerged branch leaked into historical main-branch snapshot")
		}
	}
	feature := validationFile(t, &dataset.Snapshots[1], "feature.go")
	if feature.Lines != 2 {
		t.Fatalf("merged feature lines = %d, want 2", feature.Lines)
	}
	var changed int
	for _, contribution := range feature.Contributions {
		changed += contribution.ChangedLines
	}
	if changed != 0 {
		t.Errorf("old commits must not become recent activity because of a merge: changed=%d", changed)
	}
	for _, person := range dataset.People {
		if person.Name == "Integrator" {
			for _, contribution := range feature.Contributions {
				if contribution.PersonID == person.ID && contribution.Lines > 0 {
					t.Error("merge-only integrator was given source attribution")
				}
			}
		}
	}
	validationConservation(t, dataset.Snapshots)
}

func TestOwnershipEmptyRepositoryAndBeforeHistory(t *testing.T) {
	repo := validationRepository(t)
	if _, err := Build(context.Background(), repo, Options{}); err == nil {
		t.Error("empty Git repository must produce a useful error")
	}
	validationWrite(t, repo, "main.go", "package main\n")
	validationCommit(t, repo, "Alice", "alice@example.test", "2020-01-10T12:00:00Z", "2020-01-10T12:00:00Z", "Initial")
	dataset, err := Build(context.Background(), repo, Options{Since: "2019-01-01", Until: "2019-12-31", Snapshots: 2})
	if err != nil {
		return
	} // A clear no-history error is also an honest representation.
	for _, snapshot := range dataset.Snapshots {
		if len(snapshot.Files) != 0 || snapshot.Revision != "" {
			t.Errorf("interval before first commit returned a future revision: %+v", snapshot)
		}
	}
}

func validationRepository(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git executable is required")
	}
	dir := t.TempDir()
	validationGit(t, dir, nil, "init", "-q", "-b", "main")
	return dir
}

func validationWrite(t *testing.T, repo, path, value string) {
	t.Helper()
	filename := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(value), 0644); err != nil {
		t.Fatal(err)
	}
}

func validationCommit(t *testing.T, repo, name, email, authorDate, commitDate, subject string) string {
	t.Helper()
	validationGit(t, repo, nil, "add", "-A")
	validationGit(t, repo, []string{"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email, "GIT_AUTHOR_DATE=" + authorDate, "GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + email, "GIT_COMMITTER_DATE=" + commitDate}, "commit", "-q", "-m", subject)
	return strings.TrimSpace(validationGit(t, repo, nil, "rev-parse", "HEAD"))
}

func validationGit(t *testing.T, repo string, environment []string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repo, "-c", "commit.gpgsign=false", "-c", "core.autocrlf=false"}, args...)...)
	command.Env = append(os.Environ(), append([]string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}, environment...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func validationFile(t *testing.T, snapshot *Snapshot, path string) *File {
	t.Helper()
	for i := range snapshot.Files {
		if snapshot.Files[i].Path == path {
			return &snapshot.Files[i]
		}
	}
	t.Fatalf("snapshot %s is missing %s", snapshot.Date, path)
	return nil
}

func validationOrganizationLines(file *File, organization string) int {
	var lines int
	for _, contribution := range file.Contributions {
		if contribution.OrganizationID == organization {
			lines += contribution.Lines
		}
	}
	return lines
}

func validationConservation(t *testing.T, snapshots []Snapshot) {
	t.Helper()
	for _, snapshot := range snapshots {
		for _, file := range snapshot.Files {
			var attributed int
			for _, contribution := range file.Contributions {
				if contribution.Lines < 0 || contribution.ChangedLines < 0 {
					t.Errorf("negative contribution for %s", file.Path)
				}
				attributed += contribution.Lines
				if contribution.Lines > 0 && len(contribution.Evidence) == 0 {
					t.Errorf("attribution lacks inspectable evidence for %s", file.Path)
				}
			}
			if attributed != file.Lines {
				t.Errorf("%s at %s: attributed %d lines != denominator %d", file.Path, snapshot.Date, attributed, file.Lines)
			}
		}
	}
}
