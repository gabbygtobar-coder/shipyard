package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/gabbygtobar-coder/shipyard/internal/deploy"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

const errReleaseNotFound = "release not found"

type rollbackService interface {
	Rollback(ctx context.Context, appName, deploymentID string) (store.Deployment, error)
}

type rollbackRequest struct {
	DeploymentID string `json:"deployment_id"`
}

type releaseListResponse struct {
	Releases []store.Release `json:"releases"`
}

func (h *handler) rollbackApp(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if err := ValidateName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	deploymentID, err := decodeRollbackID(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if deploymentID != "" {
		if err := ValidateDeploymentID(deploymentID); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	rollbacks, ok := h.builds.(rollbackService)
	if !ok || h.builds == nil {
		writeError(w, http.StatusServiceUnavailable, errBuildsUnconfigured)
		return
	}
	dep, err := rollbacks.Rollback(r.Context(), name, deploymentID)
	if err != nil {
		h.writeRollbackError(w, r, err)
		return
	}
	w.Header().Set("Location", "/deployments/"+url.PathEscape(dep.ID))
	writeJSON(w, http.StatusAccepted, dep)
}

func (h *handler) listReleases(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if err := ValidateName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	releases, err := h.store.ListReleases(r.Context(), name)
	if err != nil {
		h.writeStoreError(w, r, err, errAppNotFound)
		return
	}
	if releases == nil {
		releases = []store.Release{}
	}
	writeJSON(w, http.StatusOK, releaseListResponse{Releases: releases})
}

func (h *handler) writeRollbackError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, deploy.ErrNoRelease), errors.Is(err, deploy.ErrAlreadyCurrent):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, deploy.ErrReleaseNotFound):
		writeError(w, http.StatusNotFound, errReleaseNotFound)
	case errors.Is(err, deploy.ErrQueueFull), errors.Is(err, deploy.ErrStopped):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, errAppNotFound)
	default:
		h.writeBuildError(w, r, err)
	}
}

func decodeRollbackID(w http.ResponseWriter, r *http.Request) (string, error) {
	body := r.Body
	if body == nil {
		body = http.NoBody
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, body, maxBodyBytes))
	if err != nil {
		return "", classifyDecodeError(err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return "", nil
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != mediaTypeJSON {
		return "", &httpStatusError{status: http.StatusUnsupportedMediaType, msg: errContentType}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var req rollbackRequest
	if err := dec.Decode(&req); err != nil {
		return "", classifyDecodeError(err)
	}
	trailing := dec.Decode(&struct{}{})
	if errors.Is(trailing, io.EOF) {
		return strings.TrimSpace(req.DeploymentID), nil
	}
	if trailing == nil {
		return "", errors.New(errMultipleJSON)
	}
	return "", classifyDecodeError(trailing)
}
