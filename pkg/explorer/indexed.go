package explorer

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sderosiaux/git-contributor-insights/pkg/ownership"
)

// IndexSource separates HTTP requests from the persistent indexing lifecycle.
type IndexSource interface {
	Status() ownership.IndexStatus
	Query(context.Context, string, string, string) (*ownership.Dataset, error)
}

type revisionSource interface {
	Revision(context.Context, string) (*ownership.Dataset, error)
}

type historySource interface {
	QueryHistory(context.Context, string, string, string, string) (*ownership.Dataset, error)
}

type compactHistorySource interface {
	QueryCompactHistory(context.Context, string, string, string, string) (*ownership.Dataset, error)
}

type commitSource interface {
	Commits(context.Context, ownership.CommitFilter) (ownership.CommitPage, error)
}

func validRevisionSHA(sha string) bool {
	_, err := hex.DecodeString(sha)
	return err == nil && (len(sha) == 40 || len(sha) == 64)
}

// NewIndexedHandler serves the workspace immediately while the durable index is
// built. Queries never substitute a partial or sampled history for an index.
func NewIndexedHandler(index IndexSource, from, to string, retry func(), options ...HandlerOptions) (http.Handler, error) {
	var initial *ownership.Dataset
	var initialPayload []byte
	var initialMu sync.Mutex
	historyBounds := map[string]ownership.Timeline{}
	return newWorkspaceHandler(func(mux *http.ServeMux, evidence *evidenceRegistry) {
		registerSourceAPI(mux, index)
		registerAIAPI(mux, index)
		mux.HandleFunc("GET /api/index", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_ = json.NewEncoder(w).Encode(index.Status())
		})
		mux.HandleFunc("POST /api/index/retry", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			if origin := r.Header.Get("Origin"); origin != "" {
				parsed, err := url.Parse(origin)
				if err != nil || parsed.Host != r.Host || parsed.Scheme != "http" {
					writeAPIError(w, http.StatusForbidden, "Retry indexing from the local workspace.")
					return
				}
			}
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				writeAPIError(w, http.StatusForbidden, "Retry indexing from the local workspace.")
				return
			}
			if retry != nil && index.Status().State == "error" {
				retry()
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(index.Status())
		})
		mux.HandleFunc("GET /api/revision", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			status := index.Status()
			if status.State != "ready" {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusAccepted)
				_ = json.NewEncoder(w).Encode(status)
				return
			}
			source, supported := index.(revisionSource)
			if !supported {
				writeAPIError(w, http.StatusNotFound, "Exact revisions are unavailable in this analysis.")
				return
			}
			sha := r.URL.Query().Get("sha")
			if !validRevisionSHA(sha) {
				writeAPIError(w, http.StatusBadRequest, "Choose a full indexed revision SHA.")
				return
			}
			dataset, err := source.Revision(r.Context(), sha)
			if r.Context().Err() != nil {
				return
			}
			if err != nil {
				writeAPIError(w, http.StatusNotFound, "Could not read this indexed revision: "+err.Error())
				return
			}
			if dataset.Repository.Head != status.Head {
				writeAPIError(w, http.StatusInternalServerError, "The revision does not belong to this index.")
				return
			}
			body, err := encodeDataset(dataset, true)
			if err != nil {
				writeAPIError(w, http.StatusInternalServerError, "Could not encode this revision.")
				return
			}
			evidence.register(dataset)
			writeDataset(w, r, body)
		})
		mux.HandleFunc("GET /api/commits", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			status := index.Status()
			if status.State != "ready" {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusAccepted)
				_ = json.NewEncoder(w).Encode(status)
				return
			}
			source, supported := index.(commitSource)
			if !supported {
				writeAPIError(w, http.StatusNotFound, "The commit catalogue is unavailable in this analysis.")
				return
			}
			query := r.URL.Query()
			filter := ownership.CommitFilter{Query: query.Get("q"), From: query.Get("from"), To: query.Get("to"), Limit: 50}
			for _, param := range []string{"offset", "limit"} {
				if !query.Has(param) {
					continue
				}
				value, err := strconv.Atoi(query.Get(param))
				if err != nil || value < 0 || (param == "limit" && (value < 1 || value > 200)) {
					writeAPIError(w, http.StatusBadRequest, "Choose a nonnegative offset and a page size between 1 and 200.")
					return
				}
				if param == "offset" {
					filter.Offset = value
				} else {
					filter.Limit = value
				}
			}
			var start, end time.Time
			var err error
			if filter.From != "" {
				start, err = parseRangeDate(filter.From, false)
				if err != nil {
					writeAPIError(w, http.StatusBadRequest, "Choose a valid commit start date.")
					return
				}
			}
			if filter.To != "" {
				end, err = parseRangeDate(filter.To, true)
				if err != nil {
					writeAPIError(w, http.StatusBadRequest, "Choose a valid commit end date.")
					return
				}
			}
			if !start.IsZero() && !end.IsZero() && start.After(end) {
				writeAPIError(w, http.StatusBadRequest, "Choose commit dates in chronological order.")
				return
			}
			page, err := source.Commits(r.Context(), filter)
			if r.Context().Err() != nil {
				return
			}
			if err != nil {
				writeAPIError(w, http.StatusInternalServerError, "Could not read indexed commits: "+err.Error())
				return
			}
			body, err := json.Marshal(page)
			if err != nil {
				writeAPIError(w, http.StatusInternalServerError, "Could not encode indexed commits.")
				return
			}
			writeDataset(w, r, body)
		})
		mux.HandleFunc("GET /api/ownership", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			status := index.Status()
			if status.State != "ready" {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusAccepted)
				_ = json.NewEncoder(w).Encode(status)
				return
			}
			query := r.URL.Query()
			tip := query.Get("tip")
			if tip == status.Head {
				tip = ""
			}
			queryHistory := index.Query
			if source, supported := index.(compactHistorySource); supported {
				queryHistory = func(ctx context.Context, start, end, scope string) (*ownership.Dataset, error) {
					return source.QueryCompactHistory(ctx, start, end, scope, tip)
				}
			}
			if tip != "" {
				if !validRevisionSHA(tip) {
					writeAPIError(w, http.StatusBadRequest, "Choose a full indexed commit SHA for this history.")
					return
				}
				source, supported := index.(historySource)
				if !supported {
					writeAPIError(w, http.StatusNotFound, "Branch histories are unavailable in this analysis.")
					return
				}
				queryHistory = func(ctx context.Context, start, end, scope string) (*ownership.Dataset, error) {
					if compact, ok := index.(compactHistorySource); ok {
						return compact.QueryCompactHistory(ctx, start, end, scope, tip)
					}
					return source.QueryHistory(ctx, start, end, scope, tip)
				}
			}
			// Cache compact HTTP bytes and bounds, not a dense history containing
			// millions of repeated contribution objects.
			initialMu.Lock()
			var base *ownership.Dataset
			if tip == "" {
				base = initial
			} else if bounds, found := historyBounds[tip]; found {
				base = &ownership.Dataset{Timeline: bounds}
			}
			if base == nil {
				initialFrom, initialTo := from, to
				if tip != "" {
					// An explicitly selected branch may predate the default view's
					// CLI range. Its full history must remain explorable.
					initialFrom, initialTo = "", ""
				}
				dataset, err := queryHistory(r.Context(), initialFrom, initialTo, "")
				if err != nil {
					initialMu.Unlock()
					if r.Context().Err() == nil {
						code := http.StatusInternalServerError
						if tip != "" {
							code = http.StatusBadRequest
						}
						writeAPIError(w, code, "Could not read this history: "+err.Error())
					}
					return
				}
				if dataset.Repository.Head != status.Head {
					initialMu.Unlock()
					writeAPIError(w, http.StatusInternalServerError, "The history does not belong to this index revision.")
					return
				}
				base = dataset
				if tip == "" {
					body, err := encodeDataset(dataset, true)
					if err != nil {
						initialMu.Unlock()
						writeAPIError(w, http.StatusInternalServerError, "Could not encode this history.")
						return
					}
					evidence.register(dataset)
					initial = &ownership.Dataset{Timeline: dataset.Timeline}
					initialPayload = body
				} else {
					historyBounds[tip] = dataset.Timeline
				}
			}
			initialMu.Unlock()
			selectedFrom, selectedTo := base.Timeline.Since, base.Timeline.Until
			if query.Has("from") || query.Has("to") {
				start, startErr := parseRangeDate(query.Get("from"), false)
				end, endErr := parseRangeDate(query.Get("to"), true)
				first, firstErr := parseRangeDate(base.Timeline.Since, false)
				last, lastErr := parseRangeDate(base.Timeline.Until, true)
				if startErr != nil || endErr != nil || firstErr != nil || lastErr != nil || start.After(end) || start.Before(first.Truncate(24*time.Hour)) || !end.Before(last.Truncate(24*time.Hour).AddDate(0, 0, 1)) {
					writeAPIError(w, http.StatusBadRequest, "Choose valid dates within the indexed history, in chronological order.")
					return
				}
				selectedFrom, selectedTo = start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)
			}
			scope := query.Get("path")
			if strings.ContainsRune(scope, 0) || strings.HasPrefix(scope, "/") || scope == ".." || strings.HasPrefix(scope, "../") || strings.Contains(scope, "/../") || strings.HasSuffix(scope, "/..") {
				writeAPIError(w, http.StatusBadRequest, "Choose a path within this repository.")
				return
			}
			if tip == "" && scope == "" && selectedFrom == base.Timeline.Since && selectedTo == base.Timeline.Until {
				initialMu.Lock()
				body := initialPayload
				initialMu.Unlock()
				writeDataset(w, r, body)
				return
			}
			dataset := base
			if base.Repository.Head == "" || scope != "" || selectedFrom != base.Timeline.Since || selectedTo != base.Timeline.Until {
				var err error
				dataset, err = queryHistory(r.Context(), selectedFrom, selectedTo, scope)
				if r.Context().Err() != nil {
					return
				}
				if err != nil {
					writeAPIError(w, http.StatusInternalServerError, "Could not read this period: "+err.Error())
					return
				}
			}
			if dataset.Repository.Head != status.Head {
				writeAPIError(w, http.StatusInternalServerError, "The period does not belong to this index revision.")
				return
			}
			body, err := encodeDataset(dataset, true)
			if err != nil {
				writeAPIError(w, http.StatusInternalServerError, "Could not encode this period.")
				return
			}
			evidence.register(dataset)
			writeDataset(w, r, body)
		})
	}, options...)
}
