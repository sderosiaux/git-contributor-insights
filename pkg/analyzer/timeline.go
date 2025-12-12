package analyzer

import (
	"fmt"
	"sort"
	"time"

	"github.com/sderosiaux/ghca/pkg/config"
	"github.com/sderosiaux/ghca/pkg/types"
)

// TimeBreakdown represents metrics for a specific time period
type TimeBreakdown struct {
	Period        string                        // e.g., "2024", "2024-Q1", "2024-01", "2024-W01"
	StartDate     time.Time
	EndDate       time.Time
	VendorMetrics map[string]*types.VendorMetrics
	TotalCommits  int
}

// TimelineAnalysis represents the complete timeline breakdown
type TimelineAnalysis struct {
	RepoName   string
	Breakdown  string // "year", "quarter", "month", "week"
	Periods    []*TimeBreakdown
	DateRange  types.DateRange
}

// AnalyzeTimeline analyzes commits with time breakdown
func AnalyzeTimeline(commits []*types.CommitData, cfg *config.Config, repoName string, breakdownType string) *TimelineAnalysis {
	if len(commits) == 0 {
		return &TimelineAnalysis{
			RepoName:  repoName,
			Breakdown: breakdownType,
			Periods:   []*TimeBreakdown{},
		}
	}

	// Group commits by time period
	periodMap := make(map[string][]*types.CommitData)

	for _, commit := range commits {
		period := getPeriodKey(commit.Date, breakdownType)
		periodMap[period] = append(periodMap[period], commit)
	}

	// Sort periods
	periods := make([]string, 0, len(periodMap))
	for period := range periodMap {
		periods = append(periods, period)
	}
	sort.Strings(periods)

	// Analyze each period
	breakdowns := make([]*TimeBreakdown, 0, len(periods))

	for _, period := range periods {
		periodCommits := periodMap[period]

		// Initialize vendor metrics for this period
		vendorMetrics := make(map[string]*types.VendorMetrics)
		for _, category := range cfg.GetAllCategories() {
			vendorMetrics[category] = types.NewVendorMetrics(category)
		}

		// Process commits for this period
		totalCommits := 0
		for _, commit := range periodCommits {
			vendor := cfg.Classify(commit.AuthorEmail, "", "")
			metrics := vendorMetrics[vendor]

			metrics.TotalCommits++
			metrics.TotalAdditions += commit.Additions
			metrics.TotalDeletions += commit.Deletions
			metrics.UniqueContributors[commit.AuthorEmail] = true

			totalCommits++
		}

		// Determine start/end dates for this period
		startDate, endDate := getPeriodRange(period, breakdownType)

		breakdowns = append(breakdowns, &TimeBreakdown{
			Period:        period,
			StartDate:     startDate,
			EndDate:       endDate,
			VendorMetrics: vendorMetrics,
			TotalCommits:  totalCommits,
		})
	}

	// Determine overall date range
	dateRange := types.DateRange{
		Start: commits[0].Date,
		End:   commits[0].Date,
	}
	for _, c := range commits {
		if c.Date.Before(dateRange.Start) {
			dateRange.Start = c.Date
		}
		if c.Date.After(dateRange.End) {
			dateRange.End = c.Date
		}
	}

	return &TimelineAnalysis{
		RepoName:  repoName,
		Breakdown: breakdownType,
		Periods:   breakdowns,
		DateRange: dateRange,
	}
}

// getPeriodKey returns the period key for a given date and breakdown type
func getPeriodKey(date time.Time, breakdownType string) string {
	switch breakdownType {
	case "year":
		return fmt.Sprintf("%d", date.Year())
	case "quarter":
		quarter := (date.Month()-1)/3 + 1
		return fmt.Sprintf("%d-Q%d", date.Year(), quarter)
	case "month":
		return fmt.Sprintf("%d-%02d", date.Year(), date.Month())
	case "week":
		year, week := date.ISOWeek()
		return fmt.Sprintf("%d-W%02d", year, week)
	default:
		return fmt.Sprintf("%d", date.Year())
	}
}

// getPeriodRange returns the start and end dates for a period key
func getPeriodRange(period string, breakdownType string) (time.Time, time.Time) {
	var start, end time.Time

	switch breakdownType {
	case "year":
		var year int
		fmt.Sscanf(period, "%d", &year)
		start = time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
		end = time.Date(year, 12, 31, 23, 59, 59, 0, time.UTC)

	case "quarter":
		var year, quarter int
		fmt.Sscanf(period, "%d-Q%d", &year, &quarter)
		startMonth := time.Month((quarter-1)*3 + 1)
		endMonth := time.Month(quarter * 3)
		start = time.Date(year, startMonth, 1, 0, 0, 0, 0, time.UTC)
		end = time.Date(year, endMonth+1, 1, 0, 0, 0, 0, time.UTC).Add(-time.Second)

	case "month":
		var year, month int
		fmt.Sscanf(period, "%d-%d", &year, &month)
		start = time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
		end = time.Date(year, time.Month(month)+1, 1, 0, 0, 0, 0, time.UTC).Add(-time.Second)

	case "week":
		var year, week int
		fmt.Sscanf(period, "%d-W%d", &year, &week)
		// ISO week calculation
		start = time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
		// Find the first day of week 1
		for start.Weekday() != time.Monday {
			start = start.AddDate(0, 0, 1)
		}
		start = start.AddDate(0, 0, (week-1)*7)
		end = start.AddDate(0, 0, 7).Add(-time.Second)
	}

	return start, end
}

// GetVendorPercentage calculates vendor percentage for a time breakdown
func (tb *TimeBreakdown) GetVendorPercentage(vendor string, metric string) float64 {
	if tb.TotalCommits == 0 {
		return 0
	}

	metrics, ok := tb.VendorMetrics[vendor]
	if !ok {
		return 0
	}

	switch metric {
	case "commits":
		return (float64(metrics.TotalCommits) / float64(tb.TotalCommits)) * 100
	case "additions":
		totalAdditions := 0
		for _, m := range tb.VendorMetrics {
			totalAdditions += m.TotalAdditions
		}
		if totalAdditions == 0 {
			return 0
		}
		return (float64(metrics.TotalAdditions) / float64(totalAdditions)) * 100
	case "contributors":
		totalContributors := 0
		for _, m := range tb.VendorMetrics {
			totalContributors += m.ContributorCount()
		}
		if totalContributors == 0 {
			return 0
		}
		return (float64(metrics.ContributorCount()) / float64(totalContributors)) * 100
	default:
		return 0
	}
}
