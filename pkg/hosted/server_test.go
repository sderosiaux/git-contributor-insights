package hosted

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sderosiaux/git-contributor-insights/pkg/ownership"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{Listen: "127.0.0.1:8789", PublicURL: "http://127.0.0.1:8789", DataDir: t.TempDir(), Organization: "conduktor", EmailDomain: "conduktor.io", Admins: []string{"sderosiaux"}, Development: true, Workers: 2}
}
func testRequest(s *Server, method, path, body, sessionID, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, s.cfg.PublicURL+path, strings.NewReader(body))
	if sessionID != "" {
		r.AddCookie(&http.Cookie{Name: "atlas_session", Value: sessionID})
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if method != "GET" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func authServer(t *testing.T) (*Server, *atomic.Bool) {
	t.Helper()
	allowed := &atomic.Bool{}
	allowed.Store(true)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			jsonResponse(w, 200, map[string]string{"login": "sderosiaux"})
		case "/user/emails":
			jsonResponse(w, 200, []map[string]any{{"email": "simon@conduktor.io", "verified": allowed.Load()}})
		case "/orgs/conduktor/installation":
			jsonResponse(w, 200, map[string]int{"id": 12})
		case "/app/installations/12/access_tokens":
			jsonResponse(w, 201, map[string]string{"token": "installation-secret"})
		case "/orgs/conduktor/memberships/sderosiaux":
			jsonResponse(w, 200, map[string]string{"state": "active"})
		case "/login/oauth/access_token":
			if r.ParseForm() != nil || r.Form.Get("code_verifier") == "" {
				http.Error(w, "missing PKCE", 400)
				return
			}
			jsonResponse(w, 200, map[string]any{"access_token": "user-secret", "expires_in": 28800})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fixture.Close)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	c := testConfig(t)
	c.Development = false
	c.PublicURL = "https://atlas.test"
	c.GitHubAPI = fixture.URL
	c.GitHubWeb = fixture.URL
	c.AppID = "42"
	c.ClientID = "client"
	c.ClientSecret = "secret"
	c.WebhookSecret = strings.Repeat("x", 32)
	c.PrivateKeyFile = filepath.Join(t.TempDir(), "app.pem")
	if err = os.WriteFile(c.PrivateKeyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := New(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, allowed
}
func TestAuthenticationBoundaryAndCSRF(t *testing.T) {
	s, _ := authServer(t)
	for _, path := range []string{"/api/session", "/api/repositories", "/api/settings", "/r/conduktor/private/api/source?path=secrets.txt", "/r/conduktor/private/api/ownership"} {
		w := testRequest(s, "GET", path, "", "", "")
		if w.Code != 401 {
			t.Fatalf("anonymous %s: %d", path, w.Code)
		}
	}
	w := testRequest(s, "GET", "/r/conduktor/private/codebase?path=README.md", "", "", "")
	if w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/login?next=") {
		t.Fatal("shared link must return to login")
	}
	id := randomToken()
	if err := s.store.saveSession(id, session{Login: "reader", Token: "unused", Expires: time.Now().Add(time.Hour), Checked: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if w = testRequest(s, "POST", "/api/repositories", `{"repository":"owner/repo"}`, id, s.cfg.PublicURL); w.Code != 403 {
		t.Fatal("reader mutated repositories")
	}
	admin := randomToken()
	_ = s.store.saveSession(admin, session{Login: "sderosiaux", Token: "unused", Expires: time.Now().Add(time.Hour), Checked: time.Now()})
	for _, origin := range []string{"", "https://evil.example"} {
		if w = testRequest(s, "PUT", "/api/settings/admins", `{"admins":["reader"]}`, admin, origin); w.Code != 403 {
			t.Fatal("cross-origin mutation succeeded")
		}
	}
	if w = testRequest(s, "PUT", "/api/settings/admins", `{"admins":["reader"]}`, admin, s.cfg.PublicURL); w.Code != 200 || !s.isAdmin("reader") {
		t.Fatal("admin update failed")
	}
	if w = testRequest(s, "POST", "/auth/logout", "{}", admin, s.cfg.PublicURL); w.Code != 200 {
		t.Fatal("logout failed")
	}
	if w = testRequest(s, "GET", "/api/session", "", admin, ""); w.Code != 401 {
		t.Fatal("logged-out session still works")
	}
	req := httptest.NewRequest("GET", "https://evil.example/api/session", nil)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 421 {
		t.Fatal("host check missing")
	}
}
func TestOAuthStatePKCEAndVerifiedDomain(t *testing.T) {
	s, allowed := authServer(t)
	begin := testRequest(s, "GET", "/auth/github?next="+url.QueryEscape("/r/conduktor/console-plus/evolution?from=2024-01-01"), "", "", "")
	u, _ := url.Parse(begin.Header().Get("Location"))
	state := u.Query().Get("state")
	if state == "" || u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("code_challenge") == "" {
		t.Fatal("missing state or PKCE")
	}
	callback := func(cookie bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", s.cfg.PublicURL+"/auth/callback?code=ok&state="+state, nil)
		if cookie {
			req.AddCookie(begin.Result().Cookies()[0])
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		return w
	}
	if w := callback(false); w.Code != 400 {
		t.Fatal("state without bound cookie accepted")
	}
	w := callback(true)
	if w.Code != 303 || w.Header().Get("Location") != "/r/conduktor/console-plus/evolution?from=2024-01-01" {
		t.Fatalf("login failed: %d %s", w.Code, w.Body.String())
	}
	var sessionID string
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "atlas_session" {
			sessionID = cookie.Value
			if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
				t.Fatal("insecure session cookie")
			}
		}
	}
	if sessionID == "" {
		t.Fatal("no session cookie")
	}
	if w = callback(true); w.Code != 400 {
		t.Fatal("replayed OAuth state accepted")
	}
	allowed.Store(false)
	if _, err := s.github.identity(context.Background(), "user-secret"); err == nil {
		t.Fatal("unverified company email accepted")
	}
	v, _ := s.store.session(sessionID)
	v.Checked = time.Now().Add(-11 * time.Minute)
	_ = s.store.saveSession(sessionID, v)
	if w = testRequest(s, "GET", "/api/session", "", sessionID, ""); w.Code != 401 {
		t.Fatal("revoked access not rechecked")
	}
	for _, target := range []string{"//evil.example", "https://evil.example", "/\\evil.example"} {
		if safeNext(target) != "/repositories" {
			t.Fatal("open redirect", target)
		}
	}
}
func TestValidationRejectsExposureAndArbitraryCloneURLs(t *testing.T) {
	for _, value := range []string{"file:///etc/passwd", "ssh://host/repo", "https://github.com.evil/a/b", "https://x@github.com/a/b", "https://github.com/a/b?x=1", "a/..", "a/b/c", "--upload-pack=bad"} {
		if _, err := normalizeRepo(value); err == nil {
			t.Fatal("accepted", value)
		}
	}
	if v, err := normalizeRepo("https://github.com/Apache/Kafka.git"); err != nil || v != "apache/kafka" {
		t.Fatal(v, err)
	}
	c := testConfig(t)
	c.Listen = "0.0.0.0:8789"
	if c.Validate() == nil {
		t.Fatal("development mode exposed publicly")
	}
	c = testConfig(t)
	c.Development = false
	if c.Validate() == nil {
		t.Fatal("production started with insecure configuration")
	}
}
func TestWebhookSignatureDeduplicationAndRestartQueue(t *testing.T) {
	c := testConfig(t)
	c.WebhookSecret = strings.Repeat("w", 32)
	st, err := openStore(c.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, err := newManager(ctx, c, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := &runtimeRepo{Repository: Repository{ID: 1, Name: "conduktor/console-plus", Branch: "main", State: "ready"}, views: map[string]*generation{}}
	m.repos[1] = r
	_ = m.save(r)
	s := &Server{cfg: c, store: st, manager: m}
	send := func(delivery, signature, ref string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"ref":%q,"repository":{"id":1}}`, ref)
		req := httptest.NewRequest("POST", "https://atlas.test/webhooks/github", strings.NewReader(body))
		req.Header.Set("X-GitHub-Delivery", delivery)
		req.Header.Set("X-GitHub-Event", "push")
		if signature == "valid" {
			mac := hmac.New(sha256.New, []byte(c.WebhookSecret))
			mac.Write([]byte(body))
			signature = "sha256=" + hex.EncodeToString(mac.Sum(nil))
		}
		req.Header.Set("X-Hub-Signature-256", signature)
		w := httptest.NewRecorder()
		s.webhook(w, req)
		return w
	}
	if w := send("1", "bad", "refs/heads/main"); w.Code != 401 || r.Queued {
		t.Fatal("unsigned webhook queued work")
	}
	if w := send("2", "valid", "refs/heads/feature"); w.Code != 202 || r.Queued {
		t.Fatal("feature push changed default branch analysis")
	}
	if w := send("3", "valid", "refs/heads/main"); w.Code != 202 || !r.Queued {
		t.Fatal("push not queued")
	}
	r.Queued = false
	_ = m.save(r)
	if w := send("3", "valid", "refs/heads/main"); w.Code != 202 || r.Queued {
		t.Fatal("duplicate webhook queued again")
	}
	_ = send("4", "valid", "refs/heads/main")
	reopened, err := newManager(ctx, c, st, nil)
	if err != nil || !reopened.repos[1].Queued {
		t.Fatal("queue lost after restart")
	}
}
func localGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Example", "GIT_AUTHOR_EMAIL=example@conduktor.io", "GIT_COMMITTER_NAME=Example", "GIT_COMMITTER_EMAIL=example@conduktor.io", "GIT_AUTHOR_DATE=2025-01-01T12:00:00Z", "GIT_COMMITTER_DATE=2025-01-01T12:00:00Z")
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s %v", args, b, err)
	}
	return strings.TrimSpace(string(b))
}
func addFixture(t *testing.T, s *Server, id int64, name string) *runtimeRepo {
	t.Helper()
	dir := s.manager.repoDir(id)
	_ = os.MkdirAll(dir, 0700)
	localGit(t, dir, "init", "-q")
	_ = os.WriteFile(filepath.Join(dir, "README.md"), []byte("first owner\n"), 0600)
	localGit(t, dir, "add", ".")
	localGit(t, dir, "commit", "-qm", "first")
	head := localGit(t, dir, "rev-parse", "HEAD")
	idx, err := ownership.OpenIndex(context.Background(), dir, ownership.Options{Revision: head, Workers: 2, ConfigPath: s.manager.affiliations()}, s.manager.database(id))
	if err != nil {
		t.Fatal(err)
	}
	if err = idx.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	idx.Close()
	r := &runtimeRepo{Repository: Repository{ID: id, Name: name, Branch: "main", State: "ready", Head: head, Generations: []string{head}}, views: map[string]*generation{}}
	s.manager.mu.Lock()
	s.manager.repos[id] = r
	err = s.manager.save(r)
	s.manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestMultipleReposPinnedGenerationsAndRestart(t *testing.T) {
	c := testConfig(t)
	s, err := New(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	first := addFixture(t, s, 1, "conduktor/one")
	second := addFixture(t, s, 2, "apache/two")
	old := first.Head
	dir := s.manager.repoDir(first.ID)
	_ = os.WriteFile(filepath.Join(dir, "later.txt"), []byte("new state\n"), 0600)
	localGit(t, dir, "add", ".")
	localGit(t, dir, "commit", "-qm", "second")
	latest := localGit(t, dir, "rev-parse", "HEAD")
	idx, err := ownership.OpenIndex(context.Background(), dir, ownership.Options{Revision: latest, Workers: 2, ConfigPath: s.manager.affiliations()}, s.manager.database(1))
	if err != nil {
		t.Fatal(err)
	}
	if err = idx.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	idx.Close()
	s.manager.mu.Lock()
	first.Head = latest
	first.Generations = append(first.Generations, latest)
	_ = s.manager.save(first)
	s.manager.mu.Unlock()
	for _, head := range []string{old, latest} {
		w := testRequest(s, "GET", "/r/conduktor/one/api/ownership?index="+head, "", "", "")
		if w.Code != 200 {
			t.Fatalf("query: %d %s", w.Code, w.Body.String())
		}
		var data ownership.Dataset
		if json.Unmarshal(w.Body.Bytes(), &data) != nil || data.Repository.Head != head {
			t.Fatal("generation changed under shared link")
		}
	}
	if w := testRequest(s, "GET", "/r/apache/two/api/ownership?index="+latest, "", "", ""); w.Code != 404 {
		t.Fatal("generation escaped repository boundary")
	}
	w := testRequest(s, "GET", "/r/conduktor/one/codebase?path=README.md", "", "", "")
	if w.Code != 303 || !strings.Contains(w.Header().Get("Location"), "index="+latest) {
		t.Fatal("page did not freeze the active generation")
	}
	w = testRequest(s, "GET", "/r/conduktor/one/codebase?index="+old, "", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `src="/r/conduktor/one/routes.js"`) {
		t.Fatal("hosted assets lost repo scope")
	}
	// The previous generation still serves while a refresh is in progress.
	s.manager.mu.Lock()
	first.State = "indexing"
	first.Queued = false
	_ = s.manager.save(first)
	s.manager.mu.Unlock()
	if w = testRequest(s, "GET", "/r/conduktor/one/api/index?index="+old, "", "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"ready"`) {
		t.Fatal("refresh blocked previous generation")
	}
	// Avoid starting a real network job when reopening this fixture.
	s.manager.mu.Lock()
	first.State = "ready"
	_ = s.manager.save(first)
	s.manager.mu.Unlock()
	secondID := second.ID
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if w = testRequest(s, "GET", "/r/conduktor/one/api/index?index="+old, "", "", ""); w.Code != 200 {
		t.Fatal("saved generations lost across restart")
	}
	if w = testRequest(s, "DELETE", fmt.Sprintf("/api/repositories/%d", secondID), "{}", "", c.PublicURL); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = testRequest(s, "GET", "/r/apache/two/overview", "", "", ""); w.Code != 404 {
		t.Fatal("archived shared link remains visible")
	}
}

func TestRefreshFailureKeepsOldVersionAndRetryReusesCommits(t *testing.T) {
	c := testConfig(t)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, githubRepo{ID: 1, FullName: "conduktor/one", DefaultBranch: "main"})
	}))
	defer fixture.Close()
	c.GitHubAPI = fixture.URL
	s, err := New(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := addFixture(t, s, 1, "conduktor/one")
	old := r.Head
	dir := s.manager.repoDir(1)
	_ = os.WriteFile(filepath.Join(dir, "new.go"), []byte("package example\n"), 0600)
	localGit(t, dir, "add", ".")
	localGit(t, dir, "commit", "-qm", "later change")
	latest := localGit(t, dir, "rev-parse", "HEAD")
	s.manager.fetch = func(context.Context, githubRepo, string) (string, error) { return latest, nil }
	s.manager.worker = func(context.Context, string, string, string, func(ownership.IndexStatus)) error {
		return fmt.Errorf("interrupted worker")
	}
	if err = s.manager.sync(r); err == nil {
		t.Fatal("worker failure ignored")
	}
	if r.Head != old {
		t.Fatal("failed generation replaced the complete analysis")
	}
	if w := testRequest(s, "GET", "/r/conduktor/one/api/ownership", "", "", ""); w.Code != 200 {
		t.Fatal("previous analysis blocked by failed update")
	}
	s.manager.worker = func(ctx context.Context, repo, head, db string, progress func(ownership.IndexStatus)) error {
		idx, err := ownership.OpenIndex(ctx, repo, ownership.Options{Revision: head, Workers: 2, ConfigPath: s.manager.affiliations()}, db)
		if err != nil {
			return err
		}
		defer idx.Close()
		if idx.Status().Completed != 1 || idx.Status().Total != 2 {
			t.Fatalf("completed history not reused: %+v", idx.Status())
		}
		return idx.Run(ctx)
	}
	if err = s.manager.sync(r); err != nil {
		t.Fatal(err)
	}
	if r.Head != latest || len(r.Generations) != 2 {
		t.Fatal("successful generation not published")
	}
	if w := testRequest(s, "GET", "/r/conduktor/one/api/ownership?index="+old, "", "", ""); w.Code != 200 {
		t.Fatal("old shared link broke after successful refresh")
	}
}

