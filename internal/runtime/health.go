package runtime

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"
)

// PassChecker reports healthy without an HTTP call.
// The ECS runtime waits for the load balancer target inside Start, so the
// supervisor does not probe the public URL a second time.
type PassChecker struct{}

// Wait implements Checker.
func (PassChecker) Wait(context.Context, string) error {
	return nil
}

// HTTPChecker polls an HTTP path until it returns a 2xx status or ctx ends.
// A nil Client uses a short timeout and does not follow redirects.
type HTTPChecker struct {
	Path     string
	Interval time.Duration
	Client   *http.Client
}

// Wait implements Checker.
func (c HTTPChecker) Wait(ctx context.Context, endpoint string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := healthURL(endpoint, c.Path)
	if err != nil {
		return err
	}
	interval := c.Interval
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	client := c.Client
	if client == nil {
		client = &http.Client{
			Timeout: interval,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	var last error
	for {
		last = c.probe(ctx, client, target)
		if last == nil {
			return nil
		}
		if ctx.Err() != nil {
			return healthWaitError(ctx.Err())
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return healthWaitError(ctx.Err())
		case <-timer.C:
		}
	}
}

func (c HTTPChecker) probe(ctx context.Context, client *http.Client, target string) error {
	return probeOnce(ctx, client, target)
}

// ProbeOnce performs one health GET. A 2xx response is healthy.
// A nil client uses a two-second timeout and does not follow redirects.
func ProbeOnce(ctx context.Context, endpoint, path string, client *http.Client) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := healthURL(endpoint, path)
	if err != nil {
		return err
	}
	if client == nil {
		client = &http.Client{
			Timeout: 2 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return probeOnce(ctx, client, target)
}

func probeOnce(ctx context.Context, client *http.Client, target string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "shipyard-health")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ErrUnhealthy
	}
	return nil
}

func healthURL(endpoint, path string) (string, error) {
	if path == "" {
		path = "/"
	}
	base, err := url.Parse(endpoint)
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return "", ErrInvalidSpec
	}
	ref, err := url.Parse(path)
	if err != nil || ref.Host != "" || ref.Path == "" {
		return "", ErrInvalidSpec
	}
	return base.ResolveReference(ref).String(), nil
}

func healthWaitError(ctxErr error) error {
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		return ErrHealthTimeout
	}
	return ctxErr
}
