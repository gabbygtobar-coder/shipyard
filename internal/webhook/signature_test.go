package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"zen":"keep it logically awesome"}`)
	secret := "correct-horse"
	valid := sign(secret, body)

	if !Verify(secret, body, valid) {
		t.Fatal("valid signature was rejected")
	}
	if Verify(secret, body, "") {
		t.Fatal("missing signature was accepted")
	}
	if Verify(secret, body, "sha256=") {
		t.Fatal("empty digest was accepted")
	}
	if Verify(secret, body, sign("other-secret", body)) {
		t.Fatal("invalid signature was accepted")
	}
	if Verify("", body, valid) {
		t.Fatal("empty secret was accepted")
	}
	if Verify(secret, []byte(`{"zen":"tampered"}`), valid) {
		t.Fatal("tampered body was accepted")
	}
	if Verify(secret, body, strings.TrimPrefix(valid, "sha256=")) {
		t.Fatal("digest without the prefix was accepted")
	}
	upper := "sha256=" + strings.ToUpper(strings.TrimPrefix(valid, "sha256="))
	if !Verify(secret, body, "  "+upper+"  ") {
		t.Fatal("uppercase hex was rejected")
	}
}

func TestParsePush(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"ref":"refs/heads/main",
		"after":"0123456789ABCDEF0123456789ABCDEF01234567",
		"deleted":false,
		"repository":{"clone_url":"https://github.com/acme/demo.git","full_name":"acme/demo"}
	}`)
	push, err := ParsePush(body)
	if err != nil {
		t.Fatal(err)
	}
	if push.Branch != "main" || push.SHA != "0123456789abcdef0123456789abcdef01234567" || push.Deleted {
		t.Fatalf("push %#v", push)
	}
	if push.RepoURL != "https://github.com/acme/demo.git" {
		t.Fatalf("repo %q", push.RepoURL)
	}

	tag := []byte(`{"ref":"refs/tags/v1","after":"0123456789abcdef0123456789abcdef01234567","repository":{"full_name":"acme/demo"}}`)
	if _, err := ParsePush(tag); !errors.Is(err, ErrIgnored) {
		t.Fatalf("tag error %v", err)
	}
	deleted := []byte(`{"ref":"refs/heads/main","after":"0000000000000000000000000000000000000000","deleted":true,"repository":{"html_url":"https://github.com/acme/demo"}}`)
	push, err = ParsePush(deleted)
	if err != nil || !push.Deleted || push.Branch != "main" {
		t.Fatalf("deleted %#v %v", push, err)
	}
	if RepoURL(deleted) != "https://github.com/acme/demo" {
		t.Fatalf("html url %q", RepoURL(deleted))
	}
}
