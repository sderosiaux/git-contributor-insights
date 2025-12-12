package analyzer

import (
	"fmt"
	"sort"

	"github.com/sderosiaux/ghca/pkg/config"
	"github.com/sderosiaux/ghca/pkg/types"
)

// Analyzer analyzes commit and contributor data
type Analyzer struct {
	config *config.Config
}

// New creates a new Analyzer
func New(cfg *config.Config) *Analyzer {
	return &Analyzer{
		config: cfg,
	}
}

// Analyze performs complete analysis of commits and contributors
func (a *Analyzer) Analyze(commits []*types.CommitData, contributors []*types.ContributorData, repoName string) *types.RepositoryAnalysis {
	// Initialize metrics for each category
	vendorMetrics := make(map[string]*types.VendorMetrics)
	for _, category := range a.config.GetAllCategories() {
		vendorMetrics[category] = types.NewVendorMetrics(category)
	}

	// Track global date range
	var minDate, maxDate types.DateRange
	if len(commits) > 0 {
		minDate.Start = commits[0].Date
		maxDate.End = commits[0].Date
	}

	// Track all unique contributors
	allContributors := make(map[string]bool)

	// Process each commit
	for _, commit := range commits {
		// Classify contributor
		vendor := a.config.Classify(commit.AuthorEmail, "", "")

		// Get or create metrics for this vendor
		metrics := vendorMetrics[vendor]
		if metrics == nil {
			metrics = types.NewVendorMetrics(vendor)
			vendorMetrics[vendor] = metrics
		}

		// Update commit counts
		metrics.TotalCommits++
		metrics.TotalAdditions += commit.Additions
		metrics.TotalDeletions += commit.Deletions

		// Track contributor
		contributorID := commit.AuthorEmail
		if contributorID == "" {
			contributorID = commit.AuthorName
		}
		if contributorID != "" {
			metrics.UniqueContributors[contributorID] = true
			allContributors[contributorID] = true
		}

		// Track monthly metrics
		monthKey := commit.Date.Format("2006-01")
		metrics.CommitsByMonth[monthKey]++
		metrics.AdditionsByMonth[monthKey] += commit.Additions
		metrics.DeletionsByMonth[monthKey] += commit.Deletions

		// Update date range
		if commit.Date.Before(minDate.Start) || minDate.Start.IsZero() {
			minDate.Start = commit.Date
		}
		if commit.Date.After(maxDate.End) || maxDate.End.IsZero() {
			maxDate.End = commit.Date
		}
	}

	return &types.RepositoryAnalysis{
		RepoName:          repoName,
		TotalCommits:      len(commits),
		TotalContributors: len(allContributors),
		DateRange:         types.DateRange{Start: minDate.Start, End: maxDate.End},
		VendorMetrics:     vendorMetrics,
	}
}

// GetSortedVendors returns vendors sorted by a metric
func GetSortedVendors(analysis *types.RepositoryAnalysis, by string, reverse bool) []string {
	vendors := make([]string, 0, len(analysis.VendorMetrics))
	for name := range analysis.VendorMetrics {
		vendors = append(vendors, name)
	}

	sort.Slice(vendors, func(i, j int) bool {
		vi := analysis.VendorMetrics[vendors[i]]
		vj := analysis.VendorMetrics[vendors[j]]

		var result bool
		switch by {
		case "commits":
			result = vi.TotalCommits > vj.TotalCommits
		case "additions":
			result = vi.TotalAdditions > vj.TotalAdditions
		case "contributors":
			result = vi.ContributorCount() > vj.ContributorCount()
		default:
			result = vi.TotalCommits > vj.TotalCommits
		}

		if !reverse {
			result = !result
		}

		return result
	})

	return vendors
}

// GetTimelineData generates timeline data for all vendors
func GetTimelineData(analysis *types.RepositoryAnalysis, metric string) map[string]map[string]int {
	timeline := make(map[string]map[string]int)

	for vendorName, metrics := range analysis.VendorMetrics {
		var dataByMonth map[string]int

		switch metric {
		case "commits":
			dataByMonth = metrics.CommitsByMonth
		case "additions":
			dataByMonth = metrics.AdditionsByMonth
		case "deletions":
			dataByMonth = metrics.DeletionsByMonth
		default:
			continue
		}

		for month, value := range dataByMonth {
			if timeline[month] == nil {
				timeline[month] = make(map[string]int)
			}
			timeline[month][vendorName] = value
		}
	}

	return timeline
}

// FormatNumber formats a number with commas
func FormatNumber(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}

	str := fmt.Sprintf("%d", n)
	var result []byte
	for i, c := range str {
		if i > 0 && (len(str)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, byte(c))
	}

	return string(result)
}
