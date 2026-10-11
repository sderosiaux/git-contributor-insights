package ownership

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestAggregateReplacementTargetsOneRevision(t *testing.T) {
	repo := validationRepository(t)
	validationWrite(t, repo, "code.go", "line\n")
	validationCommit(t, repo, "Alice", "alice@test", "2020-01-01T12:00:00Z", "2020-01-01T12:00:00Z", "Initial")
	index, err := OpenIndex(context.Background(), repo, Options{}, filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	// Scope-first primary keys support chart queries but make per-revision
	// replacement scan the entire repository unless SHA is also indexed here.
	for _, table := range []string{"aggregates", "aggregate_deltas"} {
		var id, parent, unused int
		var plan string
		err := index.db.QueryRow("EXPLAIN QUERY PLAN DELETE FROM "+table+" WHERE fingerprint=? AND sha=?", index.fingerprint, index.Status().Head).Scan(&id, &parent, &unused, &plan)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(plan, "sha=?") {
			t.Fatalf("%s replacement scans unrelated commits: %s", table, plan)
		}
	}
}
