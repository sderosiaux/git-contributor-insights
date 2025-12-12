package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/sderosiaux/ghca/pkg/analyzer"
)

// TimelineDisplay renders timeline analysis
type TimelineDisplay struct {
	timeline *analyzer.TimelineAnalysis
	colors   map[string]lipgloss.Color
}

// NewTimeline creates a new TimelineDisplay
func NewTimeline(timeline *analyzer.TimelineAnalysis) *TimelineDisplay {
	d := &TimelineDisplay{
		timeline: timeline,
		colors:   make(map[string]lipgloss.Color),
	}
	d.assignColors()
	return d
}

// assignColors assigns colors to vendors
func (d *TimelineDisplay) assignColors() {
	colors := []lipgloss.Color{
		colorRed, colorBlue, colorGreen, colorYellow,
		colorMagenta, colorCyan,
	}

	// Community gets special color
	d.colors["community"] = lipgloss.Color("7") // white

	// Others gets dim color
	d.colors["others"] = lipgloss.Color("240") // dim gray

	// Assign colors to other vendors (collect all vendors across all periods)
	vendorSet := make(map[string]bool)
	for _, period := range d.timeline.Periods {
		for vendor := range period.VendorMetrics {
			if vendor != "community" {
				vendorSet[vendor] = true
			}
		}
	}

	i := 0
	for vendor := range vendorSet {
		d.colors[vendor] = colors[i%len(colors)]
		i++
	}
}

// Render renders the timeline analysis
func (d *TimelineDisplay) Render() string {
	var out strings.Builder

	out.WriteString(d.renderHeader())
	out.WriteString("\n\n")
	out.WriteString(d.renderTimelineTable())
	out.WriteString("\n\n")
	out.WriteString(d.renderTrendSummary())

	return out.String()
}

// renderHeader renders the timeline header
func (d *TimelineDisplay) renderHeader() string {
	breakdownLabels := map[string]string{
		"year":    "Year-over-Year",
		"quarter": "Quarter-over-Quarter",
		"month":   "Month-over-Month",
		"week":    "Week-over-Week",
	}

	content := fmt.Sprintf(`%s
%s

📊 Total Periods: %s
📅 Date Range: %s to %s`,
		titleStyle.Render(d.timeline.RepoName),
		dimStyle.Render(breakdownLabels[d.timeline.Breakdown]),
		successStyle.Bold(true).Render(analyzer.FormatNumber(len(d.timeline.Periods))),
		d.timeline.DateRange.Start.Format("2006-01-02"),
		d.timeline.DateRange.End.Format("2006-01-02"),
	)

	return boxStyle.Render(content)
}

// renderTimelineTable renders the period-by-period breakdown
func (d *TimelineDisplay) renderTimelineTable() string {
	var out strings.Builder

	out.WriteString(headerStyle.Render("Timeline Breakdown"))
	out.WriteString("\n\n")

	// Get all unique vendors across all periods
	vendorSet := make(map[string]bool)
	for _, period := range d.timeline.Periods {
		for vendor := range period.VendorMetrics {
			vendorSet[vendor] = true
		}
	}

	// Determine which vendors to show
	const maxVendorsToShow = 5 // Show top 4 + community, or group extras as "others"
	vendors := d.getVendorsToDisplay(vendorSet, maxVendorsToShow)

	// Header
	out.WriteString(fmt.Sprintf("%-15s %10s", "Period", "Total"))
	for _, vendor := range vendors {
		out.WriteString(fmt.Sprintf("  %12s", vendor))
	}
	out.WriteString("\n")

	out.WriteString(strings.Repeat("─", 15+12+len(vendors)*14))
	out.WriteString("\n")

	// Render each period
	for _, period := range d.timeline.Periods {
		out.WriteString(fmt.Sprintf("%-15s %10s",
			period.Period,
			analyzer.FormatNumber(period.TotalCommits),
		))

		for _, vendor := range vendors {
			var commits int
			var pct float64

			if vendor == "others" {
				// Sum all non-shown vendors
				commits, pct = d.calculateOthersMetrics(period, vendors)
			} else {
				metrics, ok := period.VendorMetrics[vendor]
				if !ok || metrics.TotalCommits == 0 {
					out.WriteString(fmt.Sprintf("  %12s", "-"))
					continue
				}
				commits = metrics.TotalCommits
				pct = period.GetVendorPercentage(vendor, "commits")
			}

			if commits == 0 {
				out.WriteString(fmt.Sprintf("  %12s", "-"))
				continue
			}

			vendorStyle := lipgloss.NewStyle().Foreground(d.colors[vendor])

			// Format: "1,234 (45%)"
			display := fmt.Sprintf("%s (%.0f%%)",
				analyzer.FormatNumber(commits),
				pct,
			)
			out.WriteString(fmt.Sprintf("  %s", vendorStyle.Render(fmt.Sprintf("%12s", display))))
		}
		out.WriteString("\n")
	}

	return out.String()
}

