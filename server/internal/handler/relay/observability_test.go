package relay

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jimeng-relay/server/internal/logging"
	"github.com/jimeng-relay/server/internal/middleware/sigv4"
	"github.com/jimeng-relay/server/internal/relay/upstream"
)

type testLogHandler struct {
	records []slog.Record
}

func (h *testLogHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *testLogHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r)
	return nil
}
func (h *testLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *testLogHandler) WithGroup(name string) slog.Handler       { return h }

func TestObservabilityFields(t *testing.T) {
	innerHandler := &testLogHandler{}
	logger := slog.New(&logging.RedactingHandler{Handler: innerHandler})

	fake := &fakeSubmitClient{
		resp: &upstream.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       []byte(`{"ok":true}`),
		},
	}
	auditSvc, _, _, _ := newTestAuditService(t, nil, nil, nil)
	h := NewSubmitHandler(fake, auditSvc, &mockBillingService{}, nil, nil, logger).Routes()

	req := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader([]byte(`{"prompt":"cat"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", "test-req-id")
	req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k1"))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	found := false
	for _, r := range innerHandler.records {
		if r.Message == "request finished" {
			found = true
			attrs := make(map[string]any)
			r.Attrs(func(a slog.Attr) bool {
				attrs[a.Key] = a.Value.Any()
				return true
			})

			if attrs["request_id"] != "test-req-id" {
				t.Errorf("expected request_id test-req-id, got %v", attrs["request_id"])
			}
			if _, ok := attrs["latency_ms"]; !ok {
				t.Errorf("expected latency_ms to be present")
			}
			if v, ok := attrs["upstream_status"].(int64); !ok || v != int64(http.StatusOK) {
				t.Errorf("expected upstream_status 200, got %v (%T)", attrs["upstream_status"], attrs["upstream_status"])
			}
		}
	}
	if !found {
		t.Fatal("expected 'request finished' log record")
	}
}

func TestObservabilityFields_Error(t *testing.T) {
	innerHandler := &testLogHandler{}
	logger := slog.New(&logging.RedactingHandler{Handler: innerHandler})

	fake := &fakeSubmitClient{
		resp: &upstream.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       []byte(`{"error":"bad request"}`),
		},
	}
	auditSvc, _, _, _ := newTestAuditService(t, nil, nil, nil)
	h := NewSubmitHandler(fake, auditSvc, &mockBillingService{}, nil, nil, logger).Routes()

	req := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader([]byte(`{"prompt":"cat"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", "test-req-id-err")
	req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k1"))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}

	found := false
	for _, r := range innerHandler.records {
		if r.Message == "request finished" {
			found = true
			attrs := make(map[string]any)
			r.Attrs(func(a slog.Attr) bool {
				attrs[a.Key] = a.Value.Any()
				return true
			})

			if attrs["request_id"] != "test-req-id-err" {
				t.Errorf("expected request_id test-req-id-err, got %v", attrs["request_id"])
			}
			if v, ok := attrs["upstream_status"].(int64); !ok || v != int64(http.StatusBadRequest) {
				t.Errorf("expected upstream_status 400, got %v", attrs["upstream_status"])
			}
		}
	}
	if !found {
		t.Fatal("expected 'request finished' log record")
	}
}
