package hosted

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sderosiaux/git-contributor-insights/pkg/explorer"
	"github.com/sderosiaux/git-contributor-insights/pkg/ownership"
)

type generation struct {
	index   *ownership.Index
	handler http.Handler
}
type runtimeRepo struct {
	Repository
	progress ownership.IndexStatus
	running  bool
	serveMu  sync.RWMutex
	views    map[string]*generation
}
type manager struct {
	mu      sync.Mutex
	repos   map[int64]*runtimeRepo
	store   *store
	cfg     Config
	github  *github
	ctx     context.Context
	wake    chan struct{}
	done    chan struct{}
	warming sync.WaitGroup
	worker  func(context.Context, string, string, string, func(ownership.IndexStatus)) error
	fetch   func(context.Context, githubRepo, string) (string, error)
}

func newManager(ctx context.Context, c Config, st *store, g *github) (*manager, error) {
	m := &manager{repos: map[int64]*runtimeRepo{}, store: st, cfg: c, github: g, ctx: ctx, wake: make(chan struct{}, 1), done: make(chan struct{})}
	repos, err := st.repos()
	if err != nil {
		return nil, err
	}
	for _, r := range repos {
		if r.State == "syncing" || r.State == "indexing" {
			r.Queued = true
			r.State = "queued"
		}
		m.repos[r.ID] = &runtimeRepo{Repository: r, views: map[string]*generation{}}
	}
	m.worker = m.runWorker
	m.fetch = m.fetchGit
	return m, nil
}
func (m *manager) notify() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}
func (m *manager) start() {
	m.warming.Add(1)
	go func() {
		defer m.warming.Done()
		m.warmReadyViews()
	}()
	go m.loop()
	m.notify()
}

// Reopen saved analyses in the background after a restart. HTML and assets do
// not wait for this work; data requests share the same protected handler cache.
func (m *manager) warmReadyViews() {
	type target struct {
		repo *runtimeRepo
		head string
	}
	m.mu.Lock()
	var targets []target
	for _, r := range m.repos {
		if !r.Archived && r.Head != "" {
			targets = append(targets, target{r, r.Head})
		}
	}
	m.mu.Unlock()
	for _, t := range targets {
		if m.ctx.Err() != nil {
			return
		}
		t.repo.serveMu.Lock()
		if t.repo.views[t.head] == nil {
			view, err := m.openGeneration(t.repo, t.head)
			if err == nil {
				t.repo.cacheGeneration(t.head, view)
			} else if m.ctx.Err() == nil {
				log.Printf("prepare saved analysis %s: %v", t.repo.Name, err)
			}
		}
		t.repo.serveMu.Unlock()
	}
}

