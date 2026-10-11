package ownership

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func timelineTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestCalendarDatesKeepExactBoundsAndUTCBoundaries(t *testing.T) {
	tests := []struct {
		interval, start, end string
		want                 []string
	}{
		{"month", "2020-01-15T12:34:56Z", "2020-04-17T19:20:21Z", []string{"2020-01-15T12:34:56Z", "2020-02-01T00:00:00Z", "2020-03-01T00:00:00Z", "2020-04-01T00:00:00Z", "2020-04-17T19:20:21Z"}},
		{"week", "2024-03-06T12:00:00Z", "2024-03-19T12:00:00Z", []string{"2024-03-06T12:00:00Z", "2024-03-11T00:00:00Z", "2024-03-18T00:00:00Z", "2024-03-19T12:00:00Z"}},
		{"day", "2024-02-28T12:00:00Z", "2024-03-01T12:00:00Z", []string{"2024-02-28T12:00:00Z", "2024-02-29T00:00:00Z", "2024-03-01T00:00:00Z", "2024-03-01T12:00:00Z"}},
		{"quarter", "2024-02-01T00:00:00Z", "2024-08-01T00:00:00Z", []string{"2024-02-01T00:00:00Z", "2024-04-01T00:00:00Z", "2024-07-01T00:00:00Z", "2024-08-01T00:00:00Z"}},
	}
	for _, test := range tests {
		t.Run(test.interval, func(t *testing.T) {
			got, err := calendarDates(timelineTime(t, test.start), timelineTime(t, test.end), test.interval)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(test.want) {
				t.Fatalf("dates=%v, want %v", got, test.want)
			}
			for i, date := range got {
				if date.Format(time.RFC3339) != test.want[i] {
					t.Errorf("date%d=%s, want%s", i, date, test.want[i])
				}
			}
		})
	}
}

func TestAutomaticResolutionAndExplicitSamples(t *testing.T) {
	e := &engine{commits: map[string]commitInfo{"first": {committed: timelineTime(t, "1900-01-01T00:00:00Z")}, "last": {committed: timelineTime(t, "2025-01-01T00:00:00Z")}}}
	for _, test := range []struct {
		since, until, interval string
		count                  int
	}{
		{"2015-01-15T12:00:00Z", "2025-01-15T12:00:00Z", "month", 122},
		{"2024-01-01T00:00:00Z", "2025-01-01T00:00:00Z", "week", 54},
		{"2024-02-28T00:00:00Z", "2024-03-01T00:00:00Z", "day", 3},
		{"1950-01-01T00:00:00Z", "2025-01-01T00:00:00Z", "year", 76},
		{"1990-01-01T00:00:00Z", "2025-01-01T00:00:00Z", "quarter", 141},
	} {
		t.Run(test.interval+test.since, func(t *testing.T) {
			cutoffs, timeline, _, err := e.selectCutoffs([]string{"last", "first"}, Options{Since: test.since, Until: test.until, Interval: "auto"})
			if err != nil {
				t.Fatal(err)
			}
			if timeline.Interval != test.interval || len(cutoffs) != test.count {
				t.Fatalf("%+v, cutoffs%d", timeline, len(cutoffs))
			}
			for _, selected := range cutoffs {
				if selected.sha != "" && e.commits[selected.sha].committed.After(selected.date) {
					t.Fatal("future revision leaked")
				}
			}
		})
	}
	cutoffs, timeline, _, err := e.selectCutoffs([]string{"last", "first"}, Options{Since: "2015-01-01", Until: "2025-01-01", Snapshots: 8, Interval: "auto"})
	if err != nil || len(cutoffs) != 8 || timeline.Calendar || timeline.Interval != "uniform" {
		t.Fatalf("explicit opt-in: %+v %v", timeline, err)
	}
	if _, err := calendarDates(timelineTime(t, "2020-01-01T00:00:00Z"), timelineTime(t, "2025-01-01T00:00:00Z"), "day"); err == nil {
		t.Fatal("unbounded daily range accepted")
	}
}

