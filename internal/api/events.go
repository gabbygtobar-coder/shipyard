package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

type eventListResponse struct {
	Events []store.Event `json:"events"`
}

func (h *handler) listEvents(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if err := ValidateName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	limit, ok := eventLimit(w, r)
	if !ok {
		return
	}
	events, err := h.store.ListEvents(r.Context(), name, limit)
	if err != nil {
		h.writeStoreError(w, r, err, errAppNotFound)
		return
	}
	if events == nil {
		events = []store.Event{}
	}
	writeJSON(w, http.StatusOK, eventListResponse{Events: events})
}

func eventLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > store.MaxEventLimit {
		writeError(w, http.StatusBadRequest, "limit must be from 1 to 500")
		return 0, false
	}
	return n, true
}
