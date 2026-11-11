package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gabbygtobar-coder/shipyard/internal/store"
	"github.com/gabbygtobar-coder/shipyard/internal/webhook"
)

const (
	maxWebhookBytes       = 2 << 20
	errInvalidSignature   = "invalid webhook signature"
	errInvalidDelivery    = "invalid delivery id"
	headerHubSignature256 = "X-Hub-Signature-256"
	headerGitHubEvent     = "X-GitHub-Event"
	headerGitHubDelivery  = "X-GitHub-Delivery"
)

type pushService interface {
	StartPush(ctx context.Context, appName, branch, commitSHA string) (store.Deployment, bool, error)
}

type webhookResponse struct {
	Status      string   `json:"status"`
	Deployments []string `json:"deployments,omitempty"`
}

func (h *handler) githubWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := readLimited(w, r, maxWebhookBytes)
	if err != nil {
		h.metrics.ObserveWebhook("invalid")
		writeDecodeError(w, err)
		return
	}
	event := strings.TrimSpace(r.Header.Get(headerGitHubEvent))
	delivery := strings.TrimSpace(r.Header.Get(headerGitHubDelivery))
	signature := r.Header.Get(headerHubSignature256)
	matched, ok, err := h.authorizedApps(r.Context(), body, signature)
	if err != nil {
		h.metrics.ObserveWebhook("error")
		h.writeStoreError(w, r, err, errAppNotFound)
		return
	}
	if !ok {
		h.metrics.ObserveWebhook("unauthorized")
		writeError(w, http.StatusUnauthorized, errInvalidSignature)
		return
	}
	claimed, err := h.store.ClaimDelivery(r.Context(), delivery)
	if err != nil {
		if errors.Is(err, store.ErrInvalid) {
			h.metrics.ObserveWebhook("invalid")
			writeError(w, http.StatusBadRequest, errInvalidDelivery)
			return
		}
		h.metrics.ObserveWebhook("error")
		h.writeStoreError(w, r, err, errAppNotFound)
		return
	}
	if !claimed {
		h.metrics.ObserveWebhook("duplicate")
		writeJSON(w, http.StatusAccepted, webhookResponse{Status: "duplicate"})
		return
	}
	switch event {
	case "ping":
		h.noteWebhook(r.Context(), matched, "ping")
		h.metrics.ObserveWebhook("pong")
		writeJSON(w, http.StatusOK, webhookResponse{Status: "pong"})
	case "push":
		h.handlePush(w, r, delivery, body, matched)
	default:
		h.noteWebhook(r.Context(), matched, "ignored event "+event)
		h.metrics.ObserveWebhook("ignored")
		writeJSON(w, http.StatusAccepted, webhookResponse{Status: "ignored"})
	}
}

func (h *handler) handlePush(w http.ResponseWriter, r *http.Request, delivery string, body []byte, matched []store.App) {
	forget := func() {
		_ = h.store.ForgetDelivery(context.Background(), delivery)
	}
	push, err := webhook.ParsePush(body)
	if err != nil && !errors.Is(err, webhook.ErrIgnored) {
		forget()
		h.metrics.ObserveWebhook("invalid")
		writeError(w, http.StatusBadRequest, errInvalidJSON)
		return
	}
	if err != nil || push.Deleted || push.Branch == "" {
		h.noteWebhook(r.Context(), matched, "ignored push")
		h.metrics.ObserveWebhook("ignored")
		writeJSON(w, http.StatusAccepted, webhookResponse{Status: "ignored"})
		return
	}
	var ids []string
	coalesced := false
	for _, app := range matched {
		if !app.AutoDeploy || app.Branch != push.Branch {
			h.noteWebhook(r.Context(), []store.App{app}, "ignored push of "+push.Branch)
			continue
		}
		pushes, ok := h.builds.(pushService)
		if !ok || h.builds == nil {
			if len(ids) == 0 {
				forget()
				h.metrics.ObserveWebhook("error")
				writeError(w, http.StatusServiceUnavailable, errBuildsUnconfigured)
				return
			}
			break
		}
		dep, coal, startErr := pushes.StartPush(r.Context(), app.Name, push.Branch, push.SHA)
		if startErr != nil {
			h.logger(r.Context()).Error("webhook deploy", "app", app.Name, "err", startErr)
			if len(ids) == 0 {
				forget()
				h.noteWebhook(r.Context(), []store.App{app}, "push failed")
				h.metrics.ObserveWebhook("error")
				h.writeBuildError(w, r, startErr)
				return
			}
			break
		}
		h.noteWebhook(r.Context(), []store.App{app}, "push "+push.SHA+" on "+push.Branch)
		ids = append(ids, dep.ID)
		coalesced = coalesced || coal
	}
	if len(ids) == 0 {
		h.metrics.ObserveWebhook("ignored")
		writeJSON(w, http.StatusAccepted, webhookResponse{Status: "ignored"})
		return
	}
	status := "accepted"
	if coalesced {
		status = "coalesced"
	}
	h.metrics.ObserveWebhook(status)
	writeJSON(w, http.StatusAccepted, webhookResponse{Status: status, Deployments: ids})
}

func (h *handler) noteWebhook(ctx context.Context, apps []store.App, message string) {
	if len(message) > store.MaxEventMessageBytes {
		message = message[:store.MaxEventMessageBytes]
	}
	for _, app := range apps {
		if _, err := h.store.AppendEvent(ctx, store.Event{
			AppName: app.Name,
			Type:    store.EventWebhookReceived,
			Message: message,
		}); err != nil {
			h.logger(ctx).Error("webhook event", "app", app.Name, "err", err)
		}
	}
}

func (h *handler) authorizedApps(ctx context.Context, body []byte, signature string) ([]store.App, bool, error) {
	var matched []store.App
	if repo := webhook.RepoURL(body); repo != "" {
		apps, err := h.store.ListAppsByRepo(ctx, repo)
		if err != nil && !errors.Is(err, store.ErrInvalid) {
			return nil, false, err
		}
		for _, app := range apps {
			secret := app.WebhookSecret
			if secret == "" {
				secret = h.webhookSecret
			}
			if webhook.Verify(secret, body, signature) {
				matched = append(matched, app)
			}
		}
	}
	globalOK := webhook.Verify(h.webhookSecret, body, signature)
	return matched, len(matched) > 0 || globalOK, nil
}

func readLimited(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	body := r.Body
	if body == nil {
		body = http.NoBody
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, body, limit))
	if err != nil {
		return nil, classifyDecodeError(err)
	}
	return data, nil
}
