package explorer

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sderosiaux/git-contributor-insights/pkg/ownership"
)

type testSourceIndex struct {
	testIndex
	calls int
}

func (index *testSourceIndex) Source(ctx context.Context, sha, path string) (*ownership.SourceFile, error) {
	index.calls++
	if path == "missing" {
		return nil, fmt.Errorf("absent at this revision")
	}
	return &ownership.SourceFile{Revision: sha, Path: path, Lines: []string{"<script>code</script>"}, Blocks: []ownership.SourceBlock{{Start: 1, End: 1, SHA: sha, PersonID: "alice", OrganizationID: "Old"}}}, nil
}

func TestSourceAPIValidatesAndCachesExactFiles(t *testing.T) {
	index := &testSourceIndex{testIndex: testIndex{status: ownership.IndexStatus{State: "ready"}}}
	handler, err := NewIndexedHandler(index, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	request := func(sha, path string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", "http://localhost:8787/api/source?"+url.Values{"sha": {sha}, "path": {path}}.Encode(), nil))
		return response
	}
	sha := strings.Repeat("a", 40)
	for _, sample := range [][2]string{{"HEAD", "config.properties"}, {sha, "../file"}, {sha, "/etc/passwd"}, {sha, "."}, {sha, ""}, {sha, "file\x00"}} {
		if response := request(sample[0], sample[1]); response.Code != 400 {
			t.Fatalf("invalid input: %d %s", response.Code, response.Body)
		}
	}
	if index.calls != 0 {
		t.Fatal("invalid requests reached Git")
	}
	for n := 0; n < 2; n++ {
		response := request(sha, "config.properties")
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"revision":"`+sha+`"`) {
			t.Fatalf("%d: %s", response.Code, response.Body)
		}
		if strings.Contains(response.Body.String(), "<script>") {
			t.Fatal("JSON source was not escaped")
		}
	}
	if index.calls != 1 {
		t.Fatal("repeated highlight queries should reuse source")
	}
	if response := request(strings.Repeat("b", 40), "config.properties"); response.Code != 200 || index.calls != 2 {
		t.Fatal("different revision reused cached code")
	}
	if response := request(sha, "missing"); response.Code != 404 {
		t.Fatal("absent file must not return a source")
	}
	index.status.State = "indexing"
	if response := request(sha, "config.properties"); response.Code != 202 {
		t.Fatal("incomplete index served code")
	}
}
