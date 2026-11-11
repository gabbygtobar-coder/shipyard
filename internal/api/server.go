// Package api implements the shipyard HTTP API.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/deploy"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

const (
	contentTypeJSON = "application/json; charset=utf-8"
	maxBodyBytes    = 1 << 20

	errAppNotFound  = "app not found"
	errAppExists    = "app already exists"
	errInternal     = "internal error"
	errInvalidJSON  = "invalid JSON body"
	errBodyRequired = "request body is required"
	errBodyTooLarge = "request body too large"
	errContentType  = "content type must be application/json"
	errCanceled     = "request canceled"

	errDeploymentNotFound = "deployment not found"
	errInvalidDeployment  = "invalid deployment"
	errBuildsUnconfigured = "builds are not configured"
)

// handler serves the shipyard HTTP API.
type handler struct {
	store  store.Store
	builds deploy.BuildService
	log    *slog.Logger
}

// NewHandler returns the shipyard HTTP API.
// st must be non-nil. builds may be nil, in which case deployment creation
// returns 503 and the read routes still serve records already in st.
// A nil logger discards log output.
func NewHandler(st store.Store, builds deploy.BuildService, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	h := &handler{store: st, builds: builds, log: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("POST /apps", h.createApp)
	mux.HandleFunc("GET /apps", h.listApps)
	mux.HandleFunc("GET /apps/{name}", h.getApp)
	mux.HandleFunc("POST /apps/{name}/deployments", h.createDeployment)
	mux.HandleFunc("GET /apps/{name}/deployments", h.listDeployments)
	mux.HandleFunc("GET /deployments/{id}", h.getDeployment)
	mux.HandleFunc("GET /deployments/{id}/logs", h.deploymentLogs)
	return h.wrap(mux)
}

func (h *handler) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			rec := recover()
			abort := false
			if rec != nil {
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					abort = true
				} else {
					h.log.Error("panic",
						"err", fmt.Sprint(rec),
						"method", r.Method,
						"path", r.URL.Path,
						"stack", string(debug.Stack()),
					)
					writeError(sw, http.StatusInternalServerError, errInternal)
				}
			}
			h.log.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"duration_ms", time.Since(start).Milliseconds(),
			)
			if abort {
				panic(rec)
			}
		}()
		next.ServeHTTP(sw, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.wrote {
		return
	}
	s.status = code
	s.wrote = true
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.status = http.StatusOK
		s.wrote = true
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}

type errorBody struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", contentTypeJSON)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	if err := enc.Encode(v); err != nil {
		// The status is already committed; the request log records the handler status.
		return
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}