// getVendorsToDisplay returns vendors to show (with grouping if needed)
func (d *TimelineDisplay) getVendorsToDisplay(vendorSet map[string]bool, maxVendors int) []string {
	// Always show community first
	vendors := make([]string, 0)
	if vendorSet["community"] {
		vendors = append(vendors, "community")
	}

	// Get other vendors sorted by total commits across all periods
	otherVendors := make(map[string]int)
	for vendor := range vendorSet {
		if vendor != "community" {
			totalCommits := 0
			for _, period := range d.timeline.Periods {
				if metrics, ok := period.VendorMetrics[vendor]; ok {
					totalCommits += metrics.TotalCommits
				}
			}
			otherVendors[vendor] = totalCommits
		}
	}

	// Sort by commit count
	vendorNames := make([]string, 0, len(otherVendors))
	for name := range otherVendors {
		vendorNames = append(vendorNames, name)
	}
	sort.Slice(vendorNames, func(i, j int) bool {
		return otherVendors[vendorNames[i]] > otherVendors[vendorNames[j]]
	})

	// Show top vendors, group rest as "others"
	availableSlots := maxVendors - len(vendors) // Subtract community
	if len(vendorNames) <= availableSlots {
		// Show all
		vendors = append(vendors, vendorNames...)
	} else {
		// Show top N-1 and group rest as "others"
		showIndividually := availableSlots - 1
		vendors = append(vendors, vendorNames[:showIndividually]...)
		vendors = append(vendors, "others")
	}

	return vendors
}

// calculateOthersMetrics sums metrics for vendors not individually shown
func (d *TimelineDisplay) calculateOthersMetrics(period *analyzer.TimeBreakdown, shownVendors []string) (int, float64) {
	shownSet := make(map[string]bool)
	for _, v := range shownVendors {
		shownSet[v] = true
	}

	totalCommits := 0
	for vendor, metrics := range period.VendorMetrics {
		if !shownSet[vendor] && vendor != "community" {
			totalCommits += metrics.TotalCommits
		}
	}

	pct := 0.0
	if period.TotalCommits > 0 {
		pct = (float64(totalCommits) / float64(period.TotalCommits)) * 100
	}

	return totalCommits, pct
}

// renderTrendSummary shows key trends
func (d *TimelineDisplay) renderTrendSummary() string {
	var out strings.Builder

	out.WriteString(headerStyle.Render("Key Trends"))
	out.WriteString("\n\n")

	if len(d.timeline.Periods) == 0 {
		out.WriteString("No data available\n")
		return out.String()
	}

	// Compare first and last period
	firstPeriod := d.timeline.Periods[0]
	lastPeriod := d.timeline.Periods[len(d.timeline.Periods)-1]

	out.WriteString(fmt.Sprintf("📈 Period Range: %s → %s\n",
		successStyle.Render(firstPeriod.Period),
		successStyle.Render(lastPeriod.Period),
	))

	// Total commits trend
	commitsChange := lastPeriod.TotalCommits - firstPeriod.TotalCommits
	commitsChangeStr := fmt.Sprintf("%+d", commitsChange)
	if commitsChange > 0 {
		commitsChangeStr = successStyle.Render(commitsChangeStr)
	} else if commitsChange < 0 {
		commitsChangeStr = lipgloss.NewStyle().Foreground(colorRed).Render(commitsChangeStr)
	}
	out.WriteString(fmt.Sprintf("📊 Commits: %s → %s (%s)\n",
		analyzer.FormatNumber(firstPeriod.TotalCommits),
		analyzer.FormatNumber(lastPeriod.TotalCommits),
		commitsChangeStr,
	))

	// Vendor trends (show community and top vendor)
	out.WriteString("\n")

	if metrics, ok := firstPeriod.VendorMetrics["community"]; ok {
		firstPct := firstPeriod.GetVendorPercentage("community", "commits")
		lastPct := lastPeriod.GetVendorPercentage("community", "commits")
		pctChange := lastPct - firstPct

		changeSymbol := "→"
		if pctChange > 0 {
			changeSymbol = "↗"
		} else if pctChange < 0 {
			changeSymbol = "↘"
		}

		out.WriteString(fmt.Sprintf("🌍 Community: %.1f%% → %.1f%% %s\n",
			firstPct, lastPct, changeSymbol,
		))

		if len(metrics.UniqueContributors) > 0 {
			firstContribs := len(firstPeriod.VendorMetrics["community"].UniqueContributors)
			lastContribs := len(lastPeriod.VendorMetrics["community"].UniqueContributors)
			out.WriteString(fmt.Sprintf("   Contributors: %d → %d\n",
				firstContribs, lastContribs,
			))
		}
	}

	return out.String()
}
