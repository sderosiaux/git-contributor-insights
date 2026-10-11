package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	"github.com/sderosiaux/git-contributor-insights/pkg/explorer"
	"github.com/sderosiaux/git-contributor-insights/pkg/ownership"
	"github.com/spf13/cobra"
)

func init() {
	var exportOptions, exploreOptions ownership.Options
	var exportDatabase, exploreDatabase string
	export := &cobra.Command{
		Use: "ownership [repo-path]", Short: "Export complete indexed ownership history, affiliations and evidence as JSON",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var data *ownership.Dataset
			var err error
			if exportOptions.Snapshots > 0 || exportOptions.Interval != "auto" {
				data, err = ownership.Build(cmd.Context(), args[0], exportOptions)
			} else {
				database, pathErr := ownershipDatabase(args[0], exportDatabase)
				if pathErr != nil {
					return pathErr
				}
				index, openErr := ownership.OpenIndex(cmd.Context(), args[0], exportOptions, database)
				if openErr != nil {
					return openErr
				}
				defer index.Close()
				if err = index.Run(cmd.Context()); err == nil {
					data, err = index.Query(cmd.Context(), exportOptions.Since, exportOptions.Until, "")
				}
			}
			if err != nil {
				return err
			}
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			return encoder.Encode(data)
		},
	}
	addOwnershipFlags(export, &exportOptions)
	export.Flags().StringVar(&exportDatabase, "database", "", "Persistent SQLite index path (default: user cache directory)")
	export.Flags().StringVar(&exportOptions.Interval, "interval", "auto", "Legacy sampled export: auto, month, week, day, quarter, year")
	export.Flags().IntVar(&exportOptions.Snapshots, "snapshots", 0, "Legacy sampled export: an explicit fixed number of snapshots")
	var port int
	explore := &cobra.Command{
		Use: "explore [repo-path]", Short: "Explore ownership by person, organization, path and date in your browser",
		Args:    cobra.ExactArgs(1),
		Example: "  ghca explore ./repo --since 2020-01-01\n  ghca explore ./repo --config ownership.yaml --port 8787",
		RunE: func(cmd *cobra.Command, args []string) error {
			if port < 0 || port > 65535 {
				return fmt.Errorf("port must be between 0 and 65535")
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			database, err := ownershipDatabase(args[0], exploreDatabase)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "Opening the complete ownership index…")
			index, err := ownership.OpenIndex(ctx, args[0], exploreOptions, database)
			if err != nil {
				return err
			}
			var indexing sync.WaitGroup
			var runMu sync.Mutex
			running := false
			startIndex := func() {
				runMu.Lock()
				defer runMu.Unlock()
				if running || ctx.Err() != nil {
					return
				}
				running = true
				indexing.Add(1)
				go func() {
					defer indexing.Done()
					err := index.Run(ctx)
					runMu.Lock()
					running = false
					runMu.Unlock()
					if err != nil && ctx.Err() == nil {
						fmt.Fprintln(cmd.ErrOrStderr(), "Indexing stopped; completed commit states are saved:", err)
					} else if err == nil {
						status := index.Status()
						fmt.Fprintf(cmd.ErrOrStderr(), "Index ready · %d/%d commit states · %d reachable commits · %s\n", status.Completed, status.Total, status.Commits, status.Path)
					}
				}()
			}
			defer func() { stop(); indexing.Wait(); _ = index.Close() }()
			handler, err := explorer.NewIndexedHandler(index, exploreOptions.Since, exploreOptions.Until, startIndex)
			if err != nil {
				return err
			}
			listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				return err
			}
			defer listener.Close()
			server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
			fmt.Fprintf(cmd.OutOrStdout(), "Ownership atlas · every reachable commit\nOpen http://%s/overview\nSQLite index: %s\nIndexing progress is shown in the browser. Completed commit states survive restarts.\nPress Ctrl+C to stop.\n", listener.Addr(), database)
			startIndex()
			done := make(chan struct{})
			defer close(done)
			go func() {
				select {
				case <-ctx.Done():
					shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					_ = server.Shutdown(shutdown)
				case <-done:
				}
			}()
			if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	addOwnershipFlags(explore, &exploreOptions)
	explore.Flags().StringVar(&exploreDatabase, "database", "", "Persistent SQLite index path (default: user cache directory)")
	explore.Flags().IntVar(&port, "port", 8787, "Local HTTP port (0 picks an available port)")
	rootCmd.AddCommand(export, explore)
}

func addOwnershipFlags(cmd *cobra.Command, opts *ownership.Options) {
	cmd.Flags().StringVar(&opts.Since, "since", "", "First date in the default history view, YYYY-MM-DD (all commits are still indexed)")
	cmd.Flags().StringVar(&opts.Until, "until", "", "Last date in the default history view, YYYY-MM-DD inclusive (default: HEAD date)")
	cmd.Flags().StringVarP(&opts.ConfigPath, "config", "c", "", "YAML domains and dated person affiliations")
	cmd.Flags().IntVar(&opts.WindowDays, "window-days", 90, "Trailing window for additions + deletions at each snapshot")
	cmd.Flags().IntVarP(&opts.Workers, "workers", "w", 0, "Concurrent Git workers (0: automatic, up to 12)")
}

// Keep indexes outside analyzed repositories and isolate repositories by their
// canonical local path. HEAD and attribution settings are versioned inside SQL.
func ownershipDatabase(repo, configured string) (string, error) {
	if configured != "" {
		return filepath.Abs(configured)
	}
	absolute, err := filepath.Abs(repo)
	if err != nil {
		return "", err
	}
	if canonical, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil {
		absolute = canonical
	}
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(absolute))
	return filepath.Join(root, "git-contributor-insights", fmt.Sprintf("%x.sqlite", digest[:16])), nil
}
