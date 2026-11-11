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

	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

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

type deploymentListResponse struct {
	Deployments []store.Deployment `json:"deployments"`
}

func newAPIClient(baseURL string) *apiClient {
	return &apiClient{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		http:    &http.Client{},
	}
}

func (c *apiClient) createApp(ctx context.Context, name, repoURL, branch string, auto bool, secret string) (store.App, error) {
	var app store.App
	body := map[string]any{"name": name, "repo_url": repoURL}
	if branch != "" {
		body["branch"] = branch
	}
	if auto {
		body["auto_deploy"] = true
	}
	if secret != "" {
		body["webhook_secret"] = secret
	}
	if err := c.do(ctx, http.MethodPost, "/apps", body, &app); err != nil {
		return store.App{}, err
	}
	return app, nil
}

func (c *apiClient) updateApp(ctx context.Context, name string, branchSet bool, branch string, auto *bool, secretSet bool, secret string) (store.App, error) {
	var app store.App
	body := map[string]any{}
	if branchSet {
		body["branch"] = branch
	}
	if auto != nil {
		body["auto_deploy"] = *auto
	}
	if secretSet {
		body["webhook_secret"] = secret
	}
	path := "/apps/" + url.PathEscape(name)
	if err := c.do(ctx, http.MethodPatch, path, body, &app); err != nil {
		return store.App{}, err
	}
	return app, nil
}

func (c *apiClient) rollback(ctx context.Context, name, deploymentID string) (store.Deployment, error) {
	var dep store.Deployment
	body := map[string]string{}
	if deploymentID != "" {
		body["deployment_id"] = deploymentID
	}
	path := "/apps/" + url.PathEscape(name) + "/rollback"
	if err := c.do(ctx, http.MethodPost, path, body, &dep); err != nil {
		return store.Deployment{}, err
	}
	return dep, nil
}

type releaseListResponse struct {
	Releases []store.Release `json:"releases"`
}

func (c *apiClient) releases(ctx context.Context, name string) (releaseListResponse, error) {
	var out releaseListResponse
	path := "/apps/" + url.PathEscape(name) + "/releases"
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return releaseListResponse{}, err
	}
	if out.Releases == nil {
		out.Releases = []store.Release{}
	}
	return out, nil
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

func (c *apiClient) deploy(ctx context.Context, name, ref string) (store.Deployment, error) {
	var dep store.Deployment
	body := map[string]string{"ref": ref}
	path := "/apps/" + url.PathEscape(name) + "/deployments"
	if err := c.do(ctx, http.MethodPost, path, body, &dep); err != nil {
		return store.Deployment{}, err
	}
	return dep, nil
}

func (c *apiClient) listDeployments(ctx context.Context, name string) (deploymentListResponse, error) {
	var out deploymentListResponse
	path := "/apps/" + url.PathEscape(name) + "/deployments"
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return deploymentListResponse{}, err
	}
	if out.Deployments == nil {
		out.Deployments = []store.Deployment{}
	}
	return out, nil
}

func (c *apiClient) getDeployment(ctx context.Context, id string) (store.Deployment, error) {
	var dep store.Deployment
	path := "/deployments/" + url.PathEscape(id)
	if err := c.do(ctx, http.MethodGet, path, nil, &dep); err != nil {
		return store.Deployment{}, err
	}
	return dep, nil
}

func (c *apiClient) streamLogs(ctx context.Context, id string, follow bool, dst io.Writer) error {
	path := "/deployments/" + url.PathEscape(id) + "/logs"
	if follow {
		path += "?follow=1"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	limit := int64(store.MaxDeploymentLogBytes + 4096)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if readErr != nil {
			return readErr
		}
		return &apiError{Status: resp.StatusCode, Message: apiErrorMessage(resp.Status, data)}
	}
	_, err = io.Copy(dst, io.LimitReader(resp.Body, limit))
	return err
}

func (c *apiClient) appURL(ctx context.Context, name string) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	path := "/apps/" + url.PathEscape(name) + "/url"
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return "", err
	}
	return out.URL, nil
}

func (c *apiClient) stopApp(ctx context.Context, name string) (store.App, error) {
	var app store.App
	path := "/apps/" + url.PathEscape(name) + "/stop"
	if err := c.do(ctx, http.MethodPost, path, nil, &app); err != nil {
		return store.App{}, err
	}
	return app, nil
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
