package api

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		wantErr error
	}{
		{name: "single letter", in: "a", wantErr: nil},
		{name: "words", in: "demo-api", wantErr: nil},
		{name: "digits", in: "a1", wantErr: nil},
		{name: "max length", in: "a" + strings.Repeat("b", 62), wantErr: nil},
		{name: "empty", in: "", wantErr: ErrNameRequired},
		{name: "uppercase", in: "Demo", wantErr: ErrNameInvalid},
		{name: "leading digit", in: "1demo", wantErr: ErrNameInvalid},
		{name: "leading hyphen", in: "-demo", wantErr: ErrNameInvalid},
		{name: "trailing hyphen", in: "demo-", wantErr: ErrNameInvalid},
		{name: "double hyphen", in: "demo--api", wantErr: ErrNameInvalid},
		{name: "underscore", in: "demo_api", wantErr: ErrNameInvalid},
		{name: "space", in: "demo api", wantErr: ErrNameInvalid},
		{name: "untrimmed", in: " demo", wantErr: ErrNameInvalid},
		{name: "too long", in: "a" + strings.Repeat("b", 63), wantErr: ErrNameInvalid},
		{name: "unicode", in: "démó", wantErr: ErrNameInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateName(tt.in)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ValidateName(%q) error = %v, want %v", tt.in, err, tt.wantErr)
			}
		})
	}
}

func TestValidateDeploymentID(t *testing.T) {
	t.Parallel()
	if err := ValidateDeploymentID("dep_1"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDeploymentID("dep_42"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "dep_", "dep_0", "dep_01", "dep_1a", "../dep_1"} {
		if err := ValidateDeploymentID(id); !errors.Is(err, ErrDeploymentID) {
			t.Fatalf("ValidateDeploymentID(%q) = %v", id, err)
		}
	}
}

func TestNormalizeRepoURL(t *testing.T) {
	t.Parallel()
	owner39 := "a" + strings.Repeat("b", 38)
	repo100 := "r" + strings.Repeat("e", 99)

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr error
	}{
		{name: "canonical", in: "https://github.com/acme/demo", want: "https://github.com/acme/demo"},
		{name: "git suffix", in: "https://github.com/acme/demo.git", want: "https://github.com/acme/demo"},
		{name: "trailing slash", in: "https://github.com/acme/demo/", want: "https://github.com/acme/demo"},
		{name: "slash and git", in: "https://github.com/acme/demo.git/", want: "https://github.com/acme/demo"},
		{name: "host case and repo case", in: " HTTPS://GitHub.com/Acme/Demo.git ", want: "https://github.com/Acme/Demo"},
		{name: "underscore repo", in: "https://github.com/acme/_demo", want: "https://github.com/acme/_demo"},
		{name: "dotted repo", in: "https://github.com/acme/demo.github.io", want: "https://github.com/acme/demo.github.io"},
		{name: "double slash prefix", in: "https://github.com//acme/demo", want: "https://github.com/acme/demo"},
		{name: "max owner and repo", in: "https://github.com/" + owner39 + "/" + repo100, want: "https://github.com/" + owner39 + "/" + repo100},
		{name: "empty", in: "", wantErr: ErrRepoRequired},
		{name: "whitespace", in: "   ", wantErr: ErrRepoRequired},
		{name: "http", in: "http://github.com/acme/demo", wantErr: ErrRepoInvalid},
		{name: "gitlab", in: "https://gitlab.com/acme/demo", wantErr: ErrRepoInvalid},
		{name: "www", in: "https://www.github.com/acme/demo", wantErr: ErrRepoInvalid},
		{name: "tree path", in: "https://github.com/acme/demo/tree/main", wantErr: ErrRepoInvalid},
		{name: "owner only", in: "https://github.com/acme", wantErr: ErrRepoInvalid},
		{name: "ssh scp", in: "git@github.com:acme/demo.git", wantErr: ErrRepoInvalid},
		{name: "ssh url", in: "ssh://git@github.com/acme/demo.git", wantErr: ErrRepoInvalid},
		{name: "query", in: "https://github.com/acme/demo?ref=1", wantErr: ErrRepoInvalid},
		{name: "fragment", in: "https://github.com/acme/demo#readme", wantErr: ErrRepoInvalid},
		{name: "userinfo", in: "https://user:pass@github.com/acme/demo", wantErr: ErrRepoInvalid},
		{name: "port", in: "https://github.com:443/acme/demo", wantErr: ErrRepoInvalid},
		{name: "encoded slash", in: "https://github.com/acme/demo%2Fevil", wantErr: ErrRepoInvalid},
		{name: "leading hyphen owner", in: "https://github.com/-acme/demo", wantErr: ErrRepoInvalid},
		{name: "trailing hyphen owner", in: "https://github.com/acme-/demo", wantErr: ErrRepoInvalid},
		{name: "double hyphen owner", in: "https://github.com/a--b/demo", wantErr: ErrRepoInvalid},
		{name: "dot owner", in: "https://github.com/acme.io/demo", wantErr: ErrRepoInvalid},
		{name: "owner too long", in: "https://github.com/" + strings.Repeat("a", 40) + "/demo", wantErr: ErrRepoInvalid},
		{name: "repo too long", in: "https://github.com/acme/" + strings.Repeat("a", 101), wantErr: ErrRepoInvalid},
		{name: "dot repo", in: "https://github.com/acme/.hidden", wantErr: ErrRepoInvalid},
		{name: "dotgit repo", in: "https://github.com/acme/.git", wantErr: ErrRepoInvalid},
		{name: "empty repo segment", in: "https://github.com/acme//demo", wantErr: ErrRepoInvalid},
		{name: "not a url", in: "github.com/acme/demo", wantErr: ErrRepoInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizeRepoURL(tt.in)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NormalizeRepoURL(%q) error = %v, want %v", tt.in, err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("NormalizeRepoURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
