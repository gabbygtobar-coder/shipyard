package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/gabbygtobar-coder/shipyard/internal/giturl"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

const errInvalidApp = "invalid app"

const errWebhookSecret = "webhook_secret is invalid"

type createAppRequest struct {
	Name          string `json:"name"`
	RepoURL       string `json:"repo_url"`
	Branch        string `json:"branch"`
	AutoDeploy    bool   `json:"auto_deploy"`
	WebhookSecret string `json:"webhook_secret"`
}

type updateAppRequest struct {
	Branch        *string `json:"branch"`
	AutoDeploy    *bool   `json:"auto_deploy"`
	WebhookSecret *string `json:"webhook_secret"`
}

type appListResponse struct {
	Apps []store.App `json:"apps"`
}

type healthResponse struct {
	Status string `json:"status"`
}

type httpStatusError struct {
	status int
	msg    string
}

func (e *httpStatusError) Error() string { return e.msg }

func (h *handler) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

func (h *handler) createApp(w http.ResponseWriter, r *http.Request) {
	var req createAppRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeDecodeError(w, err)
		return
	}

	name := strings.TrimSpace(req.Name)
	if err := ValidateName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	repoURL, err := NormalizeRepoURL(req.RepoURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	settings, err := appSettings(req.Branch, req.AutoDeploy, req.WebhookSecret)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if _, err := h.store.CreateApp(r.Context(), name, repoURL); err != nil {
		h.writeStoreError(w, r, err, errAppNotFound)
		return
	}
	app, err := h.store.ConfigureApp(r.Context(), name, settings)
	if err != nil {
		h.writeStoreError(w, r, err, errAppNotFound)
		return
	}
	w.Header().Set("Location", "/apps/"+url.PathEscape(name))
	writeJSON(w, http.StatusCreated, app)
}

func (h *handler) listApps(w http.ResponseWriter, r *http.Request) {
	apps, err := h.store.ListApps(r.Context())
	if err != nil {
		h.writeStoreError(w, r, err, errAppNotFound)
		return
	}
	if apps == nil {
		apps = []store.App{}
	}
	writeJSON(w, http.StatusOK, appListResponse{Apps: apps})
}

func (h *handler) updateApp(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if err := ValidateName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req updateAppRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeDecodeError(w, err)
		return
	}
	app, err := h.store.GetApp(r.Context(), name)
	if err != nil {
		h.writeStoreError(w, r, err, errAppNotFound)
		return
	}
	branch := app.Branch
	auto := app.AutoDeploy
	secret := app.WebhookSecret
	if req.Branch != nil {
		branch = *req.Branch
	}
	if req.AutoDeploy != nil {
		auto = *req.AutoDeploy
	}
	if req.WebhookSecret != nil {
		secret = *req.WebhookSecret
	}
	settings, err := appSettings(branch, auto, secret)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	app, err = h.store.ConfigureApp(r.Context(), name, settings)
	if err != nil {
		h.writeStoreError(w, r, err, errAppNotFound)
		return
	}
	writeJSON(w, http.StatusOK, app)
}

func appSettings(branch string, auto bool, secret string) (store.AppSettings, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		branch = store.DefaultBranch
	}
	if err := giturl.ValidateRef(branch); err != nil {
		return store.AppSettings{}, err
	}
	if len(secret) > store.MaxWebhookSecretBytes || strings.ContainsRune(secret, 0) {
		return store.AppSettings{}, errors.New(errWebhookSecret)
	}
	return store.AppSettings{Branch: branch, AutoDeploy: auto, WebhookSecret: secret}, nil
}

func (h *handler) getApp(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, app)
}

func (h *handler) writeStoreError(w http.ResponseWriter, r *http.Request, err error, notFound string) {
	switch {
	case errors.Is(err, store.ErrAlreadyExists):
		writeError(w, http.StatusConflict, errAppExists)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, notFound)
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, errInvalidApp)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusRequestTimeout, errCanceled)
	default:
		ctx := context.Background()
		if r != nil {
			ctx = r.Context()
		}
		h.logger(ctx).Error("store", "err", err)
		writeError(w, http.StatusInternalServerError, errInternal)
	}
}

func writeDecodeError(w http.ResponseWriter, err error) {
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		writeError(w, statusErr.status, statusErr.msg)
		return
	}
	writeError(w, http.StatusBadRequest, errInvalidJSON)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != mediaTypeJSON {
		return &httpStatusError{status: http.StatusUnsupportedMediaType, msg: errContentType}
	}

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return classifyDecodeError(err)
	}
	trailing := dec.Decode(&struct{}{})
	if errors.Is(trailing, io.EOF) {
		return nil
	}
	if trailing == nil {
		return errors.New(errMultipleJSON)
	}
	return classifyDecodeError(trailing)
}

func classifyDecodeError(err error) error {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return &httpStatusError{status: http.StatusRequestEntityTooLarge, msg: errBodyTooLarge}
	}
	if errors.Is(err, io.EOF) {
		return &httpStatusError{status: http.StatusBadRequest, msg: errBodyRequired}
	}
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		return &httpStatusError{status: http.StatusBadRequest, msg: errInvalidJSON}
	}
	var unmarshal *json.UnmarshalTypeError
	if errors.As(err, &unmarshal) {
		return &httpStatusError{status: http.StatusBadRequest, msg: errInvalidJSON}
	}
	return err
}