func TestExplorerShellDoesNotWaitForIndexOpening(t *testing.T) {
	c := testConfig(t)
	s, err := New(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	head := strings.Repeat("a", 40)
	// The registry is available, but this fixture intentionally has no Git files.
	// Serving a shell or asset must not try to reopen its saved index.
	repo := &runtimeRepo{Repository: Repository{ID: 42, Name: "conduktor/cold", Head: head, State: "ready", Generations: []string{head}}, views: map[string]*generation{}}
	s.manager.mu.Lock()
	s.manager.repos[42] = repo
	s.manager.mu.Unlock()
	for _, path := range []string{"overview", "evolution", "codebase", "history", "ai", "routes.js", "app.js", "styles.css"} {
		w := testRequest(s, "GET", "/r/conduktor/cold/"+path+"?index="+head, "", "", "")
		if w.Code != 200 {
			t.Fatalf("shell %s waited for a missing index: %d", path, w.Code)
		}
	}
	repo.serveMu.RLock()
	cached := len(repo.views)
	repo.serveMu.RUnlock()
	if cached != 0 {
		t.Fatal("static requests opened an index")
	}
	w := testRequest(s, "GET", "/r/conduktor/cold/api/ownership?index="+head, "", "", "")
	if w.Code != 503 {
		t.Fatal("data request pretended the missing index was available")
	}
}
