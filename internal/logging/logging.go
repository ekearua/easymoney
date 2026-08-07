// Package logging provides the service's structured log schema: JSON or text
// output at a configurable level, plus per-request correlation IDs injected
// into every record emitted while handling a request (C5).
package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
)

type requestIDKey struct{}

// RequestIDKey is exported for callers that need the raw context value.
var RequestIDKey = requestIDKey{}

// RequestID middleware sets or reuses a correlation ID for the request, puts
// it in the context, and echoes it on the X-Request-ID response header.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" || len(id) > 64 {
			id = newID()
		}
		ctx := context.WithValue(r.Context(), RequestIDKey, id)
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func newID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(raw)
}

// RequestIDFromContext returns the correlation ID, if any.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(RequestIDKey).(string)
	return id
}

// contextHandler decorates a slog.Handler so every record emitted inside a
// request carries the correlation ID.
type contextHandler struct {
	inner slog.Handler
}

// WithRequestID wraps h with context-aware record decoration.
func WithRequestID(h slog.Handler) slog.Handler {
	return contextHandler{inner: h}
}

func (h contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h contextHandler) Handle(ctx context.Context, record slog.Record) error {
	if id := RequestIDFromContext(ctx); id != "" {
		record = record.Clone()
		record.AddAttrs(slog.String("request_id", id))
	}
	return h.inner.Handle(ctx, record)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{inner: h.inner.WithGroup(name)}
}

// New builds the service logger. format is "json" or "text"; the JSON schema
// is used by log shippers (retention target: 1 year of rotated logs).
func New(level slog.Level, format string, out io.Writer) *slog.Logger {
	var handler slog.Handler
	opts := &slog.HandlerOptions{Level: level}
	if format == "text" {
		handler = slog.NewTextHandler(out, opts)
	} else {
		handler = slog.NewJSONHandler(out, opts)
	}
	return slog.New(WithRequestID(handler))
}
