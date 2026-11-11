package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/gabbygtobar-coder/shipyard/internal/deploy"
	"github.com/gabbygtobar-coder/shipyard/internal/giturl"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

type createDeploymentRequest struct {
	Ref string `json:"ref"`
}

type deploymentListResponse struct {
	Deployments []store.Deployment `json:"deployments"`
}

func (h *handler) createDeployment(w http.ResponseWriter, r *http.Request) {
	if h.builds == nil {
		writeError(w, http.StatusServiceUnavailable, errBuildsUnconfigured)
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if err := ValidateName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ref, err := decodeDeploymentRef(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if err := giturl.ValidateRef(ref); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	dep, err := h.builds.StartBuild(r.Context(), name, ref)
	if err != nil {
		h.writeBuildError(w, r, err)
		return
	}
	w.Header().Set("Location", "/deployments/"+url.PathEscape(dep.ID))
	writeJSON(w, http.StatusAccepted, dep)
}

func (h *handler) listDeployments(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if err := ValidateName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	deps, err := h.store.ListDeployments(r.Context(), name)
	if err != nil {
		h.writeStoreError(w, r, err, errAppNotFound)
		return
	}
	if deps == nil {
		deps = []store.Deployment{}
	}
	writeJSON(w, http.StatusOK, deploymentListResponse{Deployments: deps})
}

func (h *handler) getDeployment(w http.ResponseWriter, r *http.Request) {
	id, ok := deploymentID(w, r)
	if !ok {
		return
	}
	dep, err := h.store.GetDeployment(r.Context(), id)
	if err != nil {
		h.writeStoreError(w, r, err, errDeploymentNotFound)
		return
	}
	writeJSON(w, http.StatusOK, dep)
}

func (h *handler) deploymentLogs(w http.ResponseWriter, r *http.Request) {
	id, ok := deploymentID(w, r)
	if !ok {
		return
	}
	follow := wantsFollow(r)
	offset := 0
	header := false
	flusher, _ := w.(http.Flusher)
	for {
		snap, err := h.store.ReadDeploymentLog(r.Context(), id, offset)
		if err != nil {
			if !header {
				h.writeStoreError(w, r, err, errDeploymentNotFound)
			}
			return
		}
		if !header {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.WriteHeader(http.StatusOK)
			header = true
		}
		if len(snap.Data) > 0 {
			if _, err := w.Write(snap.Data); err != nil {
				return
			}
			offset = snap.Next
			if flusher != nil {
				flusher.Flush()
			}
		}
		if !follow || (snap.Terminal && offset >= snap.Total) {
			return
		}
		if err := h.store.WaitDeploymentLog(r.Context(), id, offset); err != nil {
			return
		}
	}
}

func deploymentID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := ValidateDeploymentID(id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return "", false
	}
	return id, true
}

func wantsFollow(r *http.Request) bool {
	switch strings.ToLower(r.URL.Query().Get("follow")) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func (h *handler) writeBuildError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, deploy.ErrQueueFull), errors.Is(err, deploy.ErrStopped):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, giturl.ErrRefInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, errAppNotFound)
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, errInvalidDeployment)
	default:
		h.writeStoreError(w, r, err, errAppNotFound)
	}
}

func decodeDeploymentRef(w http.ResponseWriter, r *http.Request) (string, error) {
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
	var req createDeploymentRequest
	if err := dec.Decode(&req); err != nil {
		return "", classifyDecodeError(err)
	}
	trailing := dec.Decode(&struct{}{})
	if errors.Is(trailing, io.EOF) {
		return strings.TrimSpace(req.Ref), nil
	}
	if trailing == nil {
		return "", errors.New(errMultipleJSON)
	}
	return "", classifyDecodeError(trailing)
}
