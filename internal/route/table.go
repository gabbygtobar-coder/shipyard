// Package route stores which container currently receives traffic for an app.
//
// The table is in memory. A process restart drops it; durable routing is later.
package route

import "sync"

// Target is one running container the proxy can dial.
type Target struct {
	Endpoint     string
	InstanceID   string
	DeploymentID string
}

// Table is a concurrent map of app name to the container receiving traffic.
type Table struct {
	mu   sync.RWMutex
	apps map[string]Target
}

// NewTable returns an empty route table.
func NewTable() *Table {
	return &Table{apps: make(map[string]Target)}
}

// Lookup returns the container currently routed for app.
func (t *Table) Lookup(app string) (Target, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	target, ok := t.apps[app]
	return target, ok
}

// Swap points app at next and returns the previous target.
// The previous target is zero when app had no route.
func (t *Table) Swap(app string, next Target) Target {
	t.mu.Lock()
	defer t.mu.Unlock()
	prev := t.apps[app]
	t.apps[app] = next
	return prev
}

// Clear removes app and returns the target that was routed, if any.
func (t *Table) Clear(app string) (Target, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	prev, ok := t.apps[app]
	delete(t.apps, app)
	return prev, ok
}

// Apps returns the app names that currently have a route.
func (t *Table) Apps() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]string, 0, len(t.apps))
	for name := range t.apps {
		out = append(out, name)
	}
	return out
}
