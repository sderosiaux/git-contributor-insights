package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/sderosiaux/git-contributor-insights/pkg/analyzer"
	"github.com/sderosiaux/git-contributor-insights/pkg/types"
)

var (
	// Color scheme
	colorCyan    = lipgloss.Color("14")
	colorGreen   = lipgloss.Color("10")
	colorYellow  = lipgloss.Color("11")
	colorRed     = lipgloss.Color("9")
	colorBlue    = lipgloss.Color("12")
	colorMagenta = lipgloss.Color("13")

	// Styles
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorCyan).
			MarginBottom(1)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorMagenta)

	successStyle = lipgloss.NewStyle().
			Foreground(colorGreen)

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	boxStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(colorCyan).
			Padding(1, 2)
)

// Display renders the complete analysis to terminal
type Display struct {
	analysis *types.RepositoryAnalysis
	colors   map[string]lipgloss.Color
}

// New creates a new Display
func New(analysis *types.RepositoryAnalysis) *Display {
	d := &Display{
		analysis: analysis,
		colors:   make(map[string]lipgloss.Color),
	}
	d.assignColors()
	return d
}

// assignColors assigns colors to vendors
func (d *Display) assignColors() {
	colors := []lipgloss.Color{
		colorRed, colorBlue, colorGreen, colorYellow,
		colorMagenta, colorCyan,
	}

	// Community gets special color
	d.colors["community"] = lipgloss.Color("7") // white

	// Assign colors to other vendors
	i := 0
	for vendor := range d.analysis.VendorMetrics {
		if vendor != "community" {
			d.colors[vendor] = colors[i%len(colors)]
			i++
		}
	}
}

// Render renders the complete analysis
func (d *Display) Render() string {
	var out strings.Builder

	out.WriteString(d.renderHeader())
	out.WriteString("\n\n")
	out.WriteString(d.renderSummaryTable())
	out.WriteString("\n\n")
	out.WriteString(d.renderBarChart("commits"))
	out.WriteString("\n\n")
	out.WriteString(d.renderBarChart("additions"))
	out.WriteString("\n\n")
	out.WriteString(d.renderBarChart("contributors"))
	out.WriteString("\n\n")
	out.WriteString(d.renderInsights())

	return out.String()
}

// renderHeader renders the analysis header
func (d *Display) renderHeader() string {
	content := fmt.Sprintf(`%s

📊 Total Commits: %s
👥 Total Contributors: %s
📅 Date Range: %s to %s`,
		titleStyle.Render(d.analysis.RepoName),
		successStyle.Bold(true).Render(analyzer.FormatNumber(d.analysis.TotalCommits)),
		successStyle.Bold(true).Render(analyzer.FormatNumber(d.analysis.TotalContributors)),
		d.analysis.DateRange.Start.Format("2006-01-02"),
		d.analysis.DateRange.End.Format("2006-01-02"),
	)

	return boxStyle.Render(content)
}

// renderSummaryTable renders the vendor/community breakdown table
func (d *Display) renderSummaryTable() string {
	var out strings.Builder

	out.WriteString(headerStyle.Render("Vendor/Community Breakdown"))
	out.WriteString("\n\n")

	// Header with proper alignment
	out.WriteString(fmt.Sprintf("%-18s %10s  %10s  %14s  %16s  %15s  %15s  %13s\n",
		"Category", "Commits", "% Commits", "Contributors", "% Contributors",
		"Lines Added", "Lines Deleted", "Net Change"))

	out.WriteString(strings.Repeat("─", 132))
	out.WriteString("\n")

	// Show all vendors sorted by commits (hide vendors with 0 commits)
	vendors := analyzer.GetSortedVendors(d.analysis, "commits", true)
	groups := make([]*analyzer.VendorGroup, 0, len(vendors))
	for _, vendor := range vendors {
		metrics := d.analysis.VendorMetrics[vendor]
		// Skip vendors with no commits
		if metrics.TotalCommits == 0 {
			continue
		}
		groups = append(groups, &analyzer.VendorGroup{
			Name:               vendor,
			TotalCommits:       metrics.TotalCommits,
			TotalAdditions:     metrics.TotalAdditions,
			TotalDeletions:     metrics.TotalDeletions,
			UniqueContributors: metrics.UniqueContributors,
			IsGrouped:          false,
		})
	}

	for _, group := range groups {
		commitsPct := d.calculatePercentage(group.TotalCommits, d.analysis.TotalCommits)
		contributorsPct := d.calculatePercentage(group.ContributorCount(), d.analysis.TotalContributors)

		// Format the row data first (no styling for alignment)
		commitsStr := analyzer.FormatNumber(group.TotalCommits)
		commitsPctStr := fmt.Sprintf("%.1f%%", commitsPct)
		contributorsStr := analyzer.FormatNumber(group.ContributorCount())
		contributorsPctStr := fmt.Sprintf("%.1f%%", contributorsPct)
		additionsStr := "+" + analyzer.FormatNumber(group.TotalAdditions)
		deletionsStr := "-" + analyzer.FormatNumber(group.TotalDeletions)
		netChangeStr := analyzer.FormatNumber(group.NetChanges())

		// Style the vendor name after calculating padding
		vendorStyle := lipgloss.NewStyle().Foreground(d.colors[group.Name])
		paddedVendor := fmt.Sprintf("%-18s", group.Name)
		styledVendor := vendorStyle.Render(paddedVendor)

		out.WriteString(fmt.Sprintf("%s %10s  %10s  %14s  %16s  %15s  %15s  %13s\n",
			styledVendor,
			commitsStr,
			commitsPctStr,
			contributorsStr,
			contributorsPctStr,
			additionsStr,
			deletionsStr,
			netChangeStr,
		))
	}

	return out.String()
}

