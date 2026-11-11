package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gabbygtobar-coder/shipyard/internal/runtime"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

type urlResponse struct {
	URL string `json:"url"`
}

func (h *handler) appURL(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if err := ValidateName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	app, err := h.store.GetApp(r.Context(), name)
	if err != nil {
		h.writeStoreError(w, r, err, errAppNotFound)
		return
	}
	if app.URL == "" || (app.Status != store.AppStatusRunning && app.Status != store.AppStatusUnhealthy) {
		writeError(w, http.StatusConflict, runtime.ErrNotRunning.Error())
		return
	}
	writeJSON(w, http.StatusOK, urlResponse{URL: app.URL})
}

func (h *handler) stopApp(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if err := ValidateName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := h.store.GetApp(r.Context(), name); err != nil {
		h.writeStoreError(w, r, err, errAppNotFound)
		return
	}
	if h.run == nil {
		writeError(w, http.StatusServiceUnavailable, errRuntimeUnconfigured)
		return
	}
	if _, err := h.run.Stop(r.Context(), name); err != nil {
		if errors.Is(err, runtime.ErrNotRunning) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		h.writeStoreError(w, r, err, errAppNotFound)
		return
	}
	app, err := h.store.GetApp(r.Context(), name)
	if err != nil {
		h.writeStoreError(w, r, err, errAppNotFound)
		return
	}
	writeJSON(w, http.StatusOK, app)
}
