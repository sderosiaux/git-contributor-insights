package hosted

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed assets/*
var assets embed.FS

type Server struct {
	cfg     Config
	store   *store
	github  *github
	manager *manager
	handler http.Handler
	cancel  context.CancelFunc
	oauthMu sync.Mutex
	oauth   map[string]oauthState
}

func New(ctx context.Context, c Config) (*Server, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	g, err := newGitHub(c)
	if err != nil {
		return nil, err
	}
	st, err := openStore(c.DataDir)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Server{cfg: c, store: st, github: g, cancel: cancel, oauth: map[string]oauthState{}}
	m, err := newManager(ctx, c, st, g)
	if err != nil {
		cancel()
		st.db.Close()
		return nil, err
	}
	s.manager = m
	// Managed repos are read without checking out or executing their contents.
	// The workspace attribution rule is kept outside the public source checkout.
	affiliation := m.affiliations()
	if _, err = os.Stat(affiliation); os.IsNotExist(err) {
		contents := "vendors: {}\n"
		if c.EmailDomain != "" {
			contents = fmt.Sprintf("vendors:\n  %q:\n    domains: [%q]\n", c.Organization, c.EmailDomain)
		}
		if err = os.WriteFile(affiliation, []byte(contents), 0600); err != nil {
			cancel()
			st.db.Close()
			return nil, err
		}
	}
	s.routes()
	m.start()
	return s, nil
}
func (s *Server) Close() error { s.cancel(); s.manager.close(); return s.store.db.Close() }
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	origin, _ := url.Parse(s.cfg.PublicURL)
	if !strings.EqualFold(r.Host, origin.Host) {
		http.Error(w, "Unexpected host", 421)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self' https://github.com")
	s.handler.ServeHTTP(w, r)
}
func (s *Server) routes() {
	mux := http.NewServeMux()
	static, _ := fs.Sub(assets, "assets")
	mux.Handle("GET /hosted/", http.StripPrefix("/hosted/", http.FileServer(http.FS(static))))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { jsonResponse(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("GET /auth/github", s.login)
	mux.HandleFunc("GET /auth/callback", s.callback)
	mux.HandleFunc("POST /webhooks/github", s.webhook)
	secured := http.NewServeMux()
	secured.HandleFunc("GET /api/session", func(w http.ResponseWriter, r *http.Request) {
		login, _ := r.Context().Value(userKey{}).(string)
		jsonResponse(w, 200, map[string]any{"login": login, "admin": s.isAdmin(login), "organization": s.cfg.Organization, "emailDomain": s.cfg.EmailDomain, "development": s.cfg.Development, "installURL": s.installURL()})
	})
	secured.HandleFunc("POST /auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if !s.checkMutation(w, r) {
			return
		}
		if c, e := r.Cookie("atlas_session"); e == nil {
			s.store.logout(c.Value)
		}
		s.cookie(w, "atlas_session", "", -time.Second)
		jsonResponse(w, 200, map[string]bool{"ok": true})
	})
	secured.HandleFunc("GET /api/repositories", s.repositories)
	secured.HandleFunc("POST /api/repositories", s.addRepository)
	secured.HandleFunc("POST /api/repositories/{id}/refresh", func(w http.ResponseWriter, r *http.Request) {
		if !s.checkMutation(w, r) || !s.requireAdmin(w, r) {
			return
		}
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err := s.manager.queue(id); err != nil {
			jsonError(w, 400, err.Error())
			return
		}
		jsonResponse(w, 202, map[string]bool{"queued": true})
	})
	secured.HandleFunc("PATCH /api/repositories/{id}", s.updateRepository)
	secured.HandleFunc("DELETE /api/repositories/{id}", s.archiveRepository)
	secured.HandleFunc("GET /api/github/repositories", func(w http.ResponseWriter, r *http.Request) {
		if !s.requireAdmin(w, r) {
			return
		}
		repos, err := s.github.repositories(r.Context())
		if err != nil {
			jsonError(w, 502, err.Error())
			return
		}
		jsonResponse(w, 200, repos)
	})
	secured.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		if !s.requireAdmin(w, r) {
			return
		}
		jsonResponse(w, 200, map[string]any{"admins": s.store.admins(), "initialAdmins": s.cfg.Admins, "organization": s.cfg.Organization, "emailDomain": s.cfg.EmailDomain, "workers": s.cfg.Workers, "installURL": s.installURL()})
	})
	secured.HandleFunc("PUT /api/settings/admins", func(w http.ResponseWriter, r *http.Request) {
		if !s.checkMutation(w, r) || !s.requireAdmin(w, r) {
			return
		}
		var body struct {
			Admins []string `json:"admins"`
		}
		if decode(w, r, &body) != nil || len(body.Admins) > 50 {
			jsonError(w, 400, "Provide up to 50 GitHub usernames")
			return
		}
		for _, a := range body.Admins {
			if !loginPattern.MatchString(a) {
				jsonError(w, 400, "Invalid GitHub username")
				return
			}
		}
		if s.store.saveAdmins(body.Admins) != nil {
			jsonError(w, 500, "Unable to save administrators")
			return
		}
		jsonResponse(w, 200, map[string]bool{"ok": true})
	})
	secured.HandleFunc("GET /r/{owner}/{repo}/{rest...}", s.repositoryView)
	secured.HandleFunc("POST /r/{owner}/{repo}/api/index/retry", func(w http.ResponseWriter, r *http.Request) {
		if !s.checkMutation(w, r) || !s.requireAdmin(w, r) {
			return
		}
		name := strings.ToLower(r.PathValue("owner") + "/" + r.PathValue("repo"))
		s.manager.mu.Lock()
		var id int64
		for _, rr := range s.manager.repos {
			if rr.Name == name && !rr.Archived {
				id = rr.ID
				break
			}
		}
		s.manager.mu.Unlock()
		if err := s.manager.queue(id); err != nil {
			jsonError(w, 404, "Repository unavailable")
			return
		}
		jsonResponse(w, 202, map[string]bool{"queued": true})
	})
	secured.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			http.Redirect(w, r, "/repositories", 303)
		case "/repositories", "/settings":
			b, _ := assets.ReadFile("assets/workspace.html")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(b)
		default:
			http.NotFound(w, r)
		}
	})
	mux.Handle("/", s.authenticate(secured))
	s.handler = mux
}
func (s *Server) installURL() string {
	if s.cfg.AppSlug == "" {
		return "https://github.com/organizations/" + url.PathEscape(s.cfg.Organization) + "/settings/installations"
	}
	return "https://github.com/apps/" + url.PathEscape(s.cfg.AppSlug) + "/installations/new"
}
func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Development {
		http.Redirect(w, r, "/repositories", 303)
		return
	}
	b, _ := assets.ReadFile("assets/login.html")
	t, err := template.New("login").Parse(string(b))
	if err != nil {
		http.Error(w, "Login unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = t.Execute(w, map[string]string{"Organization": s.cfg.Organization, "Domain": s.cfg.EmailDomain, "Next": safeNext(r.URL.Query().Get("next"))})
}
func (s *Server) repositories(w http.ResponseWriter, r *http.Request) {
	m := s.manager
	m.mu.Lock()
	out := []map[string]any{}
	for _, repo := range m.repos {
		if repo.Archived {
			continue
		}
		out = append(out, map[string]any{"id": repo.ID, "name": repo.Name, "branch": repo.Branch, "private": repo.Private, "state": repo.State, "error": repo.Error, "head": repo.Head, "lastSync": repo.LastSync, "nextSync": repo.NextSync, "refreshMinutes": repo.RefreshMinutes, "queued": repo.Queued, "progress": repo.progress})
	}
	m.mu.Unlock()
	jsonResponse(w, 200, out)
}
func validSchedule(n int) bool { return n == 0 || n == 15 || n == 60 || n == 360 || n == 1440 }
func (s *Server) addRepository(w http.ResponseWriter, r *http.Request) {
	if !s.checkMutation(w, r) || !s.requireAdmin(w, r) {
		return
	}
	var body struct {
		Repository     string `json:"repository"`
		RefreshMinutes *int   `json:"refreshMinutes"`
	}
	if decode(w, r, &body) != nil {
		jsonError(w, 400, "Choose a GitHub repository")
		return
	}
	name, err := normalizeRepo(body.Repository)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	minutes := 60
	if body.RefreshMinutes != nil {
		minutes = *body.RefreshMinutes
	}
	if !validSchedule(minutes) {
		jsonError(w, 400, "Choose a supported refresh interval")
		return
	}
	remote, _, err := s.github.repository(r.Context(), name)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	name, _ = normalizeRepo(remote.FullName)
	m := s.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing := m.repos[remote.ID]; existing != nil {
		if !existing.Archived {
			jsonError(w, 409, "This repository is already in the workspace")
			return
		}
		old := existing.Repository
		existing.Archived = false
		existing.Queued = true
		existing.State = "queued"
		existing.RefreshMinutes = minutes
		if err = m.save(existing); err != nil {
			existing.Repository = old
			jsonError(w, 500, "Unable to restore repository")
			return
		}
		m.notify()
		jsonResponse(w, 201, existing.Repository)
		return
	}
	repo := &runtimeRepo{Repository: Repository{ID: remote.ID, Name: name, Branch: remote.DefaultBranch, Private: remote.Private, RefreshMinutes: minutes, Queued: true, State: "queued"}, views: map[string]*generation{}}
	if err = m.save(repo); err != nil {
		jsonError(w, 500, "Unable to save repository")
		return
	}
	m.repos[repo.ID] = repo
	m.notify()
	jsonResponse(w, 201, repo.Repository)
}
func (s *Server) updateRepository(w http.ResponseWriter, r *http.Request) {
	if !s.checkMutation(w, r) || !s.requireAdmin(w, r) {
		return
	}
	var body struct {
		RefreshMinutes int `json:"refreshMinutes"`
	}
	if decode(w, r, &body) != nil || !validSchedule(body.RefreshMinutes) {
		jsonError(w, 400, "Choose a supported refresh interval")
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	m := s.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	repo := m.repos[id]
	if repo == nil || repo.Archived {
		jsonError(w, 404, "Repository not found")
		return
	}
	old := repo.Repository
	repo.RefreshMinutes = body.RefreshMinutes
	repo.NextSync = time.Time{}
	if body.RefreshMinutes > 0 {
		repo.NextSync = time.Now().Add(time.Duration(body.RefreshMinutes) * time.Minute).UTC()
	}
	if m.save(repo) != nil {
		repo.Repository = old
		jsonError(w, 500, "Unable to save schedule")
		return
	}
	jsonResponse(w, 200, repo.Repository)
}
func (s *Server) archiveRepository(w http.ResponseWriter, r *http.Request) {
	if !s.checkMutation(w, r) || !s.requireAdmin(w, r) {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	m := s.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	repo := m.repos[id]
	if repo == nil || repo.Archived {
		jsonError(w, 404, "Repository not found")
		return
	}
	old := repo.Repository
	repo.Archived = true
	repo.Queued = false
	if m.save(repo) != nil {
		repo.Repository = old
		jsonError(w, 500, "Unable to archive repository")
		return
	}
	jsonResponse(w, 200, map[string]bool{"archived": true})
}
func (s *Server) repositoryView(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(r.PathValue("owner") + "/" + r.PathValue("repo"))
	m := s.manager
	m.mu.Lock()
	var repo *runtimeRepo
	for _, v := range m.repos {
		if v.Name == name && !v.Archived {
			repo = v
			break
		}
	}
	if repo == nil {
		m.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	head := r.URL.Query().Get("index")
	if head == "" {
		head = repo.Head
	}
	allowed := false
	for _, h := range repo.Generations {
		if head == h {
			allowed = true
		}
	}
	path := "/" + r.PathValue("rest")
	progress := repo.progress
	state, repoError := repo.State, repo.Error
	m.mu.Unlock()
	if head == "" {
		if strings.HasPrefix(path, "/api/") {
			jsonResponse(w, 202, map[string]any{"state": state, "completed": progress.Completed, "total": progress.Total, "error": repoError})
			return
		}
		http.Redirect(w, r, "/repositories?opening="+url.QueryEscape(name), 303)
		return
	}
	if !allowed {
		jsonError(w, 404, "This saved analysis is not available")
		return
	}
	// Pin every loaded page to a completed generation; APIs and navigation carry
	// the same index parameter, avoiding mixed datasets while refreshes finish.
	if r.URL.Query().Get("index") == "" && !strings.HasPrefix(path, "/api/") && !strings.Contains(filepath.Base(path), ".") {
		q := r.URL.Query()
		q.Set("index", head)
		target := "/r/" + name + path
		if path == "/" {
			target += "overview"
		}
		http.Redirect(w, r, target+"?"+q.Encode(), 303)
		return
	}
	m.serve(w, r, repo, head, path)
}
func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		http.Error(w, "Payload too large", 413)
		return
	}
	mac := hmac.New(sha256.New, []byte(s.cfg.WebhookSecret))
	mac.Write(body)
	want := mac.Sum(nil)
	got, err := hex.DecodeString(strings.TrimPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256="))
	if s.cfg.WebhookSecret == "" || err != nil || !hmac.Equal(got, want) {
		http.Error(w, "Invalid signature", 401)
		return
	}
	delivery := r.Header.Get("X-GitHub-Delivery")
	if len(delivery) < 1 || len(delivery) > 100 {
		http.Error(w, "Missing delivery ID", 400)
		return
	}
	var event struct {
		Ref, Action string
		Repository  struct {
			ID int64 `json:"id"`
		}
		Removed []struct {
			ID int64 `json:"id"`
		} `json:"repositories_removed"`
	}
	if json.Unmarshal(body, &event) != nil {
		http.Error(w, "Invalid payload", 400)
		return
	}
	// Record delivery and the durable queue transition in one SQLite transaction.
	m := s.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	tx, err := s.store.db.Begin()
	if err != nil {
		http.Error(w, "Queue unavailable", 503)
		return
	}
	defer tx.Rollback()
	result, err := tx.Exec("INSERT OR IGNORE INTO deliveries(id,received) VALUES(?,?)", delivery, time.Now().Unix())
	if err != nil {
		http.Error(w, "Queue unavailable", 503)
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		w.WriteHeader(202)
		return
	}
	updates := map[int64]Repository{}
	if r.Header.Get("X-GitHub-Event") == "push" {
		if repo := m.repos[event.Repository.ID]; repo != nil && !repo.Archived && event.Ref == "refs/heads/"+repo.Branch {
			copy := repo.Repository
			copy.Queued = true
			if !repo.running {
				copy.State = "queued"
			}
			updates[repo.ID] = copy
		}
	}
	if r.Header.Get("X-GitHub-Event") == "installation_repositories" && event.Action == "removed" {
		for _, removed := range event.Removed {
			if repo := m.repos[removed.ID]; repo != nil {
				copy := repo.Repository
				copy.Archived = true
				copy.Queued = false
				updates[repo.ID] = copy
			}
		}
	}
	for id, repo := range updates {
		b, _ := json.Marshal(repo)
		if _, err = tx.Exec("UPDATE repositories SET document=? WHERE id=?", b, id); err != nil {
			http.Error(w, "Queue unavailable", 503)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		http.Error(w, "Queue unavailable", 503)
		return
	}
	for id, repo := range updates {
		m.repos[id].Repository = repo
	}
	m.notify()
	w.WriteHeader(202)
}
