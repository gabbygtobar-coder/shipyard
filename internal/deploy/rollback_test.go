package deploy

import (
	"errors"
	"testing"

	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

func TestSelectRelease(t *testing.T) {
	t.Parallel()
	releases := []store.Release{
		{DeploymentID: "dep_3", ImageTag: "shipyard/demo:333333333333"},
		{DeploymentID: "dep_2", ImageTag: "shipyard/demo:222222222222"},
		{DeploymentID: "dep_1", ImageTag: "shipyard/demo:111111111111"},
	}
	got, err := SelectRelease(releases, "dep_3", "")
	if err != nil || got.DeploymentID != "dep_2" {
		t.Fatalf("previous %#v %v", got, err)
	}
	got, err = SelectRelease(releases, "dep_3", "dep_1")
	if err != nil || got.DeploymentID != "dep_1" {
		t.Fatalf("explicit %#v %v", got, err)
	}
	if _, err := SelectRelease(releases, "dep_3", "dep_3"); !errors.Is(err, ErrAlreadyCurrent) {
		t.Fatalf("current %v", err)
	}
	if _, err := SelectRelease(releases, "dep_3", "dep_9"); !errors.Is(err, ErrReleaseNotFound) {
		t.Fatalf("missing %v", err)
	}
	if _, err := SelectRelease(nil, "", ""); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("empty %v", err)
	}
	got, err = SelectRelease(releases[:1], "", "")
	if err != nil || got.DeploymentID != "dep_3" {
		t.Fatalf("restore %#v %v", got, err)
	}
	if _, err := SelectRelease(releases[:1], "dep_3", ""); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("only current %v", err)
	}
}
