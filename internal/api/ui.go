package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/gabbygtobar-coder/shipyard/internal/deploy"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

//go:embed templates/*.html
var templateFiles embed.FS

var uiTemplates = template.Must(template.New("ui").Funcs(template.FuncMap{
	"safeURL": safeHTTPURL,
}).ParseFS(templateFiles, "templates/*.html"))

type uiPage struct {
	Title   string
	Notice  string
	Error   string
	Pending bool
	Apps    []uiAppRow
	App     *uiAppView
}

type uiAppRow struct {
	Name    string
	Status  string
	URL     string
	Current string
}

type uiAppView struct {
	Name        string
	RepoURL     string
	Branch      string
	Status      string
	URL         string
	AutoDeploy  bool
	Current     *store.Deployment
	Deployments []store.Deployment
	Events      []store.Event
	Releases    []store.Release
}

func (h *handler) uiIndex(w http.ResponseWriter, r *http.Request) {
	if !h.allowUI(w, r) {
		return
	}
	apps, err := h.store.ListApps(r.Context())
	if err != nil {
		h.writeStoreError(w, r, err, errAppNotFound)
		return
	}
	page := uiPage{Title: "Apps"}
	for _, app := range apps {
		row := uiAppRow{Name: app.Name, Status: app.Status, URL: app.URL}
		deps, listErr := h.store.ListDeployments(r.Context(), app.Name)
		if listErr != nil {
			h.writeStoreError(w, r, listErr, errAppNotFound)
			return
		}
		if current := currentDeployment(deps); current != nil {
			row.Current = current.ID + " " + current.Status
		}
		if page.Apps == nil {
			page.Apps = []uiAppRow{}
		}
		page.Apps = append(page.Apps, row)
		page.Pending = page.Pending || deploymentPending(deps)
	}
	if page.Apps == nil {
		page.Apps = []uiAppRow{}
	}
	h.renderUI(w, "index", page, http.StatusOK)
}

func (h *handler) uiApp(w http.ResponseWriter, r *http.Request) {
	if !h.allowUI(w, r) {
		return
	}
	page, err := h.loadUIApp(r)
	if err != nil {
		h.writeUILoadError(w, r, err)
		return
	}
	if r.URL.Query().Get("notice") == "rollback queued" {
		page.Notice = "Rollback queued."
	}
	h.renderUI(w, "app", page, http.StatusOK)
}

func (h *handler) uiRollback(w http.ResponseWriter, r *http.Request) {
	if !h.allowUI(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid form")
		return
	}
	if r.Form.Get("confirm") != "rollback" {
		writeError(w, http.StatusBadRequest, "confirm rollback")
		return
	}
	deploymentID := strings.TrimSpace(r.Form.Get("deployment_id"))
	if deploymentID != "" {
		if err := ValidateDeploymentID(deploymentID); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	page, err := h.loadUIApp(r)
	if err != nil {
		h.writeUILoadError(w, r, err)
		return
	}
	rollbacks, ok := h.builds.(rollbackService)
	if !ok || h.builds == nil {
		page.Error = errBuildsUnconfigured
		h.renderUI(w, "app", page, http.StatusServiceUnavailable)
		return
	}
	if _, err := rollbacks.Rollback(r.Context(), page.App.Name, deploymentID); err != nil {
		page.Error = rollbackMessage(err)
		h.renderUI(w, "app", page, rollbackStatus(err))
		return
	}
	http.Redirect(w, r, "/ui/apps/"+url.PathEscape(page.App.Name)+"?notice=rollback+queued", http.StatusSeeOther)
}

func (h *handler) loadUIApp(r *http.Request) (uiPage, error) {
	name := strings.TrimSpace(r.PathValue("name"))
	if err := ValidateName(name); err != nil {
		return uiPage{}, err
	}
	app, err := h.store.GetApp(r.Context(), name)
	if err != nil {
		return uiPage{}, err
	}
	deps, err := h.store.ListDeployments(r.Context(), name)
	if err != nil {
		return uiPage{}, err
	}
	events, err := h.store.ListEvents(r.Context(), name, 20)
	if err != nil {
		return uiPage{}, err
	}
	releases, err := h.store.ListReleases(r.Context(), name)
	if err != nil {
		return uiPage{}, err
	}
	if events == nil {
		events = []store.Event{}
	}
	if releases == nil {
		releases = []store.Release{}
	}
	view := &uiAppView{
		Name:        app.Name,
		RepoURL:     app.RepoURL,
		Branch:      app.Branch,
		Status:      app.Status,
		URL:         app.URL,
		AutoDeploy:  app.AutoDeploy,
		Current:     currentDeployment(deps),
		Deployments: recentDeployments(deps, 10),
		Events:      events,
		Releases:    releases,
	}
	return uiPage{
		Title:   app.Name,
		Pending: deploymentPending(deps),
		App:     view,
	}, nil
}

func (h *handler) writeUILoadError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrNameRequired) || errors.Is(err, ErrNameInvalid) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.writeStoreError(w, r, err, errAppNotFound)
}

