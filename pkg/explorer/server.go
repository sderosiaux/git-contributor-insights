// Package explorer serves the local, read-only ownership explorer.
package explorer

import (
	"compress/gzip"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sderosiaux/git-contributor-insights/pkg/ownership"
)

//go:embed assets/*
var assets embed.FS

const maxPatchBytes = 200_000

// RangeBuilder refines a calendar period against the same frozen repository HEAD.
type RangeBuilder func(context.Context, string, string) (*ownership.Dataset, error)

// NewHandler serves a read-only analysis, optionally refining periods on demand.
func NewHandler(data *ownership.Dataset, builders ...RangeBuilder) (http.Handler, error) {
	var buildRange RangeBuilder
	if len(builders) > 0 {
		buildRange = builders[0]
	}
	payload, err := encodeDataset(data, buildRange != nil)
	if err != nil {
		return nil, fmt.Errorf("encode ownership: %w", err)
	}
	return newWorkspaceHandler(func(mux *http.ServeMux, evidence *evidenceRegistry) {
		evidence.register(data)
		mux.HandleFunc("GET /api/ownership", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			query := r.URL.Query()
			if !query.Has("from") && !query.Has("to") {
				writeDataset(w, r, payload)
				return
			}
			if buildRange == nil {
				writeAPIError(w, http.StatusBadRequest, "This exported analysis cannot calculate additional dates.")
				return
			}
			from, fromErr := parseRangeDate(query.Get("from"), false)
			to, toErr := parseRangeDate(query.Get("to"), true)
			if fromErr != nil || toErr != nil || from.After(to) {
				writeAPIError(w, http.StatusBadRequest, "Choose a valid start and end date, in chronological order.")
				return
			}
			first, firstErr := parseRangeDate(data.Timeline.Since, false)
			last, lastErr := parseRangeDate(data.Timeline.Until, true)
			if firstErr != nil || lastErr != nil || from.Before(first.Truncate(24*time.Hour)) || !to.Before(last.Truncate(24*time.Hour).AddDate(0, 0, 1)) {
				writeAPIError(w, http.StatusBadRequest, "Choose dates within the analyzed history.")
				return
			}
			refined, err := buildRange(r.Context(), from.Format(time.RFC3339Nano), to.Format(time.RFC3339Nano))
			if r.Context().Err() != nil {
				return
			}
			if err != nil {
				writeAPIError(w, http.StatusInternalServerError, "Could not calculate this period: "+err.Error())
				return
			}
			if refined.Repository.Head != data.Repository.Head {
				writeAPIError(w, http.StatusInternalServerError, "The period does not belong to this analysis revision.")
				return
			}
			body, err := encodeDataset(refined, true)
			if err != nil {
				writeAPIError(w, http.StatusInternalServerError, "Could not encode this period.")
				return
			}
			evidence.register(refined)
			writeDataset(w, r, body)
		})
	})
}

// A proof is readable only after a returned dataset has established its exact
// commit/path pair. Keep the repository alongside it for deferred index loading.
type evidenceRegistry struct {
	mu      sync.RWMutex
	allowed map[string]string
}

func (e *evidenceRegistry) register(dataset *ownership.Dataset) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, snapshot := range dataset.Snapshots {
		for _, file := range snapshot.Files {
			for _, contribution := range file.Contributions {
				for _, proof := range contribution.Evidence {
					path := proof.Path
					if path == "" {
						path = file.Path
					}
					e.allowed[proof.SHA+"\x00"+path] = dataset.Repository.Path
				}
			}
		}
	}
}

