package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMemoryCreateGetList(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 11, 3, 4, 5, 0, time.FixedZone("EST", -5*3600))
	st := NewMemoryWithClock(func() time.Time { return now })
	ctx := context.Background()

	apps, err := st.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if apps == nil || len(apps) != 0 {
		t.Fatalf("ListApps() = %#v, want empty slice", apps)
	}

	first, err := st.CreateApp(ctx, "  demo  ", "  https://github.com/acme/demo  ")
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.CreateApp(ctx, "api", "https://github.com/acme/api")
	if err != nil {
		t.Fatal(err)
	}

	wantCreated := now.UTC()
	if first.Name != "demo" || first.RepoURL != "https://github.com/acme/demo" || !first.CreatedAt.Equal(wantCreated) {
		t.Fatalf("first = %#v, want trimmed fields at %s", first, wantCreated)
	}
	if second.Name != "api" {
		t.Fatalf("second name = %q", second.Name)
	}

	got, err := st.GetApp(ctx, " demo ")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "demo" || got.RepoURL != first.RepoURL || !got.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("GetApp() = %#v, want %#v", got, first)
	}

	first.Name = "mutated"
	got, err = st.GetApp(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "demo" {
		t.Fatalf("stored name changed to %q", got.Name)
	}

	apps, err = st.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 2 || apps[0].Name != "demo" || apps[1].Name != "api" {
		t.Fatalf("ListApps() = %#v, want demo then api", apps)
	}
}

func TestMemoryCreateConflictsAndInvalid(t *testing.T) {
	t.Parallel()
	st := NewMemory()
	ctx := context.Background()

	if _, err := st.CreateApp(ctx, "demo", "https://github.com/acme/demo"); err != nil {
		t.Fatal(err)
	}
	_, err := st.CreateApp(ctx, "demo", "https://github.com/acme/other")
	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate error = %v, want ErrAlreadyExists", err)
	}

	cases := []struct {
		name    string
		repoURL string
	}{
		{name: "", repoURL: "https://github.com/acme/demo"},
		{name: "   ", repoURL: "https://github.com/acme/demo"},
		{name: "demo", repoURL: ""},
		{name: "demo", repoURL: "   "},
	}
	for _, tc := range cases {
		_, err := st.CreateApp(ctx, tc.name, tc.repoURL)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("CreateApp(%q, %q) error = %v, want ErrInvalid", tc.name, tc.repoURL, err)
		}
	}

	_, err = st.GetApp(ctx, "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing error = %v, want ErrNotFound", err)
	}
	_, err = st.GetApp(ctx, "  ")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank get error = %v, want ErrInvalid", err)
	}
}

func TestMemoryDeployments(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	st := NewMemoryWithClock(func() time.Time { return now })
	ctx := context.Background()

	if _, err := st.CreateApp(ctx, "alpha", "https://github.com/acme/alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateApp(ctx, "beta", "https://github.com/acme/beta"); err != nil {
		t.Fatal(err)
	}

	if _, err := st.CreateDeployment(ctx, "missing", DeploymentStatusPending); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing app error = %v", err)
	}

	first, err := st.CreateDeployment(ctx, " alpha ", " "+DeploymentStatusPending+" ")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != "dep_1" || first.AppName != "alpha" || first.Status != DeploymentStatusPending || !first.CreatedAt.Equal(now) {
		t.Fatalf("first deployment = %#v", first)
	}
	second, err := st.CreateDeployment(ctx, "beta", "rolled_back")
	if err != nil {
		t.Fatal(err)
	}
	third, err := st.CreateDeployment(ctx, "alpha", "failed")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != "dep_2" || third.ID != "dep_3" {
		t.Fatalf("ids = %s %s, want dep_2 dep_3", second.ID, third.ID)
	}

	got, err := st.GetDeployment(ctx, " dep_2 ")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "dep_2" || got.AppName != "beta" {
		t.Fatalf("GetDeployment() = %#v", got)
	}
	got.Status = "mutated"
	got, err = st.GetDeployment(ctx, "dep_2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "rolled_back" {
		t.Fatalf("status changed to %q", got.Status)
	}

	alpha, err := st.ListDeployments(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(alpha) != 2 || alpha[0].ID != "dep_1" || alpha[1].ID != "dep_3" {
		t.Fatalf("alpha deployments = %#v", alpha)
	}
	beta, err := st.ListDeployments(ctx, "beta")
	if err != nil {
		t.Fatal(err)
	}
	if len(beta) != 1 || beta[0].ID != "dep_2" {
		t.Fatalf("beta deployments = %#v", beta)
	}

	if _, err := st.CreateApp(ctx, "gamma", "https://github.com/acme/gamma"); err != nil {
		t.Fatal(err)
	}
	gamma, err := st.ListDeployments(ctx, "gamma")
	if err != nil {
		t.Fatal(err)
	}
	if gamma == nil || len(gamma) != 0 {
		t.Fatalf("gamma deployments = %#v, want empty slice", gamma)
	}
}

