package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

const clientTimeout = 20 * time.Second

type apiClient struct {
	baseURL string
	http    *http.Client
}

type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s (HTTP %d)", e.Message, e.Status)
}

type appListResponse struct {
	Apps []store.App `json:"apps"`
}

func newAPIClient(baseURL string) *apiClient {
	return &apiClient{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		http:    &http.Client{Timeout: clientTimeout},
	}
}

func (c *apiClient) createApp(ctx context.Context, name, repoURL string) (store.App, error) {
	var app store.App
	body := map[string]string{"name": name, "repo_url": repoURL}
	if err := c.do(ctx, http.MethodPost, "/apps", body, &app); err != nil {
		return store.App{}, err
	}
	return app, nil
}

func (c *apiClient) listApps(ctx context.Context) (appListResponse, error) {
	var out appListResponse
	if err := c.do(ctx, http.MethodGet, "/apps", nil, &out); err != nil {
		return appListResponse{}, err
	}
	if out.Apps == nil {
		out.Apps = []store.App{}
	}
	return out, nil
}

func (c *apiClient) getApp(ctx context.Context, name string) (store.App, error) {
	var app store.App
	path := "/apps/" + url.PathEscape(name)
	if err := c.do(ctx, http.MethodGet, path, nil, &app); err != nil {
		return store.App{}, err
	}
	return app, nil
}

func (c *apiClient) do(ctx context.Context, method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, payload)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &apiError{Status: resp.StatusCode, Message: apiErrorMessage(resp.Status, data)}
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func apiErrorMessage(status string, data []byte) string {
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(data, &body); err == nil && body.Error != "" {
		return body.Error
	}
	msg := strings.TrimSpace(string(data))
	if msg == "" {
		return status
	}
	return msg
}
