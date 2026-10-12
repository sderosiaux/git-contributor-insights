package explorer

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sderosiaux/git-contributor-insights/pkg/ownership"
)

type testAIIndex struct {
	testIndex
	calls  int
	filter ownership.AIFilter
}

func (index *testAIIndex) AI(ctx context.Context, filter ownership.AIFilter) (*ownership.AIReport, error) {
	index.calls++
	index.filter = filter
	return &ownership.AIReport{AICounts: ownership.AICounts{Commits: 10, Declared: 3}, Evidence: []ownership.AICommit{{Signals: []ownership.AISignal{{Tool: "Claude", Evidence: "<script>alert(1)</script>"}}}}}, nil
}
func TestAIAPIAndDeepLink(t *testing.T) {
	index := &testAIIndex{testIndex: testIndex{status: ownership.IndexStatus{State: "ready"}}}
	handler, err := NewIndexedHandler(index, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", "http://localhost:8788"+path, nil))
		return response
	}
	for _, path := range []string{"/ai?person=a%40example.test", "/ai.js", "/ai.css"} {
		if r := get(path); r.Code != 200 {
			t.Fatalf("route %s: %d", path, r.Code)
		}
	}
	if r := get("/ai/?person=a"); r.Code != 308 || r.Header().Get("Location") != "/ai?person=a" {
		t.Fatal("AI deep link redirect lost filters")
	}
	for _, query := range []string{"from=2020-02-30", "limit=0", "offset=-1", "offset=bad", "limit=201"} {
		if r := get("/api/ai?" + query); r.Code != 400 {
			t.Fatalf("bad query %s returned %d", query, r.Code)
		}
	}
	if index.calls != 0 {
		t.Fatal("invalid inputs reached index")
	}
	r := get("/api/ai?from=2020-01-01&to=2020-02-01&person=alice&path=src&tool=Claude&evidence=all&offset=2&limit=5")
	if r.Code != 200 || strings.Contains(r.Body.String(), "<script>") || index.filter.Person != "alice" || index.filter.Path != "src" || index.filter.Offset != 2 || index.filter.Evidence != "all" {
		t.Fatalf("response=%s filter=%+v", r.Body, index.filter)
	}
	index.status.State = "indexing"
	if r = get("/api/ai"); r.Code != 202 || index.calls != 1 {
		t.Fatal("AI read partial index")
	}
}
