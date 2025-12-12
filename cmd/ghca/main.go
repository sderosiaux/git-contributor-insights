package main

import (
	"fmt"
	"os"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/sderosiaux/ghca/pkg/analyzer"
	"github.com/sderosiaux/ghca/pkg/config"
	"github.com/sderosiaux/ghca/pkg/git"
	"github.com/sderosiaux/ghca/pkg/tui"
)

var (
	configPath string
	sinceDate  string
	untilDate  string
	workers    int
	breakdown  string

	rootCmd = &cobra.Command{
		Use:   "ghca",
		Short: "GitHub Contributor Analyzer - Analyze contributor patterns in repositories",
		Long: `GitHub Contributor Analyzer (ghca) is a fast tool to analyze Git repository
contributor patterns, identifying vendor vs community contributions.`,
	}

	analyzeCmd = &cobra.Command{
		Use:   "analyze [repo-path]",
		Short: "Analyze a Git repository's contributor patterns",
		Long: `Analyze a local Git repository to identify vendor vs community contributions.

Examples:
  ghca analyze /path/to/kafka --config vendors.yaml
  ghca analyze ./repo --since 2024-01-01 --until 2024-12-31
  ghca analyze /tmp/kafka --workers 8`,
		Args: cobra.ExactArgs(1),
		Run:  runAnalyze,
	}
)

func init() {
	analyzeCmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to vendor configuration YAML file")
	analyzeCmd.Flags().StringVar(&sinceDate, "since", "", "Only analyze commits since this date (YYYY-MM-DD)")
	analyzeCmd.Flags().StringVar(&untilDate, "until", "", "Only analyze commits until this date (YYYY-MM-DD)")
	analyzeCmd.Flags().IntVarP(&workers, "workers", "w", 8, "Number of concurrent workers (default: 8)")
	analyzeCmd.Flags().StringVarP(&breakdown, "breakdown", "b", "", "Time breakdown: year, quarter, month, week (e.g., --breakdown year)")

	rootCmd.AddCommand(analyzeCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runAnalyze(cmd *cobra.Command, args []string) {
	repoPath := args[0]

	// Styles
	cyan := lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	green := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	yellow := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	fmt.Println(cyan.Bold(true).Render("GitHub Contributor Analyzer v1.0.0 (Go)"))
	fmt.Println(dim.Render("Mode: Local Git repository (high-performance)"))
	fmt.Println()

	// Load configuration
	var cfg *config.Config
	var err error

	if configPath != "" {
		cfg, err = config.Load(configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
			os.Exit(1)
		}

		vendors := cfg.GetVendorNames()
		fmt.Println(green.Render("✓") + " Loaded vendor config: " + joinStrings(vendors, ", "))
	} else {
		// Create empty config (will use automatic domain classification)
		cfg = &config.Config{
			Vendors: make(map[string]config.VendorConfig),
		}
		fmt.Println(yellow.Render("ℹ") + " No vendor config - using automatic domain classification")
		fmt.Println(dim.Render("  Personal emails (gmail, yahoo, etc.) → 'community'"))
		fmt.Println(dim.Render("  Corporate emails → '@domain' (e.g., '@confluent.io', '@amazon.com')"))
		fmt.Println(dim.Render("  Use --config to specify custom vendor identification rules"))
	}

	fmt.Println()

	// Parse date filters
	var since, until *time.Time

	if sinceDate != "" {
		t, err := time.Parse("2006-01-02", sinceDate)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid --since date format: %v\n", err)
			os.Exit(1)
		}
		since = &t
	}

	if untilDate != "" {
		t, err := time.Parse("2006-01-02", untilDate)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid --until date format: %v\n", err)
			os.Exit(1)
		}
		until = &t
	}

	// Open repository
	fmt.Println(cyan.Render("Opening local repository: ") + repoPath)
	fetcher, err := git.NewFetcher(repoPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening repository: %v\n", err)
		os.Exit(1)
	}

	repoName := fetcher.GetRepoName()
	fmt.Println(green.Render("✓") + " Repository: " + repoName)
	fmt.Println()

	// Fetch commits with spinner and progress
	spinner := tui.NewSpinner(os.Stdout, "Analyzing Git history...")
	spinner.Start()
	startTime := time.Now()

	// Progress callback to update spinner
	progressCallback := func(processed, total int) {
		spinner.UpdateProgress("Analyzing Git history...", processed, total)
	}

	commits, err := fetcher.FetchCommits(since, until, workers, progressCallback)

	spinner.Stop()

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching commits: %v\n", err)
		os.Exit(1)
	}

	elapsed := time.Since(startTime)
	fmt.Printf("%s Processed %s commits in %s (%.0f commits/sec)\n",
		green.Render("✓"),
		analyzer.FormatNumber(len(commits)),
		elapsed.Round(time.Millisecond),
		float64(len(commits))/elapsed.Seconds(),
	)
	fmt.Println()

	if len(commits) == 0 {
		fmt.Println(yellow.Render("No commits found in the specified date range"))
		return
	}

	// Fetch contributors
	contributors, err := fetcher.FetchContributors()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching contributors: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("%s Found %s unique contributors\n",
		green.Render("✓"),
		analyzer.FormatNumber(len(contributors)),
	)
	fmt.Println()

	// Analyze with spinner
	spinner = tui.NewSpinner(os.Stdout, "Computing metrics...")
	spinner.Start()

	if breakdown != "" {
		// Validate breakdown type
		validBreakdowns := map[string]bool{"year": true, "quarter": true, "month": true, "week": true}
		if !validBreakdowns[breakdown] {
			spinner.Stop()
			fmt.Fprintf(os.Stderr, "Invalid breakdown type: %s (must be: year, quarter, month, week)\n", breakdown)
			os.Exit(1)
		}

		// Timeline analysis
		timeline := analyzer.AnalyzeTimeline(commits, cfg, repoName, breakdown)
		spinner.Stop()
		fmt.Println(green.Render("✓") + " Timeline analysis complete")
		fmt.Println()

		// Display timeline
		display := tui.NewTimeline(timeline)
		fmt.Println(display.Render())
	} else {
		// Standard analysis
		an := analyzer.New(cfg)
		analysis := an.Analyze(commits, contributors, repoName)
		spinner.Stop()

		fmt.Println(green.Render("✓") + " Analysis complete")
		fmt.Println()

		// Display results
		display := tui.New(analysis)
		fmt.Println(display.Render())
	}

	fmt.Println()
	fmt.Println(dim.Render("Powered by ghca (Go) - https://github.com/sderosiaux/ghca"))
}

func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += sep + strs[i]
	}
	return result
}
