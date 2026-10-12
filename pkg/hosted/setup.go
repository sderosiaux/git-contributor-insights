package hosted

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type appSettings struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	WebhookSecret string `json:"webhook_secret"`
	PEM           string `json:"pem,omitempty"`
}

// LoadGitHubSettings reads credentials created by the one-time manifest setup.
// Explicit environment settings take precedence.
func (c *Config) LoadGitHubSettings() error {
	b, err := os.ReadFile(filepath.Join(c.DataDir, "github-app.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var settings appSettings
	if err = json.Unmarshal(b, &settings); err != nil {
		return fmt.Errorf("invalid saved GitHub App settings")
	}
	if c.AppID == "" {
		c.AppID = strconv.FormatInt(settings.ID, 10)
	}
	if c.AppSlug == "" {
		c.AppSlug = settings.Slug
	}
	if c.ClientID == "" {
		c.ClientID = settings.ClientID
	}
	if c.ClientSecret == "" {
		c.ClientSecret = settings.ClientSecret
	}
	if c.WebhookSecret == "" {
		c.WebhookSecret = settings.WebhookSecret
	}
	if c.PrivateKeyFile == "" {
		c.PrivateKeyFile = filepath.Join(c.DataDir, "github-app.pem")
	}
	return nil
}
func (c Config) GitHubConfigured() bool {
	return c.AppID != "" && c.ClientID != "" && c.ClientSecret != "" && c.PrivateKeyFile != "" && c.WebhookSecret != ""
}

// Bootstrap hosts only a token-protected GitHub App registration page. No
// repository APIs exist until the configuration is complete and serve resumes.
func Bootstrap(ctx context.Context, c Config, out io.Writer) error {
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("bootstrap requires the final HTTPS public URL")
	}
	c.PublicURL = strings.TrimSuffix(c.PublicURL, "/")
	if !loginPattern.MatchString(c.Organization) || len(c.Admins) == 0 {
		return fmt.Errorf("bootstrap requires an organization and initial administrators")
	}
	if err = os.MkdirAll(c.DataDir, 0700); err != nil {
		return err
	}
	configFile := filepath.Join(c.DataDir, "github-app.json")
	if _, err = os.Stat(configFile); err == nil {
		return fmt.Errorf("GitHub App is already configured")
	}
	tokenFile := filepath.Join(c.DataDir, "setup-token")
	tokenBytes, err := os.ReadFile(tokenFile)
	if os.IsNotExist(err) {
		tokenBytes = []byte(randomToken())
		err = os.WriteFile(tokenFile, tokenBytes, 0600)
	}
	if err != nil {
		return err
	}
	token := string(tokenBytes)
	fmt.Fprintf(out, "One-time GitHub setup: %s/setup?token=%s\n", c.PublicURL, url.QueryEscape(token))
	g := &github{cfg: c, client: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if g.cfg.GitHubAPI == "" {
		g.cfg.GitHubAPI = "https://api.github.com"
	}
	done := make(chan error, 1)
	var mu sync.Mutex
	states := map[string]time.Time{}
	completing := false
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, map[string]string{"status": "setup"})
	})
	mux.HandleFunc("GET /setup", func(w http.ResponseWriter, r *http.Request) {
		if !constantEqual(r.URL.Query().Get("token"), token) {
			http.NotFound(w, r)
			return
		}
		state := randomToken()
		mu.Lock()
		for k, v := range states {
			if time.Now().After(v) {
				delete(states, k)
			}
		}
		if len(states) > 50 {
			mu.Unlock()
			http.Error(w, "Try again shortly", 429)
			return
		}
		states[state] = time.Now().Add(20 * time.Minute)
		mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "atlas_setup", Value: state, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 1200})
		manifest := map[string]any{"name": "Ownership Atlas " + c.Organization, "url": c.PublicURL, "redirect_url": c.PublicURL + "/setup/callback", "callback_urls": []string{c.PublicURL + "/auth/callback"}, "hook_attributes": map[string]any{"url": c.PublicURL + "/webhooks/github", "active": true}, "public": false, "default_permissions": map[string]string{"contents": "read", "metadata": "read", "members": "read", "emails": "read"}, "default_events": []string{"push"}, "description": "Internal Git ownership and declared AI assistance explorer. Read-only repository analysis."}
		b, _ := json.Marshal(manifest)
		page := `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Connect GitHub · atlas</title><style>body{font:16px system-ui;color:#252525;background:#fafaf8;margin:0}main{max-width:600px;margin:10vh auto;padding:32px}h1{font-size:40px;letter-spacing:-1.5px}p,li{line-height:1.7;color:#70706c}button{font:inherit;background:#252525;color:white;border:0;border-radius:8px;padding:15px 24px;cursor:pointer}small{display:block;margin-top:20px;color:#777}</style></head><body><main><p>ATLAS · {{.Org}} WORKSPACE</p><h1>Connect your GitHub organization.</h1><p>The server is ready. Create its GitHub App, then choose the repositories it can read.</p><ul><li>Read-only code and repository metadata</li><li>Organization membership and verified email checks</li><li>Automatic refresh after pushes</li></ul><p>Access is limited to {{.Org}} members{{if .Domain}} with a verified @{{.Domain}} email{{end}}. Initial administrators: {{.Admins}}.</p><form method="post" action="{{.Action}}"><input type="hidden" name="manifest" value="{{.Manifest}}"><button>Create GitHub App →</button></form><small>GitHub will show the requested permissions. Credentials are saved directly on this server. You won't need to copy secrets.</small></main></body></html>`
		t, _ := template.New("setup").Parse(page)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = t.Execute(w, map[string]string{"Org": c.Organization, "Domain": c.EmailDomain, "Admins": strings.Join(c.Admins, ", "), "Action": "https://github.com/organizations/" + c.Organization + "/settings/apps/new?state=" + state, "Manifest": string(b)})
	})
	mux.HandleFunc("GET /setup/callback", func(w http.ResponseWriter, r *http.Request) {
		state := r.URL.Query().Get("state")
		cookie, err := r.Cookie("atlas_setup")
		if err != nil || !constantEqual(cookie.Value, state) {
			http.Error(w, "Setup session expired. Open the original setup link again.", 400)
			return
		}
		mu.Lock()
		expires, ok := states[state]
		if !ok || time.Now().After(expires) || completing {
			mu.Unlock()
			http.Error(w, "Setup session expired", 400)
			return
		}
		delete(states, state)
		completing = true
		mu.Unlock()
		success := false
		defer func() {
			if !success {
				mu.Lock()
				completing = false
				mu.Unlock()
			}
		}()
		code := r.URL.Query().Get("code")
		if code == "" || len(code) > 200 || strings.ContainsAny(code, "/?#\\") {
			http.Error(w, "Invalid GitHub registration code", 400)
			return
		}
		var settings appSettings
		if err = g.request(r.Context(), "POST", "/app-manifests/"+url.PathEscape(code)+"/conversions", "", nil, &settings); err != nil {
			http.Error(w, "Unable to complete GitHub registration. Open the original setup link to retry.", 502)
			return
		}
		if settings.ID <= 0 || settings.ClientID == "" || settings.ClientSecret == "" || len(settings.WebhookSecret) < 32 || !loginPattern.MatchString(settings.Slug) || !strings.Contains(settings.PEM, "PRIVATE KEY") {
			http.Error(w, "GitHub returned incomplete app credentials", 502)
			return
		}
		if err = saveAppSettings(c.DataDir, settings); err != nil {
			http.Error(w, "Unable to save app credentials", 500)
			return
		}
		success = true
		_ = os.Remove(tokenFile)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		page := `<!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1"><title>Install GitHub App · atlas</title></head><body style="font:18px system-ui;max-width:600px;margin:12vh auto;padding:30px"><h1>GitHub connected.</h1><p>Install the app on your organization and select the repositories you want to analyze.</p><p><a href="https://github.com/apps/` + settings.Slug + `/installations/new">Choose repositories on GitHub →</a></p><p>Then <a href="/repositories">open your workspace</a> and sign in.</p></body></html>`
		_, _ = io.WriteString(w, page)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		done <- nil
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Workspace setup is in progress. Use the setup link from the server logs.", 503)
	})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Host, u.Host) {
			http.Error(w, "Unexpected host", 421)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action https://github.com; frame-ancestors 'none'; base-uri 'none'")
		mux.ServeHTTP(w, r)
	})
	server := &http.Server{Addr: c.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	errs := make(chan error, 1)
	go func() { errs <- server.ListenAndServe() }()
	select {
	case err = <-errs:
	case err = <-done:
	case <-ctx.Done():
		err = ctx.Err()
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
	return err
}
func constantEqual(a, b string) bool {
	if len(a) != 64 || len(b) != 64 {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
func saveAppSettings(dir string, s appSettings) error {
	// The JSON file is the commit point; a restart never loads half a setup.
	key := filepath.Join(dir, "github-app.pem")
	if err := os.WriteFile(key, []byte(s.PEM), 0600); err != nil {
		return err
	}
	s.PEM = ""
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "github-app.json.tmp")
	if err = os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "github-app.json"))
}