func TestSessionFreezesHistoryConfigurationAndSupportsCancelledWait(t *testing.T) {
	repo := validationRepository(t)
	validationWrite(t, repo, "main.go", "first\n")
	first := validationCommit(t, repo, "Alice", "alice@old.test", "2020-01-10T12:00:00Z", "2020-01-10T12:00:00Z", "First")
	config := filepath.Join(t.TempDir(), "companies.yaml")
	validationWrite(t, filepath.Dir(config), filepath.Base(config), "vendors:\n  Original:\n    domains: [old.test]\n")
	session, err := NewSession(context.Background(), repo, Options{ConfigPath: config, WindowDays: 17})
	if err != nil {
		t.Fatal(err)
	}
	validationWrite(t, repo, "main.go", "first\nsecond\n")
	validationCommit(t, repo, "Bob", "bob@new.test", "2020-02-10T12:00:00Z", "2020-02-10T12:00:00Z", "Second after session started")
	validationWrite(t, filepath.Dir(config), filepath.Base(config), "vendors:\n  Rewritten:\n    domains: [old.test]\n")
	ctx, cancel := context.WithCancel(context.Background())
	session.gate <- struct{}{}
	cancel()
	if _, err := session.Build(ctx, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait=%v", err)
	}
	<-session.gate
	dataset, err := session.Build(context.Background(), Options{Since: "2020-01-10", Until: "2020-02-15", Interval: "month"})
	if err != nil {
		t.Fatal(err)
	}
	if dataset.Repository.Head != first || dataset.Method.WindowDays != 17 {
		t.Fatalf("session defaults changed: %+v", dataset.Repository)
	}
	if dataset.Snapshots[0].Revision != "" || len(dataset.Snapshots[0].Files) != 0 {
		t.Fatal("midnight boundary before first commit must be empty")
	}
	for _, snapshot := range dataset.Snapshots[1:] {
		if snapshot.Revision != first {
			t.Fatalf("HEAD was not frozen: %s", snapshot.Revision)
		}
		file := validationFile(t, &snapshot, "main.go")
		if file.Lines != 1 || validationOrganizationLines(file, "Original") != 1 {
			t.Fatalf("configuration/history not frozen: %+v", file)
		}
	}
}

func TestSessionBlameCachePreservesRevertsAndMergeAttribution(t *testing.T) {
	repo := validationRepository(t)
	validationWrite(t, repo, "main.go", "old one\nold two\n")
	validationCommit(t, repo, "Alice", "alice@example.test", "2020-01-01T12:00:00Z", "2020-01-01T12:00:00Z", "Original")
	validationWrite(t, repo, "main.go", "new one\nnew two\n")
	validationCommit(t, repo, "Bob", "bob@example.test", "2020-02-01T12:00:00Z", "2020-02-01T12:00:00Z", "Rewrite")
	validationWrite(t, repo, "main.go", "old one\nold two\n")
	validationCommit(t, repo, "Carol", "carol@example.test", "2020-03-01T12:00:00Z", "2020-03-01T12:00:00Z", "Restore identical original blob")
	validationGit(t, repo, nil, "checkout", "-q", "-b", "side")
	validationWrite(t, repo, "main.go", "same final content\n")
	validationCommit(t, repo, "Dan", "dan@example.test", "2020-04-02T12:00:00Z", "2020-04-02T12:00:00Z", "Side rewrite")
	validationGit(t, repo, nil, "checkout", "-q", "main")
	validationWrite(t, repo, "main.go", "same final content\n")
	validationCommit(t, repo, "Eve", "eve@example.test", "2020-04-01T12:00:00Z", "2020-04-01T12:00:00Z", "Main rewrite to identical content")
	validationGit(t, repo, []string{"GIT_AUTHOR_NAME=Merger", "GIT_AUTHOR_EMAIL=merger@example.test", "GIT_AUTHOR_DATE=2020-05-01T12:00:00Z", "GIT_COMMITTER_NAME=Merger", "GIT_COMMITTER_EMAIL=merger@example.test", "GIT_COMMITTER_DATE=2020-05-01T12:00:00Z"}, "merge", "-q", "--no-ff", "side", "-m", "Merge identical trees")
	session, err := NewSession(context.Background(), repo, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, month := range []int{1, 2, 3, 4, 5, 3, 1, 5} {
		date := fmt.Sprintf("2020-%02d-15T12:00:00Z", month)
		dataset, err := session.Build(context.Background(), Options{Since: date, Until: date, Interval: "day"})
		if err != nil {
			t.Fatal(err)
		}
		snapshot := &dataset.Snapshots[0]
		file := validationFile(t, snapshot, "main.go")
		counts := map[string]int{}
		for _, line := range strings.Split(validationGit(t, repo, nil, "blame", "--no-textconv", "--line-porcelain", snapshot.Revision, "--", "main.go"), "\n") {
			if strings.HasPrefix(line, "author-mail ") {
				counts[strings.Trim(strings.TrimPrefix(line, "author-mail "), "<>")]++
			}
		}
		for _, contribution := range file.Contributions {
			if contribution.Lines == 0 {
				continue
			}
			person := session.engine.identities[contribution.PersonID]
			if contribution.Lines != counts[person.Email] {
				t.Fatalf("cached blame at %s: %s=%d, git=%v", date, person.Email, contribution.Lines, counts)
			}
		}
	}
}