func (m *manager) close() {
	<-m.done
	m.warming.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.repos {
		r.serveMu.Lock()
		for _, v := range r.views {
			_ = v.index.Close()
		}
		r.serveMu.Unlock()
	}
}
func (m *manager) save(r *runtimeRepo) error { return m.store.saveRepo(r.Repository) }
func (m *manager) queue(id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.repos[id]
	if r == nil || r.Archived {
		return fmt.Errorf("repository not found")
	}
	old := r.Repository
	r.Queued = true
	if !r.running {
		r.State = "queued"
	}
	if err := m.save(r); err != nil {
		r.Repository = old
		return err
	}
	m.notify()
	return nil
}
func (m *manager) loop() {
	defer close(m.done)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-m.wake:
		case <-ticker.C:
		}
		for {
			m.mu.Lock()
			var next *runtimeRepo
			now := time.Now()
			for _, r := range m.repos {
				if r.Archived || r.running {
					continue
				}
				if r.Queued || (r.RefreshMinutes > 0 && !r.NextSync.IsZero() && !now.Before(r.NextSync)) {
					if next == nil || r.LastSync.Before(next.LastSync) {
						next = r
					}
				}
			}
			if next != nil {
				next.running = true
				next.Queued = false
				next.State = "syncing"
				next.Error = ""
				next.progress = ownership.IndexStatus{}
				if err := m.save(next); err != nil {
					next.running = false
					m.mu.Unlock()
					log.Printf("save indexing job: %v", err)
					break
				}
			}
			m.mu.Unlock()
			if next == nil {
				break
			}
			err := m.sync(next)
			m.mu.Lock()
			next.running = false
			if err != nil {
				next.State = "error"
				next.Error = err.Error()
			} else {
				next.State = "ready"
				next.Error = ""
				next.LastSync = time.Now().UTC()
			}
			if next.RefreshMinutes > 0 {
				next.NextSync = time.Now().Add(time.Duration(next.RefreshMinutes) * time.Minute).UTC()
			} else {
				next.NextSync = time.Time{}
			}
			if next.Queued {
				next.State = "queued"
			}
			if m.ctx.Err() != nil {
				next.Queued = true
			}
			if e := m.save(next); e != nil {
				log.Printf("save indexing result: %v", e)
			}
			m.mu.Unlock()
			if m.ctx.Err() != nil {
				return
			}
		}
		m.store.clean()
	}
}
func (m *manager) repoDir(id int64) string {
	return filepath.Join(m.cfg.DataDir, "repos", strconv.FormatInt(id, 10))
}
func (m *manager) database(id int64) string {
	return filepath.Join(m.cfg.DataDir, "indexes", strconv.FormatInt(id, 10)+".sqlite")
}
func (m *manager) affiliations() string { return filepath.Join(m.cfg.DataDir, "affiliations.yaml") }
func (m *manager) sync(r *runtimeRepo) error {
	m.mu.Lock()
	name, id, oldHead := r.Name, r.ID, r.Head
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(m.ctx, 45*time.Minute)
	defer cancel()
	remote, token, err := m.github.repository(ctx, name)
	if err != nil {
		return err
	}
	if remote.ID != id {
		return fmt.Errorf("repository identity changed; add the replacement repository separately")
	}
	head, err := m.fetch(ctx, remote, token)
	if err != nil {
		return err
	}
	dir := m.repoDir(id)

	m.mu.Lock()
	r.Branch = remote.DefaultBranch
	r.Private = remote.Private
	if head == oldHead {
		m.mu.Unlock()
		return nil
	}
	r.State = "indexing"
	if err = m.save(r); err != nil {
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()
	err = m.worker(m.ctx, dir, head, m.database(id), func(p ownership.IndexStatus) { m.mu.Lock(); r.progress = p; m.mu.Unlock() })
	if err != nil {
		return err
	}
	// Open and verify the complete new index before publishing the generation.
	view, err := m.openGeneration(r, head)
	r.serveMu.Lock()
	if err == nil {
		r.cacheGeneration(head, view)
	}
	r.serveMu.Unlock()
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.Archived {
		return nil
	}
	old := r.Repository
	r.Head = head
	found := false
	for _, h := range r.Generations {
		if h == head {
			found = true
		}
	}
	if !found {
		r.Generations = append(r.Generations, head)
	}
	if err = m.save(r); err != nil {
		r.Repository = old
	}
	return err
}

// Called with serveMu held exclusively, so evicted handlers have no active readers.
func (r *runtimeRepo) cacheGeneration(head string, view *generation) {
	if previous := r.views[head]; previous != nil && previous != view {
		previous.index.Close()
	}
	r.views[head] = view
	for len(r.views) > 3 {
		for h, old := range r.views {
			if h != head {
				old.index.Close()
				delete(r.views, h)
				break
			}
		}
	}
}

func gitCommand(ctx context.Context, dir, token string, args ...string) ([]byte, error) {
	base := []string{"-c", "core.hooksPath=/dev/null", "-c", "credential.helper=", "-c", "protocol.file.allow=never", "-c", "http.followRedirects=false", "-C", dir}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if token != "" {
		cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://github.com/.extraheader", "GIT_CONFIG_VALUE_0=Authorization: Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token)))
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s failed", args[0])
	}
	return out, nil
}
func (m *manager) openGeneration(r *runtimeRepo, head string) (*generation, error) {
	idx, err := ownership.OpenIndex(m.ctx, m.repoDir(r.ID), ownership.Options{Revision: head, Workers: m.cfg.Workers, ConfigPath: m.affiliations()}, m.database(r.ID))
	if err != nil {
		return nil, err
	}
	if idx.Status().State != "ready" {
		idx.Close()
		return nil, fmt.Errorf("this generation is not completely indexed")
	}
	handler, err := explorer.NewIndexedHandler(idx, "", "", nil, explorer.HandlerOptions{Hosted: true, BasePath: "/r/" + r.Name})
	if err != nil {
		idx.Close()
		return nil, err
	}
	return &generation{idx, handler}, nil
}
func (m *manager) serve(w http.ResponseWriter, req *http.Request, r *runtimeRepo, head, path string) {
	page := req.Clone(req.Context())
	u := *req.URL
	u.Path = path
	u.RawPath = ""
	page.URL = &u
	if !strings.HasPrefix(path, "/api/") {
		handler, err := explorer.NewStaticHandler(explorer.HandlerOptions{Hosted: true, BasePath: "/r/" + r.Name})
		if err != nil {
			jsonError(w, 500, "Unable to open the explorer")
			return
		}
		handler.ServeHTTP(w, page)
		return
	}
	for {
		r.serveMu.RLock()
		if r.views[head] != nil {
			break
		}
		if r.views[head] == nil {
			r.serveMu.RUnlock()
			r.serveMu.Lock()
			if r.views[head] == nil {
				v, err := m.openGeneration(r, head)
				if err != nil {
					r.serveMu.Unlock()
					jsonError(w, 503, "Unable to open this saved analysis. Retry shortly.")
					return
				}
				r.cacheGeneration(head, v)
			}
			r.serveMu.Unlock()
		}
		if req.Context().Err() != nil {
			return
		}
	}
	v := r.views[head]
	defer r.serveMu.RUnlock()
	v.handler.ServeHTTP(w, page)
}
func (m *manager) runWorker(ctx context.Context, repo, head, db string, progress func(ownership.IndexStatus)) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, exe, "index-worker", repo, "--revision", head, "--database", db, "--workers", strconv.Itoa(m.cfg.Workers), "--config", m.affiliations())
	cmd.Env = append(os.Environ(), "GOMAXPROCS="+strconv.Itoa(m.cfg.Workers))
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 15 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return err
	}
	decoder := json.NewDecoder(stdout)
	for {
		var p ownership.IndexStatus
		if err := decoder.Decode(&p); err != nil {
			break
		}
		progress(p)
	}
	if err = cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("indexing stopped; completed commits are saved. Retry the refresh")
	}
	return nil
}

