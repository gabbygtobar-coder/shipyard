package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

const jsonContentType = "application/json"

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	st := store.NewMemoryWithClock(func() time.Time {
		return time.Date(2026, 11, 11, 3, 4, 5, 0, time.UTC)
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewHandler(st, nil, logger)
}

func doRequest(h http.Handler, method, path, body, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), jsonContentType) {
		t.Fatalf("content type %q", rec.Header().Get("Content-Type"))
	}
	return body.Error
}

func TestHealthz(t *testing.T) {
	t.Parallel()
	h := newTestHandler(t)
	rec := doRequest(h, http.MethodGet, "/healthz", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var body healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "ok" {
		t.Fatalf("status body %#v", body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache control %q", rec.Header().Get("Cache-Control"))
	}
}

func TestCreateListGetFlow(t *testing.T) {
	t.Parallel()
	h := newTestHandler(t)

	rec := doRequest(h, http.MethodGet, "/apps", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status %d body %s", rec.Code, rec.Body.String())
	}
	var empty appListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &empty); err != nil {
		t.Fatal(err)
	}
	if empty.Apps == nil || len(empty.Apps) != 0 || strings.Contains(rec.Body.String(), "null") {
		t.Fatalf("empty list %#v body %s", empty.Apps, rec.Body.String())
	}

	body := `{"name":"  demo  ","repo_url":"https://github.com/acme/demo.git"}`
	rec = doRequest(h, http.MethodPost, "/apps", body, jsonContentType+"; charset=utf-8")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status %d body %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Location") != "/apps/demo" {
		t.Fatalf("location %q", rec.Header().Get("Location"))
	}
	var created store.App
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	wantTime := time.Date(2026, 11, 11, 3, 4, 5, 0, time.UTC)
	if created.Name != "demo" || created.RepoURL != "https://github.com/acme/demo" || !created.CreatedAt.Equal(wantTime) {
		t.Fatalf("created %#v", created)
	}

	rec = doRequest(h, http.MethodPost, "/apps", `{"name":"api","repo_url":"https://github.com/acme/api"}`, jsonContentType)
	if rec.Code != http.StatusCreated {
		t.Fatalf("second create %d %s", rec.Code, rec.Body.String())
	}

	rec = doRequest(h, http.MethodGet, "/apps", "", "")
	var list appListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Apps) != 2 || list.Apps[0].Name != "demo" || list.Apps[1].Name != "api" {
		t.Fatalf("list %#v", list.Apps)
	}

	rec = doRequest(h, http.MethodGet, "/apps/demo", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get %d %s", rec.Code, rec.Body.String())
	}
	var got store.App
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != created.Name || got.RepoURL != created.RepoURL || !got.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("get %#v", got)
	}

	rec = doRequest(h, http.MethodPost, "/apps", `{"name":"demo","repo_url":"https://github.com/acme/other"}`, jsonContentType)
	if rec.Code != http.StatusConflict || decodeError(t, rec) != errAppExists {
		t.Fatalf("duplicate %d %s", rec.Code, rec.Body.String())
	}
}

func TestCreateAppRejectedInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        string
		contentType string
		wantStatus  int
		wantMessage string
	}{
		{name: "bad name", body: `{"name":"Demo","repo_url":"https://github.com/acme/demo"}`, contentType: jsonContentType, wantStatus: http.StatusBadRequest, wantMessage: ErrNameInvalid.Error()},
		{name: "missing name", body: `{"name":"  ","repo_url":"https://github.com/acme/demo"}`, contentType: jsonContentType, wantStatus: http.StatusBadRequest, wantMessage: ErrNameRequired.Error()},
		{name: "bad url", body: `{"name":"demo","repo_url":"http://github.com/acme/demo"}`, contentType: jsonContentType, wantStatus: http.StatusBadRequest, wantMessage: ErrRepoInvalid.Error()},
		{name: "missing url", body: `{"name":"demo","repo_url":""}`, contentType: jsonContentType, wantStatus: http.StatusBadRequest, wantMessage: ErrRepoRequired.Error()},
		{name: "unknown field", body: `{"name":"demo","repo_url":"https://github.com/acme/demo","extra":true}`, contentType: jsonContentType, wantStatus: http.StatusBadRequest, wantMessage: errInvalidJSON},
		{name: "trailing value", body: `{"name":"demo","repo_url":"https://github.com/acme/demo"}{}`, contentType: jsonContentType, wantStatus: http.StatusBadRequest, wantMessage: errInvalidJSON},
		{name: "syntax", body: `{`, contentType: jsonContentType, wantStatus: http.StatusBadRequest, wantMessage: errInvalidJSON},
		{name: "wrong type", body: `[]`, contentType: jsonContentType, wantStatus: http.StatusBadRequest, wantMessage: errInvalidJSON},
		{name: "empty", body: ``, contentType: jsonContentType, wantStatus: http.StatusBadRequest, wantMessage: errBodyRequired},
		{name: "text", body: `{"name":"demo","repo_url":"https://github.com/acme/demo"}`, contentType: "text/plain", wantStatus: http.StatusUnsupportedMediaType, wantMessage: errContentType},
		{name: "no content type", body: `{"name":"demo","repo_url":"https://github.com/acme/demo"}`, contentType: "", wantStatus: http.StatusUnsupportedMediaType, wantMessage: errContentType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handler := newTestHandler(t)
			rec := doRequest(handler, http.MethodPost, "/apps", tt.body, tt.contentType)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status %d body %s, want %d", rec.Code, rec.Body.String(), tt.wantStatus)
			}
			if got := decodeError(t, rec); got != tt.wantMessage {
				t.Fatalf("message %q, want %q", got, tt.wantMessage)
			}
		})
	}
}

