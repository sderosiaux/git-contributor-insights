package ownership

import (
	"context"
	"fmt"
	"sync"
)

// Preparation is independent Git work and may finish out of order. A bounded
// lookahead window prevents completed attribution from growing with history.
// Application and atomic database commits remain strictly parent-before-child.
func (index *Index) runRevisionPipeline(ctx context.Context, cancel context.CancelFunc, completed map[string]bool) error {
	missing := make([]indexedRevision, 0, len(index.allRevisions)-len(completed))
	for _, revision := range index.allRevisions {
		if !completed[revision.sha] {
			missing = append(missing, revision)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	parallel := min(index.session.engine.workers, len(missing))
	// Keep several waves ahead: a large commit can hold up ordered writes
	// while the other workers continue preparing later, smaller commits.
	// The ring bounds retained results regardless of repository history size.
	window := min(parallel*4, len(missing))
	type prepared struct {
		files []preparedFile
		err   error
	}
	type job struct {
		revision indexedRevision
		result   chan prepared
	}
	jobs := make(chan job, window)
	pending := make([]chan prepared, window)
	var workers sync.WaitGroup
	for n := 0; n < parallel; n++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case work := <-jobs:
					files, err := index.prepareRevision(ctx, work.revision)
					work.result <- prepared{files: files, err: err}
				}
			}
		}()
	}
	// In-flight Git commands share this cancellation context. Never return
	// while background workers could still access a closed/reused Index.
	defer func() { cancel(); workers.Wait() }()
	enqueue := func(n int) {
		result := make(chan prepared, 1)
		pending[n%window] = result
		jobs <- job{revision: missing[n], result: result}
	}
	for n := 0; n < window; n++ {
		enqueue(n)
	}
	for n, revision := range missing {
		var result prepared
		select {
		case <-ctx.Done():
			return ctx.Err()
		case result = <-pending[n%window]:
		}
		pending[n%window] = nil
		if result.err != nil {
			return fmt.Errorf("prepare revision %s: %w", revision.sha, result.err)
		}
		state, delta, err := index.applyPreparedRevision(ctx, revision, result.files)
		if err != nil {
			return fmt.Errorf("index revision %s: %w", revision.sha, err)
		}
		if err := index.persistRevisionState(ctx, revision, state, delta); err != nil {
			return err
		}
		index.mu.Lock()
		index.status.Completed++
		index.mu.Unlock()
		if next := n + window; next < len(missing) {
			enqueue(next)
		}
	}
	return nil
}