func (m *manager) fetchGit(ctx context.Context, remote githubRepo, token string) (string, error) {
	dir := m.repoDir(remote.ID)
	var err error
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if _, err = os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		if _, err = gitCommand(ctx, dir, "", "init"); err != nil {
			return "", err
		}
		if _, err = gitCommand(ctx, dir, "", "remote", "add", "origin", "https://github.com/"+remote.FullName+".git"); err != nil {
			return "", err
		}
	}
	ref := "+refs/heads/" + remote.DefaultBranch + ":refs/remotes/origin/atlas"
	if _, err = gitCommand(ctx, dir, token, "fetch", "--no-tags", "origin", ref); err != nil {
		return "", fmt.Errorf("could not fetch GitHub repository; check installation access and connectivity")
	}
	b, err := gitCommand(ctx, dir, "", "rev-parse", "--verify", "refs/remotes/origin/atlas^{commit}")
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(string(b))
	if _, err = gitCommand(ctx, dir, "", "update-ref", "--no-deref", "HEAD", head); err != nil {
		return "", err
	}
	// Retain indexed objects even after a force-push. Pinned links remain readable.
	if _, err = gitCommand(ctx, dir, "", "update-ref", "refs/atlas/"+head, head); err != nil {
		return "", err
	}
	return head, nil
}
