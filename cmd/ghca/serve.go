package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sderosiaux/git-contributor-insights/pkg/hosted"
	"github.com/sderosiaux/git-contributor-insights/pkg/ownership"
	"github.com/spf13/cobra"
)

func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func init() {
	var c hosted.Config
	var bootstrap bool
	serve := &cobra.Command{Use: "serve", Short: "Run an authenticated multi-repository workspace", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		c.AppID = os.Getenv("ATLAS_GITHUB_APP_ID")
		c.ClientID = os.Getenv("ATLAS_GITHUB_CLIENT_ID")
		c.ClientSecret = os.Getenv("ATLAS_GITHUB_CLIENT_SECRET")
		c.PrivateKeyFile = os.Getenv("ATLAS_GITHUB_PRIVATE_KEY_FILE")
		c.AppSlug = os.Getenv("ATLAS_GITHUB_APP_SLUG")
		c.WebhookSecret = os.Getenv("ATLAS_WEBHOOK_SECRET")
		for _, a := range strings.Split(os.Getenv("ATLAS_ADMINS"), ",") {
			if a = strings.TrimSpace(a); a != "" {
				c.Admins = append(c.Admins, a)
			}
		}
		if c.Development && len(c.Admins) == 0 {
			c.Admins = []string{"developer"}
		}
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if err := c.LoadGitHubSettings(); err != nil {
			return err
		}
		if bootstrap && !c.Development && !c.GitHubConfigured() {
			if err := hosted.Bootstrap(ctx, c, cmd.OutOrStdout()); err != nil {
				return err
			}
			if err := c.LoadGitHubSettings(); err != nil {
				return err
			}
		}
		app, err := hosted.New(ctx, c)
		if err != nil {
			return err
		}
		defer app.Close()
		listener, err := net.Listen("tcp", c.Listen)
		if err != nil {
			return err
		}
		defer listener.Close()
		server := &http.Server{Handler: app, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-ctx.Done():
				shutdown, stop := context.WithTimeout(context.Background(), 15*time.Second)
				defer stop()
				_ = server.Shutdown(shutdown)
			case <-done:
			}
		}()
		fmt.Fprintf(cmd.OutOrStdout(), "Ownership atlas workspace · %s\nPersistent data: %s\n", c.PublicURL, c.DataDir)
		if c.Development {
			fmt.Fprintln(cmd.OutOrStdout(), "Development mode: loopback only, authentication disabled.")
		}
		if err = server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}}
	f := serve.Flags()
	f.BoolVar(&bootstrap, "bootstrap", false, "Offer a one-time token-protected GitHub App setup when credentials are absent")
	f.StringVar(&c.Listen, "listen", envDefault("ATLAS_LISTEN", "127.0.0.1:8789"), "HTTP listen address (behind HTTPS proxy in production)")
	f.StringVar(&c.PublicURL, "public-url", os.Getenv("ATLAS_PUBLIC_URL"), "Canonical HTTPS origin")
	f.StringVar(&c.DataDir, "data-dir", os.Getenv("ATLAS_DATA_DIR"), "Private persistent directory for managed repos, indexes and sessions")
	f.StringVar(&c.Organization, "organization", os.Getenv("ATLAS_ORGANIZATION"), "Allowed GitHub organization")
	f.StringVar(&c.EmailDomain, "email-domain", os.Getenv("ATLAS_EMAIL_DOMAIN"), "Require a verified GitHub email in this domain")
	f.IntVar(&c.Workers, "workers", 8, "Global indexing concurrency (one background repo job at a time)")
	f.BoolVar(&c.Development, "dev", false, "Disable login for loopback-only development")
	var opts ownership.Options
	var database string
	worker := &cobra.Command{Use: "index-worker REPO", Hidden: true, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		idx, err := ownership.OpenIndex(ctx, args[0], opts, database)
		if err != nil {
			return err
		}
		defer idx.Close()
		result := make(chan error, 1)
		go func() { result <- idx.Run(ctx) }()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		enc := json.NewEncoder(cmd.OutOrStdout())
		for {
			select {
			case err := <-result:
				_ = enc.Encode(idx.Status())
				return err
			case <-ticker.C:
				_ = enc.Encode(idx.Status())
			}
		}
	}}
	worker.Flags().StringVar(&opts.Revision, "revision", "", "Exact commit SHA")
	worker.Flags().StringVar(&database, "database", "", "Index database")
	worker.Flags().StringVar(&opts.ConfigPath, "config", "", "Affiliation rules")
	worker.Flags().IntVar(&opts.Workers, "workers", 8, "Git workers")
	rootCmd.AddCommand(serve, worker)
}
