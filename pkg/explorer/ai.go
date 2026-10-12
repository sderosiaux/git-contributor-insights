package explorer

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/sderosiaux/git-contributor-insights/pkg/ownership"
)

type aiSource interface {
	AI(context.Context, ownership.AIFilter) (*ownership.AIReport, error)
}

func registerAIAPI(mux *http.ServeMux, index IndexSource) {
	mux.HandleFunc("GET /api/ai", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if status := index.Status(); status.State != "ready" {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(status)
			return
		}
		source, ok := index.(aiSource)
		if !ok {
			writeAPIError(w, http.StatusNotFound, "AI provenance needs the local indexed explorer.")
			return
		}
		q := r.URL.Query()
		filter := ownership.AIFilter{From: q.Get("from"), To: q.Get("to"), Person: q.Get("person"), Path: q.Get("path"), Tool: q.Get("tool"), Evidence: q.Get("evidence"), Limit: 30}
		for _, field := range []string{"offset", "limit"} {
			if !q.Has(field) {
				continue
			}
			n, err := strconv.Atoi(q.Get(field))
			if err != nil || n < 0 || (field == "limit" && (n < 1 || n > 200)) {
				writeAPIError(w, http.StatusBadRequest, "Choose a valid evidence page.")
				return
			}
			if field == "offset" {
				filter.Offset = n
			} else {
				filter.Limit = n
			}
		}
		for _, field := range []string{"from", "to"} {
			if q.Get(field) != "" {
				if _, err := parseRangeDate(q.Get(field), field == "to"); err != nil {
					writeAPIError(w, http.StatusBadRequest, "Choose valid dates.")
					return
				}
			}
		}
		data, err := source.AI(r.Context(), filter)
		if r.Context().Err() != nil {
			return
		}
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		body, err := json.Marshal(data)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "Could not encode AI provenance.")
			return
		}
		writeDataset(w, r, body)
	})
}
