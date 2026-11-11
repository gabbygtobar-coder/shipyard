package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

func signWebhook(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

type fakePush struct {
	mu    sync.Mutex
	calls []string
	n     int
}

func (f *fakePush) StartBuild(context.Context, string, string) (store.Deployment, error) {
	return store.Deployment{}, context.Canceled
}

func (f *fakePush) StartPush(_ context.Context, appName, branch, sha string) (store.Deployment, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	f.calls = append(f.calls, appName+" "+branch+" "+sha)
	return store.Deployment{ID: "dep_" + strconv.Itoa(f.n), AppName: appName, Status: store.DeploymentStatusQueued}, false, nil
}

func (f *fakePush) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func webhookRequest(h http.Handler, event, delivery, secret string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
	req.Header.Set("Content-Type", jsonContentType)
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", delivery)
	if secret != "" {
		req.Header.Set("X-Hub-Signature-256", signWebhook(secret, body))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestWebhookSignatureBranchAndIdempotency(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	pushes := &fakePush{}
	const (
		global = "global-webhook-secret"
		appKey = "per-app-webhook-secret"
	)
	h := NewHandler(st, pushes, logger, nil, WithWebhookSecret(global))

	create := doRequest(h, http.MethodPost, "/apps", `{
		"name":"demo",
		"repo_url":"https://github.com/acme/demo.git",
		"branch":"main",
		"auto_deploy":false,
		"webhook_secret":"`+appKey+`"
	}`, jsonContentType)
	if create.Code != http.StatusCreated {
		t.Fatalf("create %d %s", create.Code, create.Body.String())
	}
	if strings.Contains(create.Body.String(), appKey) || strings.Contains(logs.String(), appKey) || strings.Contains(logs.String(), global) {
		t.Fatal("webhook secret leaked")
	}
	var created store.App
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Branch != "main" || created.AutoDeploy {
		t.Fatalf("created %#v", created)
	}

	push := []byte(`{"ref":"refs/heads/main","after":"0123456789abcdef0123456789abcdef01234567","repository":{"clone_url":"https://github.com/acme/demo.git"}}`)
	if rec := webhookRequest(h, "push", "delivery-1", "", push); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing signature %d %s", rec.Code, rec.Body.String())
	}
	if rec := webhookRequest(h, "push", "delivery-1", "wrong-secret", push); rec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid signature %d %s", rec.Code, rec.Body.String())
	}
	ping := []byte(`{"zen":"keep it logically awesome","repository":{"clone_url":"https://github.com/acme/demo.git"}}`)
	if rec := webhookRequest(h, "ping", "delivery-ping", appKey, ping); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "pong") {
		t.Fatalf("ping %d %s", rec.Code, rec.Body.String())
	}
	if rec := webhookRequest(h, "issues", "delivery-issues", appKey, push); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "ignored") {
		t.Fatalf("other event %d %s", rec.Code, rec.Body.String())
	}
	if rec := webhookRequest(h, "push", "delivery-off", appKey, push); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "ignored") {
		t.Fatalf("auto deploy off %d %s", rec.Code, rec.Body.String())
	}
	if pushes.count() != 0 {
		t.Fatalf("pushes before enable %d", pushes.count())
	}

	update := doRequest(h, http.MethodPatch, "/apps/demo", `{"auto_deploy":true}`, jsonContentType)
	if update.Code != http.StatusOK || strings.Contains(update.Body.String(), appKey) {
		t.Fatalf("update %d %s", update.Code, update.Body.String())
	}
	other := []byte(`{"ref":"refs/heads/wip","after":"abcdefabcdefabcdefabcdefabcdefabcdefabcd","repository":{"full_name":"acme/demo"}}`)
	if rec := webhookRequest(h, "push", "delivery-branch", appKey, other); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "ignored") {
		t.Fatalf("other branch %d %s", rec.Code, rec.Body.String())
	}
	if pushes.count() != 0 {
		t.Fatalf("branch filter called push %d", pushes.count())
	}
	if rec := webhookRequest(h, "push", "delivery-global", global, push); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "ignored") {
		t.Fatalf("global secret must not deploy an app that has its own secret %d %s", rec.Code, rec.Body.String())
	}
	if pushes.count() != 0 {
		t.Fatalf("global signature queued %d", pushes.count())
	}
	accepted := webhookRequest(h, "push", "delivery-ok", appKey, push)
	if accepted.Code != http.StatusAccepted || !strings.Contains(accepted.Body.String(), `"accepted"`) {
		t.Fatalf("accepted %d %s", accepted.Code, accepted.Body.String())
	}
	if pushes.count() != 1 {
		t.Fatalf("pushes %d", pushes.count())
	}
	dup := webhookRequest(h, "push", "delivery-ok", appKey, push)
	if dup.Code != http.StatusAccepted || !strings.Contains(dup.Body.String(), "duplicate") || pushes.count() != 1 {
		t.Fatalf("duplicate %d %s pushes %d", dup.Code, dup.Body.String(), pushes.count())
	}
	if strings.Contains(logs.String(), appKey) || strings.Contains(logs.String(), global) || strings.Contains(accepted.Body.String(), appKey) {
		t.Fatal("secret leaked from webhook")
	}
}

func TestWebhookGlobalSecretDeploysAppWithoutSecret(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	pushes := &fakePush{}
	const global = "global-webhook-secret"
	h := NewHandler(st, pushes, nil, nil, WithWebhookSecret(global))
	rec := doRequest(h, http.MethodPost, "/apps", `{
		"name":"plain",
		"repo_url":"https://github.com/acme/plain",
		"auto_deploy":true
	}`, jsonContentType)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %d %s", rec.Code, rec.Body.String())
	}
	body := []byte(`{"ref":"refs/heads/main","after":"0123456789abcdef0123456789abcdef01234567","repository":{"clone_url":"https://github.com/acme/plain.git"}}`)
	rec = webhookRequest(h, "push", "delivery-plain", global, body)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"accepted"`) || pushes.count() != 1 {
		t.Fatalf("global deploy %d %s pushes %d", rec.Code, rec.Body.String(), pushes.count())
	}
}
