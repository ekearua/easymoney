package logging

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func capture(t *testing.T, format string) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	return New(slog.LevelDebug, format, &buf), &buf
}

func TestRequestIDGeneratedAndEchoed(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := RequestIDFromContext(r.Context())
		if id == "" {
			t.Fatal("expected request id in context")
		}
		if w.Header().Get("X-Request-ID") != id {
			t.Fatal("response header should echo the correlation id")
		}
	})
	rec := httptest.NewRecorder()
	RequestID(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Header().Get("X-Request-ID") == "" {
		t.Fatal("expected X-Request-ID header on response")
	}
}

func TestRequestIDRespectsInboundHeader(t *testing.T) {
	var got string
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = RequestIDFromContext(r.Context())
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "abc-123")
	RequestID(next).ServeHTTP(httptest.NewRecorder(), req)
	if got != "abc-123" {
		t.Fatalf("inbound header should be reused, got %q", got)
	}
}

func TestRecordCarriesRequestID(t *testing.T) {
	logger, buf := capture(t, "json")
	var logged string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.InfoContext(r.Context(), "handled", "path", r.URL.Path)
		logged = r.Context().Value(RequestIDKey).(string)
	})
	RequestID(next).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["request_id"] != logged {
		t.Fatalf("log record should carry request_id %q, got %v", logged, record["request_id"])
	}
}

func TestBackgroundLogsHaveNoRequestID(t *testing.T) {
	logger, buf := capture(t, "json")
	logger.Info("background job")
	if strings.Contains(buf.String(), "request_id") {
		t.Fatal("background logs must not carry a request_id")
	}
}

func TestTextFormat(t *testing.T) {
	logger, buf := capture(t, "text")
	logger.Info("hello")
	if !strings.Contains(buf.String(), "msg=hello") {
		t.Fatalf("text format expected, got %q", buf.String())
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	logger := New(slog.LevelWarn, "json", &buf)
	logger.Info("should not appear")
	if buf.Len() != 0 {
		t.Fatal("info must be filtered at warn level")
	}
}

var _ io.Writer = (*bytes.Buffer)(nil)