func (h *handler) renderUI(w http.ResponseWriter, name string, page uiPage, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	if err := uiTemplates.ExecuteTemplate(w, name, page); err != nil {
		h.log.Error("ui template", "err", err, "template", name)
	}
}

func (h *handler) allowUI(w http.ResponseWriter, r *http.Request) bool {
	if h.uiToken == "" {
		http.Error(w, "dashboard token is not configured\n", http.StatusUnauthorized)
		return false
	}
	if uiAuthorized(r.Header.Get("Authorization"), h.uiToken) {
		return true
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="shipyard"`)
	http.Error(w, "authentication required\n", http.StatusUnauthorized)
	return false
}

func uiAuthorized(header, token string) bool {
	const bearer = "Bearer "
	if strings.HasPrefix(header, bearer) {
		return tokenMatches(strings.TrimSpace(header[len(bearer):]), token)
	}
	const basic = "Basic "
	if !strings.HasPrefix(header, basic) {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(header[len(basic):]))
	if err != nil {
		return false
	}
	_, pass, ok := strings.Cut(string(raw), ":")
	if !ok {
		return false
	}
	return tokenMatches(pass, token)
}

func tokenMatches(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	summed := sha256.Sum256([]byte(got))
	expected := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(summed[:], expected[:]) == 1
}

func safeHTTPURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ""
	}
	return parsed.String()
}

func currentDeployment(deps []store.Deployment) *store.Deployment {
	var latest *store.Deployment
	for i := range deps {
		dep := deps[i]
		if dep.Status == store.DeploymentStatusRunning {
			return &dep
		}
		latest = &dep
	}
	return latest
}

func recentDeployments(deps []store.Deployment, n int) []store.Deployment {
	if n < 1 {
		n = 1
	}
	if len(deps) > n {
		deps = deps[len(deps)-n:]
	}
	out := make([]store.Deployment, 0, len(deps))
	for i := len(deps) - 1; i >= 0; i-- {
		out = append(out, deps[i])
	}
	return out
}

func deploymentPending(deps []store.Deployment) bool {
	for _, dep := range deps {
		switch dep.Status {
		case store.DeploymentStatusQueued, store.DeploymentStatusBuilding, store.DeploymentStatusStarting:
			return true
		}
	}
	return false
}

func rollbackMessage(err error) string {
	switch {
	case errors.Is(err, deploy.ErrNoRelease), errors.Is(err, deploy.ErrAlreadyCurrent), errors.Is(err, deploy.ErrReleaseNotFound):
		return err.Error()
	case errors.Is(err, deploy.ErrQueueFull), errors.Is(err, deploy.ErrStopped):
		return err.Error()
	case errors.Is(err, store.ErrNotFound):
		return errAppNotFound
	default:
		return errInternal
	}
}

func rollbackStatus(err error) int {
	switch {
	case errors.Is(err, deploy.ErrNoRelease), errors.Is(err, deploy.ErrAlreadyCurrent):
		return http.StatusConflict
	case errors.Is(err, deploy.ErrReleaseNotFound), errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, deploy.ErrQueueFull), errors.Is(err, deploy.ErrStopped):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
