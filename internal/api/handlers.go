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

	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

const errInvalidApp = "invalid app"

type createAppRequest struct {
	Name    string `json:"name"`
	RepoURL string `json:"repo_url"`
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

	app, err := h.store.CreateApp(r.Context(), name, repoURL)
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	w.Header().Set("Location", "/apps/"+url.PathEscape(name))
	writeJSON(w, http.StatusCreated, app)
}

func (h *handler) listApps(w http.ResponseWriter, r *http.Request) {
	apps, err := h.store.ListApps(r.Context())
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	if apps == nil {
		apps = []store.App{}
	}
	writeJSON(w, http.StatusOK, appListResponse{Apps: apps})
}

func (h *handler) getApp(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if err := ValidateName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	app, err := h.store.GetApp(r.Context(), name)
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, app)
}

func (h *handler) writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrAlreadyExists):
		writeError(w, http.StatusConflict, errAppExists)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, errAppNotFound)
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, errInvalidApp)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusRequestTimeout, errCanceled)
	default:
		h.log.Error("store", "err", err)
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
	if err != nil || mediaType != "application/json" {
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
		return errors.New("multiple JSON values")
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
