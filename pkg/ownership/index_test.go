package ownership

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestIndexKeepsEveryRevisionSameTimestampsAndCompleteEvidence(t *testing.T) {
	repo := validationRepository(t)
	content := ""
	shas := []string{}
	for n := 0; n < 6; n++ {
		content += fmt.Sprintf("line%d\n", n)
		validationWrite(t, repo, "src/main.go", content)
		validationWrite(t, repo, " spaced.go ", "space\n")
		day := max(1, n)
		date := fmt.Sprintf("2020-01-%02dT12:00:00Z", day)
		shas = append(shas, validationCommit(t, repo, "Alice", "alice@example.test", date, date, fmt.Sprintf("Change %d", n)))
	}
	index, err := OpenIndex(context.Background(), repo, Options{WindowDays: 2, Snapshots: 1}, filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	if err := index.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := index.Status(); status.Completed != 6 || status.Total != 6 || status.Commits != 6 || status.State != "ready" {
		t.Fatalf("status=%+v", status)
	}
	dataset, err := index.Query(context.Background(), "2020-01-01", "2020-01-10", "src")
	if err != nil {
		t.Fatal(err)
	}
	if len(dataset.Snapshots) != 2 || !dataset.Timeline.Complete || dataset.Timeline.Revisions != 6 {
		t.Fatalf("timeline=%+v", dataset.Timeline)
	}
	revisionPoints := []string{}
	hasExpiry := false
	for _, point := range dataset.Series {
		if point.Kind == "revision" {
			revisionPoints = append(revisionPoints, point.Revision)
		}
		if point.Kind == "expiry" {
			hasExpiry = true
		}
	}
	if strings.Join(revisionPoints, ",") != strings.Join(shas, ",") {
		t.Fatalf("revision series lost/reordered a SHA: %v", revisionPoints)
	}
	if !hasExpiry {
		t.Fatal("activity expiry absent")
	}
	final := validationFile(t, &dataset.Snapshots[1], "src/main.go")
	if final.Lines != 6 {
		t.Fatalf("lines=%d", final.Lines)
	}
	ownershipEvidence := 0
	for _, contribution := range final.Contributions {
		if contribution.ChangedLines != 0 {
			t.Fatal("expired activity retained")
		}
		for _, evidence := range contribution.Evidence {
			if evidence.Kind == "ownership" {
				ownershipEvidence++
			}
		}
	}
	if ownershipEvidence != 6 {
		t.Fatalf("evidence capped: %d", ownershipEvidence)
	}
	exact, err := index.Revision(context.Background(), shas[0])
	if err != nil {
		t.Fatal(err)
	}
	if validationFile(t, &exact.Snapshots[0], "src/main.go").Lines != 1 {
		t.Fatal("exact SHA replaced by last same-time revision")
	}
	spaced, err := index.Query(context.Background(), "2020-01-01", "2020-01-10", " spaced.go ")
	if err != nil {
		t.Fatal(err)
	}
	if spaced.Series[len(spaced.Series)-1].Lines != 1 {
		t.Fatal("scope path spaces were altered")
	}
}

func TestIndexPersistsCheckpointsResumesAndAddsNewCommits(t *testing.T) {
	repo := validationRepository(t)
	content := ""
	for n := 1; n <= 3; n++ {
		content += fmt.Sprintf("line%d\n", n)
		validationWrite(t, repo, "main.go", content)
		date := fmt.Sprintf("2020-01-%02dT12:00:00Z", n)
		validationCommit(t, repo, "Alice", "alice@example.test", date, date, "Change")
	}
	dbPath := filepath.Join(t.TempDir(), "index.sqlite")
	index, err := OpenIndex(context.Background(), repo, Options{}, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.persistMetadata(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := index.revisions[0]
	snapshot, err := index.session.engine.snapshotWithActivity(cutoff{date: first.date, sha: first.sha}, 90, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.persistRevision(context.Background(), snapshot, first.date); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := index.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
	index, err = OpenIndex(context.Background(), repo, Options{}, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if index.Status().Completed != 1 {
		t.Fatalf("checkpoint not reused: %+v", index.Status())
	}
	if err := index.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	fingerprint := index.fingerprint
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
	validationWrite(t, repo, "main.go", content+"new fourth\n")
	validationCommit(t, repo, "Bob", "bob@example.test", "2020-02-01T12:00:00Z", "2020-02-01T12:00:00Z", "Append after restart")
	index, err = OpenIndex(context.Background(), repo, Options{}, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	if index.fingerprint != fingerprint || index.Status().Completed != 3 || index.Status().Total != 4 {
		t.Fatalf("incremental reuse=%+v", index.Status())
	}
	if err := index.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := index.db.QueryRow("SELECT COUNT(*) FROM revisions WHERE fingerprint=?", fingerprint).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 4 {
		t.Fatalf("duplicate or missing revision rows: %d", rows)
	}
}

func TestIndexFingerprintInvalidatesAffiliationsAndMailmap(t *testing.T) {
	repo := validationRepository(t)
	validationWrite(t, repo, "main.go", "one\n")
	validationCommit(t, repo, "Alice", "alice@old.test", "2020-01-01T12:00:00Z", "2020-01-01T12:00:00Z", "Initial")
	config := filepath.Join(t.TempDir(), "vendors.yaml")
	validationWrite(t, filepath.Dir(config), filepath.Base(config), "vendors:\n  Old:\n    domains: [old.test]\n")
	dbPath := filepath.Join(t.TempDir(), "index.sqlite")
	options := Options{ConfigPath: config}
	index, err := OpenIndex(context.Background(), repo, options, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	original := index.fingerprint
	index.Close()
	validationWrite(t, filepath.Dir(config), filepath.Base(config), "vendors:\n  New:\n    domains: [old.test]\n")
	index, err = OpenIndex(context.Background(), repo, options, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if index.fingerprint == original || index.Status().Completed != 0 {
		t.Fatal("affiliation configuration did not invalidate")
	}
	if err := index.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	configured := index.fingerprint
	index.Close()
	validationWrite(t, repo, ".mailmap", "Unified Person <unified@example.test> <alice@old.test>\n")
	index, err = OpenIndex(context.Background(), repo, options, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	if index.fingerprint == configured || index.Status().Completed != 0 {
		t.Fatal("mailmap change did not invalidate")
	}
	if err := index.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	dataset, err := index.Query(context.Background(), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(dataset.People) != 1 || dataset.People[0].Email != "unified@example.test" {
		t.Fatalf("mailmap not refreshed: %+v", dataset.People)
	}
}

func TestIndexGitlinksAndSkewedBranchActivity(t *testing.T) {
	repo := validationRepository(t)
	validationWrite(t, repo, "main.go", "main\n")
	validationCommit(t, repo, "Main", "main@example.test", "2020-01-01T12:00:00Z", "2020-01-01T12:00:00Z", "Main")
	validationGit(t, repo, nil, "checkout", "-q", "-b", "side")
	validationWrite(t, repo, "side.go", "future recorded date\n")
	validationCommit(t, repo, "Side", "side@example.test", "2020-01-10T12:00:00Z", "2020-01-10T12:00:00Z", "Side future stamp")
	validationGit(t, repo, nil, "checkout", "-q", "main")
	validationGit(t, repo, []string{"GIT_AUTHOR_NAME=Merger", "GIT_AUTHOR_EMAIL=merge@example.test", "GIT_AUTHOR_DATE=2020-01-05T12:00:00Z", "GIT_COMMITTER_NAME=Merger", "GIT_COMMITTER_EMAIL=merge@example.test", "GIT_COMMITTER_DATE=2020-01-05T12:00:00Z"}, "merge", "--no-ff", "-q", "side", "-m", "Merge before side timestamp")
	validationGit(t, repo, nil, "update-index", "--add", "--cacheinfo", "160000,1111111111111111111111111111111111111111,external")
	validationGit(t, repo, []string{"GIT_AUTHOR_NAME=Main", "GIT_AUTHOR_EMAIL=main@example.test", "GIT_AUTHOR_DATE=2020-01-20T12:00:00Z", "GIT_COMMITTER_NAME=Main", "GIT_COMMITTER_EMAIL=main@example.test", "GIT_COMMITTER_DATE=2020-01-20T12:00:00Z"}, "commit", "-q", "-m", "Track absent submodule")
	index, err := OpenIndex(context.Background(), repo, Options{WindowDays: 30}, filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	if err := index.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	dataset, err := index.Query(context.Background(), "2020-01-01", "2020-02-01", "")
	if err != nil {
		t.Fatal(err)
	}
	var entry *SeriesPoint
	for n := range dataset.Series {
		point := &dataset.Series[n]
		if point.Date == "2020-01-10T12:00:00Z" {
			entry = point
		}
	}
	if entry == nil || entry.Kind != "activity" {
		t.Fatalf("skewed activity entry missing: %+v", entry)
	}
	var changed int
	for _, value := range entry.Contributions {
		changed += value.ChangedLines
	}
	if changed != 2 {
		t.Fatalf("entry changed lines=%d, want main+side=2", changed)
	}
	if dataset.Snapshots[1].Exclusions["submodule"] != 1 {
		t.Fatal("absent gitlink not excluded")
	}
	var storedCommits, mergeActivity int
	index.db.QueryRow("SELECT COUNT(*) FROM commits WHERE fingerprint=?", index.fingerprint).Scan(&storedCommits)
	index.db.QueryRow("SELECT COUNT(*) FROM activity WHERE fingerprint=? AND is_merge=1", index.fingerprint).Scan(&mergeActivity)
	if storedCommits != 4 || mergeActivity == 0 {
		t.Fatalf("reachable/merge facts not indexed: %d/%d", storedCommits, mergeActivity)
	}
	if index.Status().Total != 4 || dataset.Timeline.Revisions != 3 {
		t.Fatal("all reachable ownership and first-parent timeline counts disagree")
	}
}

func TestIndexNewHeadUsesOnlyItsFirstParentRevisionLine(t *testing.T) {
	repo := validationRepository(t)
	validationWrite(t, repo, "root.go", "root\n")
	root := validationCommit(t, repo, "Root", "root@example.test", "2020-01-01T12:00:00Z", "2020-01-01T12:00:00Z", "Root")
	validationWrite(t, repo, "old.go", "old line\n")
	oldHead := validationCommit(t, repo, "Old", "old@example.test", "2020-01-02T12:00:00Z", "2020-01-02T12:00:00Z", "Old head")
	dbPath := filepath.Join(t.TempDir(), "index.sqlite")
	index, err := OpenIndex(context.Background(), repo, Options{}, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	index.Close()
	validationGit(t, repo, nil, "checkout", "-q", "-b", "alternate", root)
	validationWrite(t, repo, "alternate.go", "alternate\n")
	validationCommit(t, repo, "Other", "other@example.test", "2020-01-03T12:00:00Z", "2020-01-03T12:00:00Z", "New first parent")
	validationGit(t, repo, []string{"GIT_AUTHOR_NAME=Merge", "GIT_AUTHOR_EMAIL=merge@example.test", "GIT_AUTHOR_DATE=2020-01-04T12:00:00Z", "GIT_COMMITTER_NAME=Merge", "GIT_COMMITTER_EMAIL=merge@example.test", "GIT_COMMITTER_DATE=2020-01-04T12:00:00Z"}, "merge", "--no-ff", "-q", "main", "-m", "Old head becomes second parent")
	index, err = OpenIndex(context.Background(), repo, Options{}, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	if status := index.Status(); status.Completed != 2 || status.Total != 4 {
		t.Fatalf("reachable ownership snapshots not reused across mainline change: %+v", status)
	}
	if err := index.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	dataset, err := index.Query(context.Background(), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, point := range dataset.Series {
		if point.Kind == "revision" && point.Revision == oldHead {
			t.Fatal("old second-parent head leaked into new mainline timeline")
		}
		if point.Kind == "activity" && point.Date == "2020-01-02T12:00:00Z" {
			t.Fatal("activity entry emitted before branch was reachable")
		}
	}
	if dataset.Timeline.Revisions != 3 {
		t.Fatalf("timeline=%+v", dataset.Timeline)
	}
}
