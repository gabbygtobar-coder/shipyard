package api

import (
	"net/http"
)

type readyResponse struct {
	Status string   `json:"status"`
	Checks []string `json:"checks,omitempty"`
}

func (h *handler) readyz(w http.ResponseWriter, r *http.Request) {
	var failed []string
	if err := h.store.Ping(r.Context()); err != nil {
		h.logger(r.Context()).Error("ready store", "err", err)
		failed = append(failed, "store")
	}
	if h.ready != nil {
		if err := h.ready.Ready(r.Context()); err != nil {
			h.logger(r.Context()).Error("ready runtime", "err", err)
			failed = append(failed, "runtime")
		}
	}
	if len(failed) > 0 {
		writeJSON(w, http.StatusServiceUnavailable, readyResponse{Status: "not_ready", Checks: failed})
		return
	}
	writeJSON(w, http.StatusOK, readyResponse{Status: "ok"})
}