func newWorkspaceHandler(registerAPI func(*http.ServeMux, *evidenceRegistry)) (http.Handler, error) {
	static, err := fs.Sub(assets, "assets")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	evidence := &evidenceRegistry{allowed: make(map[string]string)}
	registerAPI(mux, evidence)
	mux.HandleFunc("GET /api/commit", func(w http.ResponseWriter, r *http.Request) {
		sha, path := r.URL.Query().Get("sha"), r.URL.Query().Get("path")
		evidence.mu.RLock()
		repo := evidence.allowed[sha+"\x00"+path]
		evidence.mu.RUnlock()
		if repo == "" {
			http.Error(w, "This commit and path are not part of the analysis.", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", "--no-pager", "--literal-pathspecs", "-c", "log.showSignature=false", "-C", repo,
			"show", "--no-ext-diff", "--no-textconv", "--format=fuller", "--no-color", "--find-renames", sha, "--", path)
		var output cappedBuffer
		cmd.Stdout = &output
		if err := cmd.Run(); err != nil {
			http.Error(w, "Could not read the commit from the local repository.", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(struct {
			SHA       string `json:"sha"`
			Path      string `json:"path"`
			Patch     string `json:"patch"`
			Truncated bool   `json:"truncated"`
		}{sha, path, output.String(), output.truncated})
	})
	files := http.FileServer(http.FS(static))
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/overview", "/codebase", "/evolution", "/history":
			// Each workspace is a real entry point, including on a hard refresh.
			page := r.Clone(r.Context())
			page.URL.Path = "/"
			page.URL.RawPath = ""
			files.ServeHTTP(w, page)
		case "/overview/", "/codebase/", "/evolution/", "/history/":
			target := strings.TrimSuffix(r.URL.Path, "/")
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusPermanentRedirect)
		case "/app.js", "/routes.js", "/timeline.js", "/source.js", "/styles.css":
			files.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The explorer is intentionally local, with no cross-origin API access.
		host := r.Host
		if value, _, err := net.SplitHostPort(host); err == nil {
			host = value
		}
		ip := net.ParseIP(strings.Trim(host, "[]"))
		if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
			http.Error(w, "Use the localhost address printed by ghca.", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
		mux.ServeHTTP(w, r)
	}), nil
}

func encodeDataset(data *ownership.Dataset, adaptive bool) ([]byte, error) {
	encoding := data.SeriesEncoding
	series := data.Series
	if encoding == "" && len(series) > 0 {
		series = compactSeries(series)
		encoding = "changes-v1"
	}
	return json.Marshal(struct {
		*ownership.Dataset
		Series         []ownership.SeriesPoint `json:"series,omitempty"`
		SeriesEncoding string                  `json:"seriesEncoding,omitempty"`
		Timeline       struct {
			ownership.Timeline
			Adaptive bool `json:"adaptive"`
		} `json:"timeline"`
	}{Dataset: data, Series: series, SeriesEncoding: encoding, Timeline: struct {
		ownership.Timeline
		Adaptive bool `json:"adaptive"`
	}{data.Timeline, adaptive}})
}

// Complete histories can contain thousands of states. Compress transport without
// dropping points or changing the JSON model seen by the browser.
func writeDataset(w http.ResponseWriter, r *http.Request, body []byte) {
	w.Header().Add("Vary", "Accept-Encoding")
	if len(body) > 4096 {
		for _, encoding := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
			parts := strings.Split(strings.TrimSpace(encoding), ";")
			if !strings.EqualFold(strings.TrimSpace(parts[0]), "gzip") {
				continue
			}
			quality := 1.0
			for _, option := range parts[1:] {
				if key, value, ok := strings.Cut(strings.TrimSpace(option), "="); ok && key == "q" {
					quality, _ = strconv.ParseFloat(value, 64)
				}
			}
			if quality <= 0 {
				break
			}
			w.Header().Set("Content-Encoding", "gzip")
			writer, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
			_, _ = writer.Write(body)
			_ = writer.Close()
			return
		}
	}
	_, _ = w.Write(body)
}

func writeAPIError(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func parseRangeDate(value string, endOfDay bool) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse("2006-01-02", value)
	if err == nil && endOfDay {
		t = t.Add(24*time.Hour - time.Nanosecond)
	}
	return t, err
}

type cappedBuffer struct {
	strings.Builder
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := maxPatchBytes - b.Len()
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	_, _ = b.Builder.Write(p)
	return n, nil
}
