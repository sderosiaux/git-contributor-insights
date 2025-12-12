package git

import (
	"fmt"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/sderosiaux/ghca/pkg/types"
)

// Fetcher handles Git repository operations
type Fetcher struct {
	repo *git.Repository
	path string
}

// NewFetcher creates a new Git fetcher
func NewFetcher(repoPath string) (*Fetcher, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	return &Fetcher{
		repo: repo,
		path: repoPath,
	}, nil
}

// FetchCommits fetches all commits with optional date filtering
// Uses concurrency for faster processing
func (f *Fetcher) FetchCommits(since, until *time.Time, workers int) ([]*types.CommitData, error) {
	// Get commit iterator
	ref, err := f.repo.Head()
	if err != nil {
		return nil, fmt.Errorf("failed to get HEAD: %w", err)
	}

	iter, err := f.repo.Log(&git.LogOptions{
		From: ref.Hash(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get log: %w", err)
	}
	defer iter.Close()

	// Collect all commits first
	var allCommits []*object.Commit
	err = iter.ForEach(func(c *object.Commit) error {
		// Apply date filters
		if since != nil && c.Committer.When.Before(*since) {
			return nil
		}
		if until != nil && c.Committer.When.After(*until) {
			return nil
		}

		allCommits = append(allCommits, c)
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Process commits concurrently
	return f.processCommitsConcurrent(allCommits, workers)
}

// processCommitsConcurrent processes commits in parallel using goroutines
func (f *Fetcher) processCommitsConcurrent(commits []*object.Commit, workers int) ([]*types.CommitData, error) {
	if workers <= 0 {
		workers = 4 // default
	}

	type result struct {
		index int
		data  *types.CommitData
		err   error
	}

	// Create job channel and results channel
	jobs := make(chan struct {
		index  int
		commit *object.Commit
	}, len(commits))
	results := make(chan result, len(commits))

	// Start worker pool
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				data, err := f.processCommit(job.commit)
				results <- result{
					index: job.index,
					data:  data,
					err:   err,
				}
			}
		}()
	}

	// Send jobs
	go func() {
		for i, commit := range commits {
			jobs <- struct {
				index  int
				commit *object.Commit
			}{index: i, commit: commit}
		}
		close(jobs)
	}()

	// Wait for all workers to finish
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results (maintaining order)
	commitData := make([]*types.CommitData, len(commits))
	for r := range results {
		if r.err != nil {
			// Skip commits that error (similar to Python version)
			continue
		}
		commitData[r.index] = r.data
	}

	// Filter out nil entries (errors)
	filteredData := make([]*types.CommitData, 0, len(commitData))
	for _, data := range commitData {
		if data != nil {
			filteredData = append(filteredData, data)
		}
	}

	return filteredData, nil
}

// processCommit processes a single commit to extract stats
func (f *Fetcher) processCommit(commit *object.Commit) (*types.CommitData, error) {
	// Get commit stats
	stats, err := commit.Stats()
	if err != nil {
		return nil, fmt.Errorf("failed to get stats: %w", err)
	}

	additions := 0
	deletions := 0
	for _, stat := range stats {
		additions += stat.Addition
		deletions += stat.Deletion
	}

	// Get first line of message
	message := commit.Message
	if len(message) > 100 {
		message = message[:100]
	}

	return &types.CommitData{
		SHA:         commit.Hash.String(),
		AuthorName:  commit.Author.Name,
		AuthorEmail: commit.Author.Email,
		Date:        commit.Author.When,
		Additions:   additions,
		Deletions:   deletions,
		Message:     message,
	}, nil
}

// FetchContributors fetches unique contributors from the repository
func (f *Fetcher) FetchContributors() ([]*types.ContributorData, error) {
	ref, err := f.repo.Head()
	if err != nil {
		return nil, fmt.Errorf("failed to get HEAD: %w", err)
	}

	iter, err := f.repo.Log(&git.LogOptions{
		From: ref.Hash(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get log: %w", err)
	}
	defer iter.Close()

	contributorsMap := make(map[string]*types.ContributorData)

	err = iter.ForEach(func(c *object.Commit) error {
		email := c.Author.Email
		if _, exists := contributorsMap[email]; !exists {
			contributorsMap[email] = &types.ContributorData{
				Name:  c.Author.Name,
				Email: email,
				Commits: 1,
			}
		} else {
			contributorsMap[email].Commits++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Convert map to slice
	contributors := make([]*types.ContributorData, 0, len(contributorsMap))
	for _, c := range contributorsMap {
		contributors = append(contributors, c)
	}

	return contributors, nil
}

// GetRepoName extracts repository name from remote URL or directory
func (f *Fetcher) GetRepoName() string {
	// Try to get from remote
	remote, err := f.repo.Remote("origin")
	if err == nil {
		urls := remote.Config().URLs
		if len(urls) > 0 {
			// Extract owner/repo from URL
			// e.g., https://github.com/apache/kafka.git -> apache/kafka
			url := urls[0]
			// Simple extraction (could be improved)
			if idx := len(url) - 1; url[idx] == '/' {
				url = url[:idx]
			}
			if last := url[len(url)-4:]; last == ".git" {
				url = url[:len(url)-4]
			}
			parts := splitPath(url)
			if len(parts) >= 2 {
				return parts[len(parts)-2] + "/" + parts[len(parts)-1]
			}
		}
	}

	// Fallback to directory name
	parts := splitPath(f.path)
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}

	return "unknown"
}

func splitPath(path string) []string {
	// Simple path splitter that handles both / and \
	result := []string{}
	current := ""

	for i := 0; i < len(path); i++ {
		c := path[i]
		if c == '/' || c == '\\' {
			if current != "" {
				result = append(result, current)
				current = ""
			}
		} else {
			current += string(c)
		}
	}

	if current != "" {
		result = append(result, current)
	}

	return result
}
