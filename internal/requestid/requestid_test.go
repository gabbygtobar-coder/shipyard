package requestid

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFromRequestAcceptsOrReplaces(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(Header, "trace-1234")
	if got := FromRequest(req); got != "trace-1234" {
		t.Fatalf("got %q", got)
	}
	req.Header.Set(Header, "has space")
	got := FromRequest(req)
	if !Valid(got) || got == "has space" {
		t.Fatalf("replacement %q", got)
	}
	if Valid("") || Valid("short") || Valid("bad/id") {
		t.Fatal("expected invalid ids")
	}
	ctx := With(req.Context(), got)
	if From(ctx) != got {
		t.Fatalf("context %q", From(ctx))
	}
}
