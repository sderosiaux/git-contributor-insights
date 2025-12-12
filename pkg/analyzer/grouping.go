package analyzer

import (
	"sort"

	"github.com/sderosiaux/ghca/pkg/types"
)

// VendorGroup represents grouped vendor data
type VendorGroup struct {
	Name               string
	TotalCommits       int
	TotalAdditions     int
	TotalDeletions     int
	UniqueContributors map[string]bool
	IsGrouped          bool // true if this is the "others" group
}

// GroupVendors groups vendors into top N + "others"
// Always shows "community" separately, then top N-1 vendors, then groups the rest
func GroupVendors(vendorMetrics map[string]*types.VendorMetrics, topN int) []*VendorGroup {
	// Separate community from vendors
	var communityMetrics *types.VendorMetrics
	vendors := make(map[string]*types.VendorMetrics)

	for name, metrics := range vendorMetrics {
		if name == "community" {
			communityMetrics = metrics
		} else {
			vendors[name] = metrics
		}
	}

	// Sort vendors by commit count
	vendorNames := make([]string, 0, len(vendors))
	for name := range vendors {
		vendorNames = append(vendorNames, name)
	}
	sort.Slice(vendorNames, func(i, j int) bool {
		return vendors[vendorNames[i]].TotalCommits > vendors[vendorNames[j]].TotalCommits
	})

	// Build result
	result := make([]*VendorGroup, 0)

	// Add community first
	if communityMetrics != nil {
		result = append(result, &VendorGroup{
			Name:               "community",
			TotalCommits:       communityMetrics.TotalCommits,
			TotalAdditions:     communityMetrics.TotalAdditions,
			TotalDeletions:     communityMetrics.TotalDeletions,
			UniqueContributors: communityMetrics.UniqueContributors,
			IsGrouped:          false,
		})
	}

	// If we have fewer vendors than topN, show all individually
	if len(vendorNames) <= topN {
		for _, name := range vendorNames {
			metrics := vendors[name]
			result = append(result, &VendorGroup{
				Name:               name,
				TotalCommits:       metrics.TotalCommits,
				TotalAdditions:     metrics.TotalAdditions,
				TotalDeletions:     metrics.TotalDeletions,
				UniqueContributors: metrics.UniqueContributors,
				IsGrouped:          false,
			})
		}
		return result
	}

	// Show top N-1 vendors individually
	showIndividually := topN - 1
	for i := 0; i < showIndividually && i < len(vendorNames); i++ {
		name := vendorNames[i]
		metrics := vendors[name]
		result = append(result, &VendorGroup{
			Name:               name,
			TotalCommits:       metrics.TotalCommits,
			TotalAdditions:     metrics.TotalAdditions,
			TotalDeletions:     metrics.TotalDeletions,
			UniqueContributors: metrics.UniqueContributors,
			IsGrouped:          false,
		})
	}

	// Group the rest as "others"
	if len(vendorNames) > showIndividually {
		othersGroup := &VendorGroup{
			Name:               "others",
			TotalCommits:       0,
			TotalAdditions:     0,
			TotalDeletions:     0,
			UniqueContributors: make(map[string]bool),
			IsGrouped:          true,
		}

		for i := showIndividually; i < len(vendorNames); i++ {
			metrics := vendors[vendorNames[i]]
			othersGroup.TotalCommits += metrics.TotalCommits
			othersGroup.TotalAdditions += metrics.TotalAdditions
			othersGroup.TotalDeletions += metrics.TotalDeletions
			for contributor := range metrics.UniqueContributors {
				othersGroup.UniqueContributors[contributor] = true
			}
		}

		result = append(result, othersGroup)
	}

	return result
}

// ContributorCount returns the number of unique contributors
func (vg *VendorGroup) ContributorCount() int {
	return len(vg.UniqueContributors)
}

// NetChanges returns net line changes (additions - deletions)
func (vg *VendorGroup) NetChanges() int {
	return vg.TotalAdditions - vg.TotalDeletions
}
