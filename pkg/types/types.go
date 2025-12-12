package types

import "time"

// CommitData represents a single commit with its metadata
type CommitData struct {
	SHA        string
	AuthorName string
	AuthorEmail string
	Date       time.Time
	Additions  int
	Deletions  int
	Message    string
}

// ContributorData represents aggregated contributor information
type ContributorData struct {
	Name  string
	Email string
	Commits int
}

// VendorMetrics contains metrics for a specific vendor or community
type VendorMetrics struct {
	Name               string
	TotalCommits       int
	TotalAdditions     int
	TotalDeletions     int
	UniqueContributors map[string]bool // email -> bool
	CommitsByMonth     map[string]int  // "YYYY-MM" -> count
	AdditionsByMonth   map[string]int
	DeletionsByMonth   map[string]int
}

// NewVendorMetrics creates a new VendorMetrics instance
func NewVendorMetrics(name string) *VendorMetrics {
	return &VendorMetrics{
		Name:               name,
		UniqueContributors: make(map[string]bool),
		CommitsByMonth:     make(map[string]int),
		AdditionsByMonth:   make(map[string]int),
		DeletionsByMonth:   make(map[string]int),
	}
}

// ContributorCount returns the number of unique contributors
func (vm *VendorMetrics) ContributorCount() int {
	return len(vm.UniqueContributors)
}

// NetChanges returns net lines changed (additions - deletions)
func (vm *VendorMetrics) NetChanges() int {
	return vm.TotalAdditions - vm.TotalDeletions
}

// AvgCommitSize returns average lines changed per commit
func (vm *VendorMetrics) AvgCommitSize() float64 {
	if vm.TotalCommits == 0 {
		return 0
	}
	return float64(vm.TotalAdditions+vm.TotalDeletions) / float64(vm.TotalCommits)
}

// RepositoryAnalysis contains complete analysis results
type RepositoryAnalysis struct {
	RepoName          string
	TotalCommits      int
	TotalContributors int
	DateRange         DateRange
	VendorMetrics     map[string]*VendorMetrics // vendor_name -> metrics
}

// DateRange represents a time range
type DateRange struct {
	Start time.Time
	End   time.Time
}

// GetVendorPercentage returns percentage of total for a specific metric
func (ra *RepositoryAnalysis) GetVendorPercentage(vendorName, metric string) float64 {
	vendor, ok := ra.VendorMetrics[vendorName]
	if !ok {
		return 0.0
	}

	switch metric {
	case "commits":
		if ra.TotalCommits == 0 {
			return 0
		}
		return float64(vendor.TotalCommits) / float64(ra.TotalCommits) * 100
	case "additions":
		total := 0
		for _, v := range ra.VendorMetrics {
			total += v.TotalAdditions
		}
		if total == 0 {
			return 0
		}
		return float64(vendor.TotalAdditions) / float64(total) * 100
	case "contributors":
		if ra.TotalContributors == 0 {
			return 0
		}
		return float64(vendor.ContributorCount()) / float64(ra.TotalContributors) * 100
	default:
		return 0.0
	}
}

// GetSortedVendors returns vendor names sorted by a metric
func (ra *RepositoryAnalysis) GetSortedVendors(by string) []string {
	vendors := make([]string, 0, len(ra.VendorMetrics))
	for name := range ra.VendorMetrics {
		vendors = append(vendors, name)
	}

	// Sort based on metric
	// (we'll implement a proper sort later, for now just return the list)
	return vendors
}
