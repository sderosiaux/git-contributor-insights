package explorer

import (
	"sort"

	"github.com/sderosiaux/git-contributor-insights/pkg/ownership"
)

// compactSeries preserves every point and encodes only changed absolute values
// after the first point. A zero/zero value removes a person/organization pair.
// Unlike a sampled chart, the full exact series can be reconstructed losslessly.
func compactSeries(series []ownership.SeriesPoint) []ownership.SeriesPoint {
	result := make([]ownership.SeriesPoint, 0, len(series))
	previous := map[string]ownership.SeriesContribution{}
	keyOf := func(value ownership.SeriesContribution) string { return value.PersonID + "\x00" + value.OrganizationID }
	for _, point := range series {
		next := make(map[string]ownership.SeriesContribution, len(point.Contributions))
		changes := []ownership.SeriesContribution{}
		for _, value := range point.Contributions {
			if value.Lines == 0 && value.ChangedLines == 0 {
				continue
			}
			key := keyOf(value)
			next[key] = value
			if prior, exists := previous[key]; !exists || prior != value {
				changes = append(changes, value)
			}
		}
		for key, value := range previous {
			if _, exists := next[key]; !exists {
				value.Lines, value.ChangedLines = 0, 0
				changes = append(changes, value)
			}
		}
		sort.Slice(changes, func(i, j int) bool { return keyOf(changes[i]) < keyOf(changes[j]) })
		point.Contributions = changes
		result = append(result, point)
		previous = next
	}
	return result
}
