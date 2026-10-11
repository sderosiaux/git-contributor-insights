package explorer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sderosiaux/git-contributor-insights/pkg/ownership"
)

type testIndex struct {
	status ownership.IndexStatus
	query  func(context.Context, string, string, string) (*ownership.Dataset, error)
}

func (index *testIndex) Status() ownership.IndexStatus { return index.status }
func (index *testIndex) Query(ctx context.Context, from, to, path string) (*ownership.Dataset, error) {
	return index.query(ctx, from, to, path)
}

func TestIndexProgressAndResume(t *testing.T) {
	calls, retries := 0, 0
	index := &testIndex{status: ownership.IndexStatus{State: "indexing", Completed: 12, Total: 40, Commits: 65}}
	index.query = func(context.Context, string, string, string) (*ownership.Dataset, error) {
		calls++
		t.Fatal("partial indexing must never become a sampled dataset")
		return nil, nil
	}
	handler, err := NewIndexedHandler(index, "", "", func() { retries++ })
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost:8787"+path, nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		return response
	}
	if response := request("GET", "/evolution", ""); response.Code != 200 || !strings.Contains(response.Body.String(), "Ownership") {
		t.Fatal("workspace must load before the index is ready")
	}
	if response := request("GET", "/api/ownership", ""); response.Code != 202 || !strings.Contains(response.Body.String(), `"completed":12`) || response.Header().Get("Retry-After") == "" {
		t.Fatalf("missing progress: %d %s", response.Code, response.Body.String())
	}
	if response := request("GET", "/api/index", ""); response.Code != 200 || !strings.Contains(response.Body.String(), `"total":40`) {
		t.Fatal("index coverage unavailable")
	}
	index.status.State = "error"
	index.status.Error = "interrupted"
	if response := request("POST", "/api/index/retry", "https://other.example"); response.Code != 403 || retries != 0 {
		t.Fatal("an unrelated origin must not initiate indexing")
	}
	if response := request("POST", "/api/index/retry", "http://localhost:8787"); response.Code != 202 || retries != 1 || calls != 0 {
		t.Fatal("retry should resume the index without serving partial results")
	}
}

