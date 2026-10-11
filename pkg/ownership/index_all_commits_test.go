package ownership

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestAllReachableOwnershipMigrationBranchHistoriesAndCatalogue(t *testing.T) {
	ctx := context.Background()
	repo := validationRepository(t)
	validationWrite(t, repo, "root.go", "root\n")
	root := validationCommit(t, repo, "Root", "root@example.test", "2020-01-01T12:00:00Z", "2020-01-01T12:00:00Z", "Shared root")
	validationWrite(t, repo, "main.go", "main only\n")
	main := validationCommit(t, repo, "Main", "main@example.test", "2020-01-02T12:00:00Z", "2020-01-02T12:00:00Z", "Main advance")
	validationGit(t, repo, nil, "checkout", "-q", "-b", "side", root)
	validationWrite(t, repo, "ephemeral.go", "first\nsecond\n")
	added := validationCommit(t, repo, "Side One", "one@example.test", "2020-01-03T12:00:00Z", "2020-01-03T12:00:00Z", "Ephemeral add")
	validationWrite(t, repo, "ephemeral.go", "replacement\nsecond\n")
	edited := validationCommit(t, repo, "Side Two", "two@example.test", "2020-01-03T12:00:00Z", "2020-01-03T12:00:00Z", "Ephemeral edit same timestamp")
	validationWrite(t, repo, "ephemeral.go", "first\nsecond\n")
	reverted := validationCommit(t, repo, "Side Three", "three@example.test", "2020-01-04T12:00:00Z", "2020-01-04T12:00:00Z", "Ephemeral revert same blob")
	validationGit(t, repo, nil, "rm", "-q", "ephemeral.go")
	deleted := validationCommit(t, repo, "Side Four", "four@example.test", "2020-01-05T12:00:00Z", "2020-01-05T12:00:00Z", "Ephemeral delete before merge")
	validationGit(t, repo, nil, "checkout", "-q", "main")
	validationGit(t, repo, []string{"GIT_AUTHOR_NAME=Merge", "GIT_AUTHOR_EMAIL=merge@example.test", "GIT_AUTHOR_DATE=2020-01-06T12:00:00Z", "GIT_COMMITTER_NAME=Merge", "GIT_COMMITTER_EMAIL=merge@example.test", "GIT_COMMITTER_DATE=2020-01-06T12:00:00Z"}, "merge", "--no-ff", "-q", "side", "-m", "Merge branch after ephemeral work disappeared")
	head := strings.TrimSpace(validationGit(t, repo, nil, "rev-parse", "HEAD"))
	validationGit(t, repo, nil, "checkout", "-q", "-b", "unmerged")
	validationWrite(t, repo, "unmerged.go", "outside indexed HEAD\n")
	unmerged := validationCommit(t, repo, "Outside", "outside@example.test", "2020-01-07T12:00:00Z", "2020-01-07T12:00:00Z", "Unmerged work")
	validationGit(t, repo, nil, "checkout", "-q", "main")
	dbPath := filepath.Join(t.TempDir(), "index.sqlite")
	index, err := OpenIndex(ctx, repo, Options{}, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.persistMetadata(ctx); err != nil {
		t.Fatal(err)
	}
	// Emulate the previous schema's completed first-parent-only index.
	oldBlobs := map[string][]byte{}
	for _, revision := range index.revisions {
		snapshot, err := index.session.engine.snapshotWithActivity(cutoff{date: revision.date, sha: revision.sha}, 90, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := index.persistRevision(ctx, snapshot, revision.date); err != nil {
			t.Fatal(err)
		}
		var blob []byte
		if err := index.db.QueryRow("SELECT snapshot FROM revisions WHERE fingerprint=? AND sha=?", index.fingerprint, revision.sha).Scan(&blob); err != nil {
			t.Fatal(err)
		}
		oldBlobs[revision.sha] = blob
	}
	// Migration must not depend on newly introduced catalogue statistics.
	if _, err := index.db.Exec("DELETE FROM revision_stats"); err != nil {
		t.Fatal(err)
	}
	fingerprint := index.fingerprint
	index.Close()
	index, err = OpenIndex(ctx, repo, Options{}, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	if status := index.Status(); status.Completed != 3 || status.Total != 7 || status.Commits != 7 || index.fingerprint != fingerprint {
		t.Fatalf("migration should reuse 3 of 7 exact ownership states: %+v", status)
	}
	if err := index.Run(ctx); err != nil {
		t.Fatal(err)
	}
	for sha, expected := range oldBlobs {
		var actual []byte
		if err := index.db.QueryRow("SELECT snapshot FROM revisions WHERE fingerprint=? AND sha=?", fingerprint, sha).Scan(&actual); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(actual, expected) {
			t.Fatalf("migration recomputed old snapshot %s", sha)
		}
	}
	for _, sha := range []string{added, edited, reverted} {
		dataset, err := index.Revision(ctx, sha)
		if err != nil {
			t.Fatal(err)
		}
		if dataset.Commit == nil || dataset.Commit.SHA != sha || dataset.Commit.Mainline {
			t.Fatalf("missing branch metadata: %+v", dataset.Commit)
		}
		file := validationFile(t, &dataset.Snapshots[0], "ephemeral.go")
		actual := map[string]int{}
		for _, contribution := range file.Contributions {
			if contribution.Lines == 0 {
				continue
			}
			actual[index.session.engine.identities[contribution.PersonID].Email] += contribution.Lines
		}
		want := map[string]int{}
		for _, line := range strings.Split(validationGit(t, repo, nil, "blame", "--no-textconv", "--line-porcelain", sha, "--", "ephemeral.go"), "\n") {
			if strings.HasPrefix(line, "author-mail ") {
				email := strings.Trim(strings.TrimPrefix(line, "author-mail "), "<>")
				want[email]++
			}
		}
		if len(actual) != len(want) {
			t.Fatalf("branch blame %s=%v, native=%v", sha, actual, want)
		}
		for email, lines := range want {
			if actual[email] != lines {
				t.Fatalf("branch cache %s %s=%d, native=%d", sha, email, actual[email], lines)
			}
		}
		for _, f := range dataset.Snapshots[0].Files {
			if f.Path == "main.go" {
				t.Fatal("mainline-only work leaked into branch state/activity")
			}
		}
	}
	branch, err := index.QueryHistory(ctx, "", "", "", reverted)
	if err != nil {
		t.Fatal(err)
	}
	if branch.Timeline.Tip != reverted || branch.Timeline.Revisions != 4 || branch.Timeline.TotalIndex != 7 {
		t.Fatalf("branch timeline=%+v", branch.Timeline)
	}
	branchSHAs := []string{}
	for _, point := range branch.Series {
		if point.Kind == "revision" {
			branchSHAs = append(branchSHAs, point.Revision)
		}
	}
	if strings.Join(branchSHAs, ",") != strings.Join([]string{root, added, edited, reverted}, ",") {
		t.Fatalf("interleaved branch timeline: %v", branchSHAs)
	}
	mainline, err := index.Query(ctx, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if mainline.Timeline.Tip != head || mainline.Timeline.Revisions != 3 {
		t.Fatalf("HEAD timeline changed: %+v", mainline.Timeline)
	}
	for _, file := range mainline.Snapshots[1].Files {
		if file.Path == "ephemeral.go" && file.Lines != 0 {
			t.Fatal("deleted branch work remains at HEAD")
		}
	}
	seen := map[string]bool{}
	for offset := 0; offset < 7; offset += 2 {
		page, err := index.Commits(ctx, CommitFilter{Offset: offset, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != 7 || page.HasMore != (offset+2 < 7) {
			t.Fatalf("pagination=%+v", page)
		}
		for _, commit := range page.Commits {
			if seen[commit.SHA] {
				t.Fatal("pagination duplicate")
			}
			seen[commit.SHA] = true
			if commit.SHA == added && commit.Lines != 3 {
				t.Fatalf("branch stock lines=%d", commit.Lines)
			}
			if commit.SHA == main && commit.Files != 2 {
				t.Fatalf("legacy lazy file stats=%d", commit.Files)
			}
		}
	}
	for _, sha := range []string{root, main, added, edited, reverted, deleted, head} {
		if !seen[sha] {
			t.Fatalf("catalogue omitted %s", sha)
		}
	}
	if seen[unmerged] {
		t.Fatal("unmerged ref escaped HEAD scope")
	}
	filtered, err := index.Commits(ctx, CommitFilter{Query: "Side Two", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Total != 1 || filtered.Commits[0].SHA != edited {
		t.Fatalf("author filtering=%+v", filtered)
	}
	if _, err := index.Revision(ctx, unmerged); err == nil {
		t.Fatal("unreachable exact revision accepted")
	}
	if _, err := index.QueryHistory(ctx, "", "", "", unmerged); err == nil {
		t.Fatal("unreachable tip accepted")
	}
}
