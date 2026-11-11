// Package api implements the shipyard HTTP API.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/deploy"
	"github.com/gabbygtobar-coder/shipyard/internal/metrics"
	"github.com/gabbygtobar-coder/shipyard/internal/requestid"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

const (
	contentTypeJSON = "application/json; charset=utf-8"
	mediaTypeJSON   = "application/json"
	maxBodyBytes    = 1 << 20

	errAppNotFound  = "app not found"
	errAppExists    = "app already exists"
	errInternal     = "internal error"
	errInvalidJSON  = "invalid JSON body"
	errBodyRequired = "request body is required"
	errBodyTooLarge = "request body too large"
	errContentType  = "content type must be application/json"
	errMultipleJSON = "multiple JSON values"
	errCanceled     = "request canceled"

	errDeploymentNotFound  = "deployment not found"
	errInvalidDeployment   = "invalid deployment"
	errBuildsUnconfigured  = "builds are not configured"
	errRuntimeUnconfigured = "runtime is not configured"
)

// AppRuntime stops the container that is receiving traffic for an app.
type AppRuntime interface {
	Stop(ctx context.Context, appName string) (deploymentID string, err error)
}

// ReadyCheck reports whether a dependency can take work.
type ReadyCheck interface {
	Ready(ctx context.Context) error
}

type loggerKey struct{}

// handler serves the shipyard HTTP API.
type handler struct {
	store         store.Store
	builds        deploy.BuildService
	run           AppRuntime
	log           *slog.Logger
	webhookSecret string
	metrics       *metrics.Metrics
	ready         ReadyCheck
	uiToken       string
}

// Option configures the HTTP API.
type Option func(*handler)

// WithWebhookSecret sets the global GitHub webhook HMAC secret.
// The secret is not logged.
func WithWebhookSecret(secret string) Option {
	return func(h *handler) {
		h.webhookSecret = secret
	}
}

// WithMetrics records API and webhook metrics and serves GET /metrics.
// A nil value leaves metrics disabled.
func WithMetrics(m *metrics.Metrics) Option {
	return func(h *handler) {
		h.metrics = m
	}
}

// WithReadyCheck makes GET /readyz call check after the store ping.
// A nil check only pings the store.
func WithReadyCheck(check ReadyCheck) Option {
	return func(h *handler) {
		h.ready = check
	}
}

// WithUIToken enables the dashboard at /ui.
// Requests must send the token as an HTTP Basic password or a Bearer token.
// An empty token leaves the dashboard refusing every request.
func WithUIToken(token string) Option {
	return func(h *handler) {
		h.uiToken = token
	}
}

// NewHandler returns the shipyard HTTP API.
// st must be non-nil. builds may be nil, in which case deployment creation
// returns 503 and the read routes still serve records already in st.
// run may be nil, in which case stop returns 503.
// A nil logger discards log output.
func NewHandler(st store.Store, builds deploy.BuildService, logger *slog.Logger, run AppRuntime, opts ...Option) http.Handler {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	h := &handler{store: st, builds: builds, run: run, log: logger}
	for _, opt := range opts {
		if opt != nil {
			opt(h)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /readyz", h.readyz)
	if h.metrics != nil {
		mux.Handle("GET /metrics", h.metrics.Handler())
	}
	mux.HandleFunc("GET /ui", h.uiIndex)
	mux.HandleFunc("GET /ui/{$}", h.uiIndex)
	mux.HandleFunc("GET /ui/apps/{name}", h.uiApp)
	mux.HandleFunc("POST /ui/apps/{name}/rollback", h.uiRollback)
	mux.HandleFunc("POST /webhooks/github", h.githubWebhook)
	mux.HandleFunc("POST /apps", h.createApp)
	mux.HandleFunc("GET /apps", h.listApps)
	mux.HandleFunc("PATCH /apps/{name}", h.updateApp)
	mux.HandleFunc("GET /apps/{name}/url", h.appURL)
	mux.HandleFunc("POST /apps/{name}/stop", h.stopApp)
	mux.HandleFunc("POST /apps/{name}/rollback", h.rollbackApp)
	mux.HandleFunc("GET /apps/{name}/releases", h.listReleases)
	mux.HandleFunc("GET /apps/{name}/events", h.listEvents)
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
		id := requestid.FromRequest(r)
		logger := requestid.Logger(h.log, id)
		ctx := requestid.With(r.Context(), id)
		ctx = context.WithValue(ctx, loggerKey{}, logger)
		r = r.WithContext(ctx)
		w.Header().Set(requestid.Header, id)
		sw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			rec := recover()
			abort := false
			if rec != nil {
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					abort = true
				} else {
					logger.Error("panic",
						"err", fmt.Sprint(rec),
						"method", r.Method,
						"path", r.URL.Path,
						"stack", string(debug.Stack()),
					)
					writeError(sw, http.StatusInternalServerError, errInternal)
				}
			}
			logger.Info("request",
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

func (h *handler) logger(ctx context.Context) *slog.Logger {
	if ctx != nil {
		if logger, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok && logger != nil {
			return logger
		}
	}
	return h.log
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
