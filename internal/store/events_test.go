package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestMemoryEventLog(t *testing.T) {
	t.Parallel()
	st := NewMemoryWithClock(func() time.Time {
		return time.Date(2026, 11, 11, 3, 4, 5, 0, time.UTC)
	})
	ctx := context.Background()
	if _, err := st.CreateApp(ctx, "demo", "https://github.com/acme/demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendEvent(ctx, Event{Type: EventDeployed, Message: "missing app"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing name err %v", err)
	}
	if _, err := st.AppendEvent(ctx, Event{AppName: "demo", Type: "nope", Message: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad type %v", err)
	}
	first, err := st.AppendEvent(ctx, Event{AppName: "demo", Type: EventDeployed, Message: "one", DeploymentID: "dep_1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.AppendEvent(ctx, Event{AppName: "demo", Type: EventHealthFailed, Message: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || second.ID < first.ID {
		t.Fatalf("ids %d %d", first.ID, second.ID)
	}
	events, err := st.ListEvents(ctx, "demo", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].ID != second.ID || events[1].Message != "one" || events[1].DeploymentID != "dep_1" {
		t.Fatalf("events %#v", events)
	}
	limited, err := st.ListEvents(ctx, "demo", 1)
	if err != nil || len(limited) != 1 || limited[0].ID != second.ID {
		t.Fatalf("limited %#v %v", limited, err)
	}
	if _, err := st.ListEvents(ctx, "missing", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing app %v", err)
	}
	n, err := st.CountActiveDeployments(ctx)
	if err != nil || n != 0 {
		t.Fatalf("active %d %v", n, err)
	}
	if _, err := st.CreateBuild(ctx, "demo", "main"); err != nil {
		t.Fatal(err)
	}
	n, err = st.CountActiveDeployments(ctx)
	if err != nil || n != 1 {
		t.Fatalf("active after queue %d %v", n, err)
	}
	if err := st.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteEventLogSurvivesReopen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "shipyard.db")
	ctx := context.Background()
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateApp(ctx, "demo", "https://github.com/acme/demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendEvent(ctx, Event{AppName: "demo", Type: EventWebhookReceived, Message: "push abc on main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendEvent(ctx, Event{AppName: "demo", Type: EventRolledBack, Message: "rolled back dep_1 as dep_2", DeploymentID: "dep_2"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	events, err := reopened.ListEvents(ctx, "demo", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type != EventRolledBack || events[1].Type != EventWebhookReceived {
		t.Fatalf("events %#v", events)
	}
	if events[0].ID <= events[1].ID {
		t.Fatalf("order %#v", events)
	}
}