func TestBodyTooLarge(t *testing.T) {
	t.Parallel()
	h := newTestHandler(t)
	repo := "https://github.com/acme/" + strings.Repeat("a", maxBodyBytes)
	body := `{"name":"demo","repo_url":"` + repo + `"}`
	rec := doRequest(h, http.MethodPost, "/apps", body, jsonContentType)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if decodeError(t, rec) != errBodyTooLarge {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestGetAppErrors(t *testing.T) {
	t.Parallel()
	h := newTestHandler(t)

	rec := doRequest(h, http.MethodGet, "/apps/Demo", "", "")
	if rec.Code != http.StatusBadRequest || decodeError(t, rec) != ErrNameInvalid.Error() {
		t.Fatalf("bad name %d %s", rec.Code, rec.Body.String())
	}
	rec = doRequest(h, http.MethodGet, "/apps/missing", "", "")
	if rec.Code != http.StatusNotFound || decodeError(t, rec) != errAppNotFound {
		t.Fatalf("missing %d %s", rec.Code, rec.Body.String())
	}
}

func TestMethodAndPathFallback(t *testing.T) {
	t.Parallel()
	h := newTestHandler(t)

	rec := doRequest(h, http.MethodPut, "/apps", "", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT /apps status %d body %s", rec.Code, rec.Body.String())
	}
	allow := rec.Header().Get("Allow")
	if !strings.Contains(allow, http.MethodGet) || !strings.Contains(allow, http.MethodPost) {
		t.Fatalf("Allow %q", allow)
	}

	rec = doRequest(h, http.MethodPost, "/healthz", "", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz status %d", rec.Code)
	}

	rec = doRequest(h, http.MethodGet, "/nope", "", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown path %d %s", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != "404 page not found" {
		t.Fatalf("unknown path body %q", rec.Body.String())
	}
}

func TestNilLoggerAndStoreErrors(t *testing.T) {
	t.Parallel()
	st := stubStore{
		list: func(context.Context) ([]store.App, error) {
			return nil, nil
		},
	}
	h := NewHandler(st, nil, nil)
	rec := doRequest(h, http.MethodGet, "/apps", "", "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "null") {
		t.Fatalf("nil list %d %s", rec.Code, rec.Body.String())
	}

	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantMsg    string
	}{
		{name: "canceled", err: context.Canceled, wantStatus: http.StatusRequestTimeout, wantMsg: errCanceled},
		{name: "deadline", err: context.DeadlineExceeded, wantStatus: http.StatusRequestTimeout, wantMsg: errCanceled},
		{name: "invalid", err: store.ErrInvalid, wantStatus: http.StatusBadRequest, wantMsg: errInvalidApp},
		{name: "not found", err: store.ErrNotFound, wantStatus: http.StatusNotFound, wantMsg: errAppNotFound},
		{name: "internal", err: errors.New("disk failed"), wantStatus: http.StatusInternalServerError, wantMsg: errInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			handler := NewHandler(stubStore{
				get: func(context.Context, string) (store.App, error) {
					return store.App{}, tc.err
				},
			}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
			rec := doRequest(handler, http.MethodGet, "/apps/demo", "", "")
			if rec.Code != tc.wantStatus {
				t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
			}
			if got := decodeError(t, rec); got != tc.wantMsg {
				t.Fatalf("message %q, want %q", got, tc.wantMsg)
			}
			if tc.name == "internal" && strings.Contains(rec.Body.String(), "disk failed") {
				t.Fatal("internal error leaked")
			}
		})
	}
}

func TestPanicBecomesInternalError(t *testing.T) {
	t.Parallel()
	h := NewHandler(stubStore{
		create: func(context.Context, string, string) (store.App, error) {
			panic("boom")
		},
	}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := doRequest(h, http.MethodPost, "/apps", `{"name":"demo","repo_url":"https://github.com/acme/demo"}`, jsonContentType)
	if rec.Code != http.StatusInternalServerError || decodeError(t, rec) != errInternal {
		t.Fatalf("panic result %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "boom") {
		t.Fatal("panic text leaked")
	}
}

type stubStore struct {
	create func(context.Context, string, string) (store.App, error)
	get    func(context.Context, string) (store.App, error)
	list   func(context.Context) ([]store.App, error)
}

func (s stubStore) CreateApp(ctx context.Context, name, repoURL string) (store.App, error) {
	if s.create == nil {
		return store.App{}, errors.New("create not stubbed")
	}
	return s.create(ctx, name, repoURL)
}

func (s stubStore) GetApp(ctx context.Context, name string) (store.App, error) {
	if s.get == nil {
		return store.App{}, errors.New("get not stubbed")
	}
	return s.get(ctx, name)
}

func (s stubStore) ListApps(ctx context.Context) ([]store.App, error) {
	if s.list == nil {
		return nil, errors.New("list not stubbed")
	}
	return s.list(ctx)
}

func (s stubStore) CreateDeployment(context.Context, string, string) (store.Deployment, error) {
	return store.Deployment{}, errors.New("deployment not stubbed")
}

func (s stubStore) CreateBuild(context.Context, string, string) (store.Deployment, error) {
	return store.Deployment{}, errors.New("deployment not stubbed")
}

func (s stubStore) UpdateDeployment(context.Context, string, store.DeploymentUpdate) (store.Deployment, error) {
	return store.Deployment{}, errors.New("deployment not stubbed")
}

func (s stubStore) GetDeployment(context.Context, string) (store.Deployment, error) {
	return store.Deployment{}, errors.New("deployment not stubbed")
}

func (s stubStore) ListDeployments(context.Context, string) ([]store.Deployment, error) {
	return nil, errors.New("deployment not stubbed")
}

func (s stubStore) AppendDeploymentLog(context.Context, string, []byte) error {
	return errors.New("deployment not stubbed")
}

func (s stubStore) ReadDeploymentLog(context.Context, string, int) (store.LogRead, error) {
	return store.LogRead{}, errors.New("deployment not stubbed")
}

func (s stubStore) WaitDeploymentLog(context.Context, string, int) error {
	return errors.New("deployment not stubbed")
}
