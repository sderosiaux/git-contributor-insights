// Package hosted runs the authenticated, multi-repository ownership workspace.
package hosted

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	Listen, PublicURL, DataDir, Organization, EmailDomain                 string
	Admins                                                                []string
	AppID, ClientID, ClientSecret, PrivateKeyFile, AppSlug, WebhookSecret string
	Workers                                                               int
	// Development is intentionally restricted to a loopback listener and origin.
	Development          bool
	GitHubAPI, GitHubWeb string
}

func (c *Config) Validate() error {
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8789"
	}
	if c.Workers == 0 {
		c.Workers = 8
	}
	if c.Workers < 1 || c.Workers > 32 {
		return fmt.Errorf("workers must be between 1 and 32")
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("public URL must be an origin, for example https://atlas.example.com")
	}
	c.PublicURL = strings.TrimSuffix(c.PublicURL, "/")
	if c.Development {
		host, _, err := net.SplitHostPort(c.Listen)
		ip := net.ParseIP(host)
		if err != nil || ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("development mode must listen on a loopback IP")
		}
		ip = net.ParseIP(u.Hostname())
		if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("development URL must be loopback")
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("invalid development URL")
		}
	} else {
		if u.Scheme != "https" {
			return fmt.Errorf("hosted mode requires an HTTPS public URL")
		}
		if c.Organization == "" || len(c.Admins) == 0 {
			return fmt.Errorf("organization and initial administrators are required")
		}
		if c.AppID == "" || c.ClientID == "" || c.ClientSecret == "" || c.PrivateKeyFile == "" || len(c.WebhookSecret) < 32 {
			return fmt.Errorf("configure the GitHub App ID, client ID, client secret, private key file and a webhook secret of at least 32 characters")
		}
	}
	if c.DataDir == "" {
		return fmt.Errorf("data directory is required")
	}
	c.DataDir, err = filepath.Abs(c.DataDir)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(c.DataDir, 0700); err != nil {
		return err
	}
	if c.GitHubAPI == "" {
		c.GitHubAPI = "https://api.github.com"
	}
	if c.GitHubWeb == "" {
		c.GitHubWeb = "https://github.com"
	}
	if c.Organization != "" && !loginPattern.MatchString(c.Organization) {
		return fmt.Errorf("invalid GitHub organization")
	}
	for _, admin := range c.Admins {
		if !loginPattern.MatchString(admin) {
			return fmt.Errorf("invalid administrator username")
		}
	}
	if strings.ContainsAny(c.EmailDomain, "@/ \\\r\n") {
		return fmt.Errorf("invalid email domain")
	}
	c.Organization = strings.ToLower(c.Organization)
	c.EmailDomain = strings.ToLower(c.EmailDomain)
	return nil
}

const sessionLifetime = 8 * time.Hour
