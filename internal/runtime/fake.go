package runtime

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
)

// Fake is a Runtime that records calls and returns scripted endpoints.
type Fake struct {
	mu sync.Mutex

	// Endpoint is used when Endpoints is empty.
	Endpoint string
	// Endpoints are consumed in order, one per Start.
	Endpoints []string
	StartErr  error
	StopErr   error
	// OnStop runs after the stop is recorded, without Fake's lock held.
	OnStop func(id string)

	Specs    []Spec
	Stops    []string
	next     int
	started  map[string]Instance
	statuses map[string]ContainerStatus
	logs     map[string]string
}

// Start implements Runtime.
func (f *Fake) Start(_ context.Context, spec Spec) (Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.StartErr != nil {
		return Instance{}, f.StartErr
	}
	if err := validateSpec(spec); err != nil {
		return Instance{}, err
	}
	f.next++
	endpoint := f.Endpoint
	if len(f.Endpoints) > 0 {
		endpoint = f.Endpoints[0]
		f.Endpoints = f.Endpoints[1:]
	}
	if endpoint == "" {
		endpoint = "http://127.0.0.1:9"
	}
	inst := Instance{
		ID:           fmt.Sprintf("%064x", f.next),
		Name:         spec.Name,
		AppName:      spec.AppName,
		DeploymentID: spec.DeploymentID,
		Endpoint:     endpoint,
	}
	f.Specs = append(f.Specs, spec)
	if f.started == nil {
		f.started = make(map[string]Instance)
	}
	f.started[inst.ID] = inst
	return inst, nil
}

// Stop implements Runtime.
func (f *Fake) Stop(_ context.Context, id string) error {
	f.mu.Lock()
	f.Stops = append(f.Stops, id)
	err := f.StopErr
	delete(f.started, id)
	hook := f.OnStop
	f.mu.Unlock()
	if hook != nil {
		hook(id)
	}
	return err
}

// Status implements Runtime.
func (f *Fake) Status(_ context.Context, id string) (ContainerStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.statuses != nil {
		if status, ok := f.statuses[id]; ok {
			return status, nil
		}
	}
	if _, ok := f.started[id]; ok {
		return ContainerStatus{ID: id, Running: true}, nil
	}
	return ContainerStatus{ID: id, Running: false, ExitCode: 1}, nil
}

// Logs implements Runtime.
func (f *Fake) Logs(_ context.Context, id string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	text := ""
	if f.logs != nil {
		text = f.logs[id]
	}
	return io.NopCloser(strings.NewReader(text)), nil
}

// SetStatus scripts Status for id.
func (f *Fake) SetStatus(id string, status ContainerStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.statuses == nil {
		f.statuses = make(map[string]ContainerStatus)
	}
	status.ID = id
	f.statuses[id] = status
}

// SetLogs scripts Logs for id.
func (f *Fake) SetLogs(id, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.logs == nil {
		f.logs = make(map[string]string)
	}
	f.logs[id] = text
}

// FakeChecker is a Checker with a scripted result.
type FakeChecker struct {
	mu    sync.Mutex
	Err   error
	Calls []string
	// WaitFunc overrides Err when set.
	WaitFunc func(ctx context.Context, endpoint string) error
}

// Wait implements Checker.
func (f *FakeChecker) Wait(ctx context.Context, endpoint string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	f.Calls = append(f.Calls, endpoint)
	fn := f.WaitFunc
	err := f.Err
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, endpoint)
	}
	return err
}