func TestMemoryDeploymentValidation(t *testing.T) {
	t.Parallel()
	st := NewMemory()
	ctx := context.Background()
	if _, err := st.CreateApp(ctx, "demo", "https://github.com/acme/demo"); err != nil {
		t.Fatal(err)
	}

	valid := []string{"pending", "a", "rolled_back", "a-b", "a1", strings.Repeat("a", 32)}
	for _, status := range valid {
		if _, err := st.CreateDeployment(ctx, "demo", status); err != nil {
			t.Fatalf("status %q: %v", status, err)
		}
	}

	invalid := []string{"", "   ", "Pending", "has space", "nope!", strings.Repeat("a", 33)}
	for _, status := range invalid {
		_, err := st.CreateDeployment(ctx, "demo", status)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("status %q error = %v, want ErrInvalid", status, err)
		}
	}

	if _, err := st.CreateDeployment(ctx, " ", "pending"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank app error = %v", err)
	}
	if _, err := st.GetDeployment(ctx, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank id error = %v", err)
	}
	if _, err := st.GetDeployment(ctx, "dep_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing id error = %v", err)
	}
	if _, err := st.ListDeployments(ctx, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank list error = %v", err)
	}
	if _, err := st.ListDeployments(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing list error = %v", err)
	}

	fresh := NewMemory()
	if _, err := fresh.CreateApp(ctx, "demo", "https://github.com/acme/demo"); err != nil {
		t.Fatal(err)
	}
	dep, err := fresh.CreateDeployment(ctx, "demo", "pending")
	if err != nil {
		t.Fatal(err)
	}
	if dep.ID != "dep_1" {
		t.Fatalf("id = %s, want dep_1", dep.ID)
	}
}

func TestMemoryRejectedDeploymentDoesNotConsumeID(t *testing.T) {
	t.Parallel()
	st := NewMemory()
	ctx := context.Background()
	if _, err := st.CreateApp(ctx, "demo", "https://github.com/acme/demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateDeployment(ctx, "missing", "pending"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := st.CreateDeployment(ctx, "demo", "NOPE"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	dep, err := st.CreateDeployment(ctx, "demo", "pending")
	if err != nil {
		t.Fatal(err)
	}
	if dep.ID != "dep_1" {
		t.Fatalf("id = %s, want dep_1", dep.ID)
	}
}

func TestMemoryContextCanceled(t *testing.T) {
	t.Parallel()
	st := NewMemory()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := st.CreateApp(ctx, "demo", "https://github.com/acme/demo"); !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateApp error = %v", err)
	}
	if _, err := st.GetApp(ctx, "demo"); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetApp error = %v", err)
	}
	if _, err := st.ListApps(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListApps error = %v", err)
	}
	if _, err := st.CreateDeployment(ctx, "demo", "pending"); !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateDeployment error = %v", err)
	}
	if _, err := st.GetDeployment(ctx, "dep_1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetDeployment error = %v", err)
	}
	if _, err := st.ListDeployments(ctx, "demo"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListDeployments error = %v", err)
	}
}

func TestMemoryNilClock(t *testing.T) {
	t.Parallel()
	st := NewMemoryWithClock(nil)
	before := time.Now().Add(-time.Second)
	app, err := st.CreateApp(context.Background(), "demo", "https://github.com/acme/demo")
	if err != nil {
		t.Fatal(err)
	}
	if app.CreatedAt.Before(before) || app.CreatedAt.Location() != time.UTC {
		t.Fatalf("CreatedAt = %s", app.CreatedAt)
	}
}

func TestMemoryConcurrentCreates(t *testing.T) {
	t.Parallel()
	st := NewMemory()
	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("app-%d", i)
			_, err := st.CreateApp(context.Background(), name, "https://github.com/acme/repo")
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	apps, err := st.ListApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != n {
		t.Fatalf("len(apps) = %d, want %d", len(apps), n)
	}
}

func TestMemoryDuplicateCreateRace(t *testing.T) {
	t.Parallel()
	st := NewMemory()
	var wg sync.WaitGroup
	errs := make([]error, 8)
	wg.Add(len(errs))
	for i := range errs {
		go func(i int) {
			defer wg.Done()
			_, errs[i] = st.CreateApp(context.Background(), "demo", "https://github.com/acme/demo")
		}(i)
	}
	wg.Wait()

	successes, conflicts := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrAlreadyExists):
			conflicts++
		default:
			t.Fatalf("unexpected error %v", err)
		}
	}
	if successes != 1 || conflicts != len(errs)-1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}
