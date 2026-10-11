package explorer

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sderosiaux/git-contributor-insights/pkg/ownership"
)

type sourceReader interface {
	Source(context.Context, string, string) (*ownership.SourceFile, error)
}

func registerSourceAPI(mux *http.ServeMux, index IndexSource) {
	var mu sync.Mutex
	cache := map[string][]byte{}
	order := []string{}
	cacheBytes := 0
	mux.HandleFunc("GET /api/source", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if status := index.Status(); status.State != "ready" {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(status)
			return
		}
		source, supported := index.(sourceReader)
		if !supported {
			writeAPIError(w, http.StatusNotFound, "Source code is unavailable in this analysis.")
			return
		}
		sha, path := r.URL.Query().Get("sha"), r.URL.Query().Get("path")
		if !validRevisionSHA(sha) || !fs.ValidPath(path) || path == "." || strings.ContainsRune(path, 0) {
			writeAPIError(w, http.StatusBadRequest, "Choose an indexed revision and an included file.")
			return
		}
		key := sha + "\x00" + path
		mu.Lock()
		body := cache[key]
		mu.Unlock()
		if body != nil {
			writeDataset(w, r, body)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		file, err := source.Source(ctx, sha, path)
		if r.Context().Err() != nil {
			return
		}
		if err != nil {
			writeAPIError(w, http.StatusNotFound, "Could not read source code: "+err.Error())
			return
		}
		body, err = json.Marshal(file)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "Could not encode source code.")
			return
		}
		// Bound the cache by both file count and bytes. Actor changes reuse this
		// same immutable source; no new Git work is needed to change highlights.
		mu.Lock()
		if cache[key] == nil && len(body) <= 16<<20 {
			for len(order) >= 8 || cacheBytes+len(body) > 16<<20 {
				oldest := order[0]
				order = order[1:]
				cacheBytes -= len(cache[oldest])
				delete(cache, oldest)
			}
			cache[key] = body
			cacheBytes += len(body)
			order = append(order, key)
		}
		mu.Unlock()
		writeDataset(w, r, body)
	})
}
