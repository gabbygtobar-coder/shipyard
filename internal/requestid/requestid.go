// Package requestid creates request identifiers and carries them on a context.
//
// The API and the reverse proxy accept an incoming X-Request-ID when it is a
// short token, and otherwise generate one. The same value is written on the
// response, forwarded to the upstream app, and attached to slog records.
package requestid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
)

const (
	// Header is the HTTP header that carries a request id.
	Header = "X-Request-ID"
	// LogKey is the slog attribute name for a request id.
	LogKey = "request_id"
)

type ctxKey struct{}

// New returns a random 32-character hex id.
func New() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "generated"
	}
	return hex.EncodeToString(b[:])
}

// FromRequest returns a valid incoming id, or a new one.
func FromRequest(r *http.Request) string {
	if r == nil {
		return New()
	}
	id := strings.TrimSpace(r.Header.Get(Header))
	if Valid(id) {
		return id
	}
	return New()
}

// Valid reports whether id is safe to log and to forward.
func Valid(id string) bool {
	if len(id) < 8 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

// With stores id on ctx.
func With(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKey{}, id)
}

// From returns the id stored by With, or an empty string.
func From(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// Logger returns base with the request id attached.
// A nil base is returned unchanged.
func Logger(base *slog.Logger, id string) *slog.Logger {
	if base == nil || id == "" {
		return base
	}
	return base.With(LogKey, id)
}
