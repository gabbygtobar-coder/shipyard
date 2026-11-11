// Package proxy routes HTTP requests to the container currently serving an app.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/route"
)

const (
	msgNoApp      = "no application in request\n"
	msgBadName    = "invalid application name\n"
	msgNotRunning = "app %s is not running\n"
	msgBadGateway = "bad gateway: app %s is unreachable\n"
)

var appPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

type ctxKey int

const (
	ctxApp ctxKey = iota
	ctxPath
)

// Handler is the reverse proxy. It logs app, status, and latency for every request.
type Handler struct {
	routes *route.Table
	proxy  *httputil.ReverseProxy
	log    *slog.Logger
}

// New returns a proxy that reads targets from routes.
// A nil logger discards log output. A nil table routes nothing.
func New(routes *route.Table, logger *slog.Logger) *Handler {
	if routes == nil {
		routes = route.NewTable()
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	h := &Handler{routes: routes, log: logger}
	h.proxy = &httputil.ReverseProxy{
		Rewrite:      h.rewrite,
		ErrorHandler: h.proxyError,
		Transport:    proxyTransport(),
	}
	return h
}

// ServeHTTP routes by Host `<app>.localhost` or by the path `/apps/<app>/`.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	sw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	app := ""
	defer func() {
		rec := recover()
		if rec != nil {
			h.log.Error("proxy panic", "err", fmt.Sprint(rec), "stack", string(debug.Stack()))
			if !sw.wrote {
				http.Error(sw, "internal error\n", http.StatusInternalServerError)
			}
		}
		h.log.Info("proxy",
			"app", app,
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"latency_ms", time.Since(start).Milliseconds(),
		)
	}()

	upstream := r.URL.Path
	if upstream == "" {
		upstream = "/"
	}
	if name, ok := appFromHost(r.Host); ok {
		app = name
	} else if name, path, ok := appFromPath(r.URL.Path); ok {
		app = name
		upstream = path
	} else if strings.HasPrefix(r.URL.Path, "/apps/") {
		http.Error(sw, msgBadName, http.StatusBadRequest)
		return
	} else {
		http.Error(sw, msgNoApp, http.StatusNotFound)
		return
	}

	target, ok := h.routes.Lookup(app)
	if !ok || target.Endpoint == "" {
		http.Error(sw, fmt.Sprintf(msgNotRunning, app), http.StatusServiceUnavailable)
		return
	}
	parsed, err := url.Parse(target.Endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		http.Error(sw, fmt.Sprintf(msgBadGateway, app), http.StatusBadGateway)
		return
	}
	ctx := context.WithValue(r.Context(), ctxApp, app)
	ctx = context.WithValue(ctx, ctxPath, upstream)
	ctx = context.WithValue(ctx, targetKey{}, parsed)
	h.proxy.ServeHTTP(sw, r.WithContext(ctx))
}

type targetKey struct{}

func (h *Handler) rewrite(pr *httputil.ProxyRequest) {
	target, _ := pr.In.Context().Value(targetKey{}).(*url.URL)
	path, _ := pr.In.Context().Value(ctxPath).(string)
	if target == nil {
		return
	}
	pr.SetURL(target)
	if path == "" {
		path = "/"
	}
	pr.Out.URL.Path = path
	pr.Out.URL.RawPath = ""
	pr.Out.Host = target.Host
}

func (h *Handler) proxyError(w http.ResponseWriter, r *http.Request, err error) {
	app, _ := r.Context().Value(ctxApp).(string)
	h.log.Error("proxy upstream", "app", app, "err", err)
	http.Error(w, fmt.Sprintf(msgBadGateway, app), http.StatusBadGateway)
}

// PublicURL is the canonical URL for an app on the proxy listener.
// proxyAddr is a host:port listen address such as ":8081".
func PublicURL(proxyAddr, app string) (string, error) {
	if !validApp(app) {
		return "", errors.New("invalid application name")
	}
	_, port, err := net.SplitHostPort(proxyAddr)
	if err != nil {
		return "", fmt.Errorf("proxy address: %w", err)
	}
	if _, err := strconv.Atoi(port); err != nil {
		return "", fmt.Errorf("proxy port: %w", err)
	}
	if port == "80" {
		return "http://" + app + ".localhost/", nil
	}
	return "http://" + app + ".localhost:" + port + "/", nil
}

func appFromHost(hostport string) (string, bool) {
	host := hostport
	if parsed, _, err := net.SplitHostPort(hostport); err == nil {
		host = parsed
	}
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	name, ok := strings.CutSuffix(host, ".localhost")
	if !ok || name == "" || strings.Contains(name, ".") || !validApp(name) {
		return "", false
	}
	return name, true
}

func appFromPath(p string) (string, string, bool) {
	rest, ok := strings.CutPrefix(p, "/apps/")
	if !ok || rest == "" {
		return "", "", false
	}
	name, tail, found := strings.Cut(rest, "/")
	if !validApp(name) {
		return "", "", false
	}
	if !found || tail == "" {
		return name, "/", true
	}
	for _, seg := range strings.Split(tail, "/") {
		if seg == ".." {
			return "", "", false
		}
	}
	return name, "/" + tail, true
}

func validApp(name string) bool {
	return appPattern.MatchString(name) && !strings.Contains(name, "--") && !strings.HasSuffix(name, "-")
}

func proxyTransport() http.RoundTripper {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultTransport
	}
	clone := base.Clone()
	clone.ResponseHeaderTimeout = 30 * time.Second
	clone.IdleConnTimeout = 90 * time.Second
	return clone
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