// calculatePercentage calculates percentage
func (d *Display) calculatePercentage(value int, total int) float64 {
	if total == 0 {
		return 0
	}
	return (float64(value) / float64(total)) * 100
}

// renderBarChart renders ASCII bar chart for a metric
func (d *Display) renderBarChart(metric string) string {
	var out strings.Builder

	metricLabels := map[string]string{
		"commits":      "Commits Distribution",
		"additions":    "Lines Added Distribution",
		"contributors": "Contributors Distribution",
	}

	out.WriteString(headerStyle.Render(metricLabels[metric]))
	out.WriteString("\n\n")

	vendors := analyzer.GetSortedVendors(d.analysis, metric, true)

	// Get values and max
	values := make([]int, len(vendors))
	maxValue := 0

	for i, vendor := range vendors {
		metrics := d.analysis.VendorMetrics[vendor]
		var value int

		switch metric {
		case "commits":
			value = metrics.TotalCommits
		case "additions":
			value = metrics.TotalAdditions
		case "contributors":
			value = metrics.ContributorCount()
		}

		values[i] = value
		if value > maxValue {
			maxValue = value
		}
	}

	// Render bars
	chartWidth := 50
	for i, vendor := range vendors {
		value := values[i]

		// Calculate bar length
		barLength := 0
		if maxValue > 0 {
			barLength = (value * chartWidth) / maxValue
		}

		// Create bar
		vendorStyle := lipgloss.NewStyle().Foreground(d.colors[vendor])
		bar := strings.Repeat("█", barLength)

		out.WriteString(fmt.Sprintf("%-20s %s %8s\n",
			vendor+"...............",
			vendorStyle.Render(bar),
			analyzer.FormatNumber(value),
		))
	}

	return out.String()
}

// renderInsights renders key insights
func (d *Display) renderInsights() string {
	var out strings.Builder

	out.WriteString(headerStyle.Render("Key Insights"))
	out.WriteString("\n\n")

	// Top contributor by commits
	vendors := analyzer.GetSortedVendors(d.analysis, "commits", true)
	if len(vendors) > 0 {
		topVendor := vendors[0]
		topMetrics := d.analysis.VendorMetrics[topVendor]
		commitsPct := d.analysis.GetVendorPercentage(topVendor, "commits")

		out.WriteString(fmt.Sprintf("🏆 %s leads with %.1f%% of commits (%s commits)\n",
			successStyle.Render(topVendor),
			commitsPct,
			analyzer.FormatNumber(topMetrics.TotalCommits),
		))
	}

	// Community contribution
	if metrics, ok := d.analysis.VendorMetrics["community"]; ok {
		commPct := d.analysis.GetVendorPercentage("community", "commits")
		out.WriteString(fmt.Sprintf("🌍 Community contributes %.1f%% of commits with %s contributors\n",
			commPct,
			analyzer.FormatNumber(metrics.ContributorCount()),
		))
	}

	// Average commit size
	totalChanges := 0
	for _, metrics := range d.analysis.VendorMetrics {
		totalChanges += metrics.TotalAdditions + metrics.TotalDeletions
	}

	avgSize := 0
	if d.analysis.TotalCommits > 0 {
		avgSize = totalChanges / d.analysis.TotalCommits
	}

	out.WriteString(fmt.Sprintf("📏 Average commit size: %s lines changed\n",
		analyzer.FormatNumber(avgSize),
	))

	return out.String()
}

// GetMonthsSorted returns sorted list of months from timeline
func GetMonthsSorted(timeline map[string]map[string]int) []string {
	months := make([]string, 0, len(timeline))
	for month := range timeline {
		months = append(months, month)
	}
	sort.Strings(months)
	return months
}