func TestIndexedPeriodsRetainEveryRevisionAndScope(t *testing.T) {
	base := &ownership.Dataset{
		Repository: ownership.Repository{Head: "frozen"},
		Timeline:   ownership.Timeline{Since: "2020-01-01T00:00:00Z", Until: "2020-12-31T23:59:59Z", Interval: "commit", Complete: true},
	}
	calls := 0
	index := &testIndex{status: ownership.IndexStatus{State: "ready", Head: "frozen", Completed: 300, Total: 300}}
	index.query = func(ctx context.Context, from, to, path string) (*ownership.Dataset, error) {
		calls++
		if calls == 1 {
			if from != "2020-01-01" || to != "2020-12-31" || path != "" {
				t.Fatal("configured history bounds were lost")
			}
			return base, nil
		}
		if from != "2020-03-07T00:00:00Z" || to != "2020-03-07T23:59:59.999999999Z" || path != "db.go" {
			t.Fatalf("query changed requested boundaries or scope: %s %s %s", from, to, path)
		}
		result := *base
		result.Series = []ownership.SeriesPoint{
			{Date: "2020-03-07T12:00:00Z", Revision: "first"},
			{Date: "2020-03-07T12:00:00Z", Revision: "second"},
		}
		return &result, nil
	}
	handler, err := NewIndexedHandler(index, "2020-01-01", "2020-12-31", nil)
	if err != nil {
		t.Fatal(err)
	}
	request := func(path string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", "http://localhost:8787"+path, nil))
		return response
	}
	for _, query := range []string{"from=2020-03-07", "from=2020-02-30&to=2020-03-01", "from=2019-01-01&to=2020-02-01", "from=2020-04-01&to=2020-03-01", "path=../secret"} {
		if response := request("/api/ownership?" + query); response.Code != 400 {
			t.Errorf("invalid query %s returned %d", query, response.Code)
		}
	}
	response := request("/api/ownership?from=2020-03-07&to=2020-03-07&path=db.go")
	var data ownership.Dataset
	if response.Code != 200 {
		t.Fatalf("query failed: %d %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Series) != 2 || data.Series[0].Revision != "first" || data.Series[1].Revision != "second" || !data.Timeline.Complete {
		t.Fatal("equal timestamps must not collapse separate revisions")
	}
	if calls != 2 {
		t.Fatalf("invalid requests reached the index: %d calls", calls)
	}
}

type testRevisionIndex struct {
	*testIndex
	revision func(context.Context, string) (*ownership.Dataset, error)
}

type testGraphIndex struct {
	*testIndex
	history func(context.Context, string, string, string, string) (*ownership.Dataset, error)
	commits func(context.Context, ownership.CommitFilter) (ownership.CommitPage, error)
}

func (index *testGraphIndex) QueryHistory(ctx context.Context, from, to, path, tip string) (*ownership.Dataset, error) {
	return index.history(ctx, from, to, path, tip)
}
func (index *testGraphIndex) Commits(ctx context.Context, filter ownership.CommitFilter) (ownership.CommitPage, error) {
	return index.commits(ctx, filter)
}

func TestBranchQueriesUseTheirOwnBoundsAndFrozenHead(t *testing.T) {
	head, branch := strings.Repeat("a", 40), strings.Repeat("b", 40)
	index := &testGraphIndex{testIndex: &testIndex{status: ownership.IndexStatus{State: "ready", Head: head}}}
	index.query = func(context.Context, string, string, string) (*ownership.Dataset, error) {
		t.Fatal("a branch request must not read or validate against the HEAD timeline")
		return nil, nil
	}
	calls := 0
	index.history = func(ctx context.Context, from, to, path, tip string) (*ownership.Dataset, error) {
		calls++
		if tip != branch {
			t.Fatalf("lost branch tip: %s", tip)
		}
		if from == "" {
			from, to = "2010-01-01T00:00:00Z", "2010-12-31T23:59:59Z"
		} else if from != "2010-03-01T00:00:00Z" || to != "2010-03-02T23:59:59.999999999Z" || path != "branch-only.go" {
			t.Fatalf("unexpected branch query: %s %s %s", from, to, path)
		}
		return &ownership.Dataset{Repository: ownership.Repository{Head: head}, Timeline: ownership.Timeline{Since: from, Until: to, Tip: branch, Complete: true}, Snapshots: []ownership.Snapshot{{Revision: branch, Date: to}}}, nil
	}
	handler, err := NewIndexedHandler(index, "2020-01-01", "2020-12-31", nil)
	if err != nil {
		t.Fatal(err)
	}
	request := func(path string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", "http://localhost:8787"+path, nil))
		return response
	}
	if response := request("/api/ownership?tip=" + branch); response.Code != 200 {
		t.Fatalf("branch history: %d %s", response.Code, response.Body.String())
	}
	response := request("/api/ownership?tip=" + branch + "&from=2010-03-01&to=2010-03-02&path=branch-only.go")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"tip":"`+branch+`"`) {
		t.Fatalf("branch range failed: %d %s", response.Code, response.Body.String())
	}
	for _, query := range []string{"tip=HEAD", "tip=" + branch + "&from=2011-01-01&to=2011-01-02"} {
		if response := request("/api/ownership?" + query); response.Code != 400 {
			t.Fatalf("bad branch request accepted: %s", query)
		}
	}
	if calls != 2 {
		t.Fatalf("invalid queries or repeated bounds reread the index: %d", calls)
	}
}

func TestCommitCataloguePaginationAndValidation(t *testing.T) {
	index := &testGraphIndex{testIndex: &testIndex{status: ownership.IndexStatus{State: "ready", Head: "frozen"}}}
	calls := 0
	index.commits = func(ctx context.Context, filter ownership.CommitFilter) (ownership.CommitPage, error) {
		calls++
		if filter.Query != "branch author" || filter.Offset != 50 || filter.Limit != 25 || filter.From != "2020-01-01" {
			t.Fatalf("lost catalogue filters: %+v", filter)
		}
		return ownership.CommitPage{Commits: []ownership.Commit{{SHA: "branch", Parents: []string{"base"}, Mainline: false}}, Offset: 50, Limit: 25, Total: 2121, HasMore: true}, nil
	}
	handler, err := NewIndexedHandler(index, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	request := func(path string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", "http://localhost:8787"+path, nil))
		return response
	}
	for _, query := range []string{"offset=-1", "limit=0", "limit=201", "offset=nope", "from=2020-02-30", "from=2020-03-01&to=2020-02-01"} {
		if response := request("/api/commits?" + query); response.Code != 400 {
			t.Fatalf("invalid page accepted: %s", query)
		}
	}
	response := request("/api/commits?q=branch+author&offset=50&limit=25&from=2020-01-01")
	var page ownership.CommitPage
	if response.Code != 200 {
		t.Fatalf("catalogue: %d %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || page.Total != 2121 || !page.HasMore || page.Commits[0].Mainline || len(page.Commits[0].Parents) != 1 {
		t.Fatalf("incomplete commit catalogue: %+v", page)
	}
	if response := request("/history"); response.Code != 200 {
		t.Fatal("commit history must support direct navigation")
	}
}

func (index *testRevisionIndex) Revision(ctx context.Context, sha string) (*ownership.Dataset, error) {
	return index.revision(ctx, sha)
}

func TestExactRevisionDoesNotCollapseTimestampCollisions(t *testing.T) {
	first, second := strings.Repeat("a", 40), strings.Repeat("b", 40)
	index := &testRevisionIndex{testIndex: &testIndex{status: ownership.IndexStatus{State: "ready", Head: "frozen"}}}
	index.revision = func(ctx context.Context, sha string) (*ownership.Dataset, error) {
		if sha != first && sha != second {
			return nil, fmt.Errorf("unknown revision")
		}
		lines := 1
		if sha == second {
			lines = 2
		}
		return &ownership.Dataset{Repository: ownership.Repository{Head: "frozen"}, Snapshots: []ownership.Snapshot{{Date: "2020-03-07T12:00:00Z", Revision: sha, Files: []ownership.File{{Path: "file.go", Lines: lines}}}}}, nil
	}
	handler, err := NewIndexedHandler(index, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		sha         string
		code, lines int
	}{{"HEAD", 400, 0}, {strings.Repeat("c", 40), 404, 0}, {first, 200, 1}, {second, 200, 2}} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", "http://localhost:8787/api/revision?sha="+test.sha, nil))
		if response.Code != test.code {
			t.Fatalf("revision %s: got %d want %d", test.sha, response.Code, test.code)
		}
		if test.code != 200 {
			continue
		}
		var dataset ownership.Dataset
		if err := json.Unmarshal(response.Body.Bytes(), &dataset); err != nil {
			t.Fatal(err)
		}
		if len(dataset.Snapshots) != 1 || dataset.Snapshots[0].Revision != test.sha || dataset.Snapshots[0].Files[0].Lines != test.lines {
			t.Fatal("exact revision was replaced with another state at the same timestamp")
		}
	}
}
