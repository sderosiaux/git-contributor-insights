package explorer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sderosiaux/git-contributor-insights/pkg/ownership"
)

func TestReadOnlyExplorerAndEvidence(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("config", "user.name", "Test Author")
	git("config", "user.email", "author@example.test")
	if err := os.WriteFile(filepath.Join(repo, "proof.txt"), []byte("real evidence\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "A change to inspect")
	sha := git("rev-parse", "HEAD")
	data := &ownership.Dataset{Repository: ownership.Repository{Name: "fixture", Path: repo}, Snapshots: []ownership.Snapshot{{Files: []ownership.File{{Path: "proof.txt", Contributions: []ownership.Contribution{{Evidence: []ownership.Evidence{{SHA: sha}}}}}}}}}
	handler, err := NewHandler(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method, url string
		code        int
	}{
		{"GET", "/", 200}, {"GET", "/app.js", 200}, {"GET", "/api/ownership", 200},
		{"POST", "/api/ownership", 405}, {"GET", "/.git/config", 404},
		{"GET", "/api/commit?sha=HEAD&path=proof.txt", 400},
		{"GET", "/api/commit?sha=" + sha + "&path=../secret", 400},
	} {
		res := httptest.NewRecorder()
		request := httptest.NewRequest(test.method, test.url, nil)
		request.Host = "127.0.0.1:8787"
		handler.ServeHTTP(res, request)
		if res.Code != test.code {
			t.Errorf("%s %s: got %d want %d", test.method, test.url, res.Code, test.code)
		}
	}
	res := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/commit?sha="+sha+"&path=proof.txt", nil)
	request.Host = "localhost:8787"
	handler.ServeHTTP(res, request)
	var proof struct {
		Patch     string `json:"patch"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &proof); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(proof.Patch, "+real evidence") || proof.Truncated {
		t.Fatalf("unexpected evidence: %+v", proof)
	}
	if got := git("status", "--porcelain"); got != "" {
		t.Fatalf("analysis changed repository: %s", got)
	}
	blocked := httptest.NewRecorder()
	handler.ServeHTTP(blocked, httptest.NewRequest(http.MethodGet, "http://untrusted.test/api/ownership", nil))
	if blocked.Code != http.StatusForbidden {
		t.Fatal("non-local host should not read repository data")
	}
}

func TestCalendarPeriodRefinementAndEvidence(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("config", "user.name", "Test Author")
	git("config", "user.email", "author@example.test")
	if err := os.WriteFile(filepath.Join(repo, "detail.go"), []byte("package detail\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "Evidence only exposed by a refined period")
	sha := git("rev-parse", "HEAD")
	initial := &ownership.Dataset{Repository: ownership.Repository{Path: repo, Head: sha}, Timeline: ownership.Timeline{Since: "2020-01-01T12:00:00Z", Until: "2020-12-31T23:59:59Z", Interval: "month", Calendar: true}}
	calls := 0
	handler, err := NewHandler(initial, func(ctx context.Context, from, to string) (*ownership.Dataset, error) {
		calls++
		if from != "2020-02-10T00:00:00Z" || to != "2020-02-20T23:59:59.999999999Z" {
			t.Errorf("lost exact day boundaries: %s → %s", from, to)
		}
		return &ownership.Dataset{Repository: initial.Repository, Timeline: ownership.Timeline{Since: from, Until: to, Interval: "day", Calendar: true, Points: 12}, Snapshots: []ownership.Snapshot{{Date: to, Files: []ownership.File{{Path: "detail.go", Contributions: []ownership.Contribution{{Evidence: []ownership.Evidence{{SHA: sha, Path: "detail.go"}}}}}}}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	request := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "http://localhost:8787"+path, nil))
		return r
	}
	proofURL := "/api/commit?sha=" + sha + "&path=detail.go"
	if got := request(proofURL); got.Code != http.StatusBadRequest {
		t.Fatal("unseen evidence must not be readable yet")
	}
	for _, query := range []string{"from=2020-02-10", "from=2020-02-30&to=2020-03-01", "from=2020-03-01&to=2020-02-01", "from=2019-12-31&to=2020-02-10", "from=2020-02-10&to=2021-01-01"} {
		if got := request("/api/ownership?" + query); got.Code != http.StatusBadRequest {
			t.Errorf("invalid period %s returned %d", query, got.Code)
		}
	}
	if calls != 0 {
		t.Fatal("invalid periods must not invoke Git analysis")
	}
	response := request("/api/ownership?from=2020-02-10&to=2020-02-20")
	if response.Code != http.StatusOK {
		t.Fatalf("refine: %d %s", response.Code, response.Body.String())
	}
	var payload struct {
		Timeline struct {
			Adaptive bool
			Interval string
			Since    string
			Until    string
		}
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Timeline.Adaptive || payload.Timeline.Interval != "day" || payload.Timeline.Since != "2020-02-10T00:00:00Z" {
		t.Fatalf("wrong refinement metadata: %+v", payload.Timeline)
	}
	if got := request(proofURL); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "+package detail") {
		t.Fatalf("refined evidence unavailable: %d %s", got.Code, got.Body.String())
	}
}

func TestConcurrentRefinementsAndCancellation(t *testing.T) {
	initial := &ownership.Dataset{Repository: ownership.Repository{Head: "frozen"}, Timeline: ownership.Timeline{Since: "2020-01-01T00:00:00Z", Until: "2020-12-31T23:59:59Z"}}
	handler, err := NewHandler(initial, func(ctx context.Context, from, to string) (*ownership.Dataset, error) {
		return &ownership.Dataset{Repository: initial.Repository, Timeline: initial.Timeline, Snapshots: []ownership.Snapshot{{Files: []ownership.File{{Path: "file.go", Contributions: []ownership.Contribution{{Evidence: []ownership.Evidence{{SHA: "sha"}}}}}}}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, path := range []string{"/api/ownership?from=2020-02-01&to=2020-02-10", "/api/commit?sha=missing&path=file.go"} {
				handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://localhost:8787"+path, nil))
			}
		}()
	}
	wg.Wait()
	cancelled, err := NewHandler(initial, func(ctx context.Context, from, to string) (*ownership.Dataset, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	response := httptest.NewRecorder()
	cancelled.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://localhost:8787/api/ownership?from=2020-02-01&to=2020-02-10", nil).WithContext(ctx))
	if response.Code == http.StatusInternalServerError || response.Body.Len() != 0 {
		t.Fatal("cancelled refinement must not produce a stale error response")
	}
}

func TestPatchOutputIsBounded(t *testing.T) {
	var b cappedBuffer
	n, err := b.Write([]byte(strings.Repeat("a", maxPatchBytes+20)))
	if err != nil || n != maxPatchBytes+20 || b.Len() != maxPatchBytes || !b.truncated {
		t.Fatal("patch bound not enforced")
	}
	if n, err := b.Write([]byte("more")); err != nil || n != 4 || b.Len() != maxPatchBytes {
		t.Fatal("subsequent writes must be consumed without growing")
	}
}

func TestWorkspaceURLsCanBeOpenedDirectly(t *testing.T) {
	handler, err := NewHandler(&ownership.Dataset{})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/overview", "/codebase?path=db.go&at=2020-07-01", "/evolution?from=2017-01-01&to=2026-10-01"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			request := httptest.NewRequest(method, "http://localhost:8787"+path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/html") {
				t.Fatalf("%s %s: %d %s", method, path, response.Code, response.Header().Get("Content-Type"))
			}
			if method == http.MethodGet && !strings.Contains(response.Body.String(), `src="/routes.js"`) {
				t.Fatalf("%s did not serve the application shell", path)
			}
		}
	}
	for _, path := range []string{"/missing", "/api/missing", "/assets/index.html"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://localhost:8787"+path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s: unexpected app fallback (%d)", path, response.Code)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://localhost:8787/evolution/?from=2017-01-01", nil))
	if response.Code != http.StatusPermanentRedirect || response.Header().Get("Location") != "/evolution?from=2017-01-01" {
		t.Fatalf("trailing slash must preserve query: %d %s", response.Code, response.Header().Get("Location"))
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://localhost:8787/routes.js", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "AtlasRoutes") {
		t.Fatal("routing script is unavailable")
	}
}
