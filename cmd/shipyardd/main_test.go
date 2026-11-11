package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-h"}, &stdout, &stderr); code != 0 {
		t.Fatalf("help code %d stderr %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Usage: shipyardd") {
		t.Fatalf("help stderr %s", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("help wrote logs %s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"extra"}, &stdout, &stderr); code != 2 {
		t.Fatalf("extra code %d stderr %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "unexpected arguments") {
		t.Fatalf("stderr %s", stderr.String())
	}

	stderr.Reset()
	if code := run([]string{"--missing"}, &stdout, &stderr); code != 2 {
		t.Fatalf("bad flag code %d stderr %s", code, stderr.String())
	}
}

func TestRunECSRequiresAWSSettings(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--runtime", "ecs"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "SHIPYARD_AWS_REGION") {
		t.Fatalf("code %d stderr %s", code, stderr.String())
	}
}
