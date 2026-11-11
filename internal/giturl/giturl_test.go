package giturl

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateRef(t *testing.T) {
	t.Parallel()
	valid := []string{
		"",
		"main",
		"feature/add-logs",
		"v1.2.3",
		"abcdef0",
		"A",
		"0123456789abcdef0123456789abcdef01234567",
	}
	for _, ref := range valid {
		if err := ValidateRef(ref); err != nil {
			t.Fatalf("ValidateRef(%q) = %v", ref, err)
		}
	}

	invalid := []string{
		"-main",
		"--upload-pack=evil",
		"feat..ure",
		"a//b",
		"/main",
		"main/",
		"release.",
		"branch.lock",
		"has space",
		"main:other",
		"main;rm",
		"HEAD@{0}",
		"main\n",
		"main\\n",
		strings.Repeat("a", 256),
		".",
		"..",
	}
	for _, ref := range invalid {
		if err := ValidateRef(ref); !errors.Is(err, ErrRefInvalid) {
			t.Fatalf("ValidateRef(%q) = %v, want ErrRefInvalid", ref, err)
		}
	}
}

func TestValidSHA(t *testing.T) {
	t.Parallel()
	if !ValidSHA("0123456789abcdef0123456789abcdef01234567") {
		t.Fatal("expected 40 hex chars to be valid")
	}
	if !ValidSHA(strings.Repeat("ab", 32)) {
		t.Fatal("expected 64 hex chars to be valid")
	}
	if ValidSHA("0123456789abcdef") || ValidSHA("ABCDEF0123456789abcdef0123456789abcdef") {
		t.Fatal("short and uppercase digests are invalid")
	}
}
