package ownership

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func assertCompactEquivalent(t *testing.T, index *Index, from, to, scope, tip string) {
	t.Helper()
	dense, err := index.QueryHistory(context.Background(), from, to, scope, tip)
	if err != nil {
		t.Fatal(err)
	}
	sparse, err := index.QueryCompactHistory(context.Background(), from, to, scope, tip)
	if err != nil {
		t.Fatal(err)
	}
	if sparse.SeriesEncoding != "changes-v1" || !reflect.DeepEqual(dense.Timeline, sparse.Timeline) {
		t.Fatal("timeline metadata changed")
	}
	if !reflect.DeepEqual(dense.Snapshots, sparse.Snapshots) || !reflect.DeepEqual(dense.People, sparse.People) {
		t.Fatal("endpoint or identity changed")
	}
	current := map[string]SeriesContribution{}
	for n, point := range sparse.Series {
		want := dense.Series[n]
		if point.Date != want.Date || point.Revision != want.Revision || point.Kind != want.Kind || point.Lines != want.Lines || point.CommittedDate != want.CommittedDate {
			t.Fatalf("point %d metadata changed: %+v / %+v", n, point, want)
		}
		for _, value := range point.Contributions {
			key := contributionKey(value)
			if value.Lines == 0 && value.ChangedLines == 0 {
				delete(current, key)
			} else {
				current[key] = value
			}
		}
		expected := map[string]SeriesContribution{}
		for _, value := range want.Contributions {
			if value.Lines != 0 || value.ChangedLines != 0 {
				expected[contributionKey(value)] = value
			}
		}
		if !reflect.DeepEqual(current, expected) {
			t.Fatalf("%s/%s point %d %s %s: reconstructed=%+v; want=%+v", scope, tip, n, point.Date, point.Kind, current, expected)
		}
	}
}

func TestCompactSeriesExactAcrossAliasesCollisionsAndSkewedBranches(t *testing.T) {
	repo := validationRepository(t)
	commit := func(email, date string) string {
		return validationCommit(t, repo, "Same Name", email, date, date, "Change")
	}
	validationWrite(t, repo, "src/a.go", "initial\n")
	root := commit("one@corp.test", "2020-01-01T12:00:00Z")
	validationGit(t, repo, nil, "checkout", "-q", "-b", "side")
	validationWrite(t, repo, "side.go", "future recorded date\n")
	side := commit("two@other.test", "2020-01-10T12:00:00Z")
	validationGit(t, repo, nil, "checkout", "-q", "main")
	validationWrite(t, repo, "src/a.go", "initial\nsame timestamp\n")
	commit("two@other.test", "2020-01-01T12:00:00Z")
	validationWrite(t, repo, "src/a.go", "replacement\n")
	commit("three@third.test", "2020-01-05T12:00:00Z")
	validationGit(t, repo, []string{"GIT_AUTHOR_NAME=Merger", "GIT_AUTHOR_EMAIL=merge@test", "GIT_AUTHOR_DATE=2020-01-05T12:00:00Z", "GIT_COMMITTER_NAME=Merger", "GIT_COMMITTER_EMAIL=merge@test", "GIT_COMMITTER_DATE=2020-01-05T12:00:00Z"}, "merge", "--no-ff", "-q", "side", "-m", "Same-time merge before branch stamp")
	validationGit(t, repo, nil, "rm", "-q", "src/a.go")
	commit("one@corp.test", "2020-01-20T12:00:00Z")
	validationWrite(t, repo, "src/a.go", "reintroduced\n")
	commit("one@corp.test", "2020-01-22T12:00:00Z")
	config := filepath.Join(t.TempDir(), "orgs.yaml")
	validationWrite(t, filepath.Dir(config), filepath.Base(config), "vendors:\n  One:\n    domains: [corp.test]\n  Two:\n    domains: [other.test]\n")
	for _, window := range []int{2, 30} {
		index, err := OpenIndex(context.Background(), repo, Options{WindowDays: window, ConfigPath: config}, filepath.Join(t.TempDir(), "index.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		if err := index.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		for _, tip := range []string{"", root, side} {
			for _, scope := range []string{"", "src", "src/a.go", "side.go", "missing"} {
				for _, dates := range [][2]string{{"", ""}, {"2019-12-01", "2020-03-01"}, {"2020-01-03", "2020-01-13"}, {"2020-01-05", "2020-01-05"}, {"2020-01-21", "2020-02-01"}} {
					assertCompactEquivalent(t, index, dates[0], dates[1], scope, tip)
				}
			}
		}
		index.Close()
	}
}
