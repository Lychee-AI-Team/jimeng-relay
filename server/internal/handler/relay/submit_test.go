package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jimeng-relay/server/internal/config"
	internalerrors "github.com/jimeng-relay/server/internal/errors"
	"github.com/jimeng-relay/server/internal/middleware/sigv4"
	"github.com/jimeng-relay/server/internal/models"
	"github.com/jimeng-relay/server/internal/relay/upstream"
	"github.com/jimeng-relay/server/internal/repository"
	auditservice "github.com/jimeng-relay/server/internal/service/audit"
	billingservice "github.com/jimeng-relay/server/internal/service/billing"
	idempotencyservice "github.com/jimeng-relay/server/internal/service/idempotency"
)

type recordingDownstreamRepo struct {
	err     error
	created []models.DownstreamRequest
}

func (r *recordingDownstreamRepo) Create(_ context.Context, request models.DownstreamRequest) error {
	if r.err != nil {
		return r.err
	}
	r.created = append(r.created, request)
	return nil
}

func (r *recordingDownstreamRepo) GetByID(_ context.Context, _ string) (models.DownstreamRequest, error) {
	return models.DownstreamRequest{}, errors.New("not implemented")
}

func (r *recordingDownstreamRepo) GetByRequestID(_ context.Context, _ string) (models.DownstreamRequest, error) {
	return models.DownstreamRequest{}, errors.New("not implemented")
}

type recordingUpstreamRepo struct {
	err     error
	created []models.UpstreamAttempt
}

func (r *recordingUpstreamRepo) Create(_ context.Context, attempt models.UpstreamAttempt) error {
	if r.err != nil {
		return r.err
	}
	r.created = append(r.created, attempt)
	return nil
}

func (r *recordingUpstreamRepo) ListByRequestID(_ context.Context, _ string) ([]models.UpstreamAttempt, error) {
	return nil, errors.New("not implemented")
}

type recordingAuditRepo struct {
	err     error
	created []models.AuditEvent
}

func (r *recordingAuditRepo) Create(_ context.Context, event models.AuditEvent) error {
	if r.err != nil {
		return r.err
	}
	r.created = append(r.created, event)
	return nil
}

func (r *recordingAuditRepo) ListByRequestID(_ context.Context, _ string) ([]models.AuditEvent, error) {
	return nil, errors.New("not implemented")
}

func (r *recordingAuditRepo) ListByTimeRange(_ context.Context, _, _ time.Time) ([]models.AuditEvent, error) {
	return nil, errors.New("not implemented")
}

type recordingIdempotencyRepo struct {
	errOnGet       error
	errOnCreate    error
	errOnDelete    error
	getByKeyCalls  int
	createCalls    int
	deleteCalls    int
	createdRecords []models.IdempotencyRecord
	byKey          map[string]models.IdempotencyRecord
}

func newRecordingIdempotencyRepo() *recordingIdempotencyRepo {
	return &recordingIdempotencyRepo{byKey: make(map[string]models.IdempotencyRecord)}
}

func (r *recordingIdempotencyRepo) GetByKey(_ context.Context, key string) (models.IdempotencyRecord, error) {
	r.getByKeyCalls++
	if r.errOnGet != nil {
		return models.IdempotencyRecord{}, r.errOnGet
	}
	rec, ok := r.byKey[key]
	if !ok {
		return models.IdempotencyRecord{}, repository.ErrNotFound
	}
	return rec, nil
}

func (r *recordingIdempotencyRepo) Create(_ context.Context, record models.IdempotencyRecord) error {
	r.createCalls++
	if r.errOnCreate != nil {
		return r.errOnCreate
	}
	r.createdRecords = append(r.createdRecords, record)
	r.byKey[record.IdempotencyKey] = record
	return nil
}

func (r *recordingIdempotencyRepo) DeleteExpired(_ context.Context, _ time.Time) (int64, error) {
	r.deleteCalls++
	if r.errOnDelete != nil {
		return 0, r.errOnDelete
	}
	return 0, nil
}

func newTestAuditService(t *testing.T, dsErr, usErr, aeErr error) (*auditservice.Service, *recordingDownstreamRepo, *recordingUpstreamRepo, *recordingAuditRepo) {
	t.Helper()
	base := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	ds := &recordingDownstreamRepo{err: dsErr}
	us := &recordingUpstreamRepo{err: usErr}
	ae := &recordingAuditRepo{err: aeErr}
	rnd := bytes.NewReader(bytes.Repeat([]byte{0x01}, 4096))
	svc := auditservice.NewService(ds, us, ae, auditservice.Config{Now: func() time.Time { return base }, Random: rnd})
	return svc, ds, us, ae
}

type fakeSubmitClient struct {
	resp *upstream.Response
	err  error

	calls      int
	reqBody    []byte
	reqHeaders http.Header
	apiKeyID   string
}

func (f *fakeSubmitClient) Submit(ctx context.Context, body []byte, headers http.Header) (*upstream.Response, error) {
	f.calls++
	f.reqBody = append([]byte(nil), body...)
	f.reqHeaders = headers.Clone()
	f.apiKeyID = upstream.GetAPIKeyID(ctx)
	return f.resp, f.err
}

type mockBillingService struct {
	err error
}

func (m *mockBillingService) PreAuthorize(ctx context.Context, requestID, apiKeyID string, requestBody []byte) (billingservice.PreAuthResult, error) {
	if m.err != nil {
		return billingservice.PreAuthResult{}, m.err
	}
	return billingservice.PreAuthResult{
		LedgerID:  "bled_test",
		RequestID: requestID,
		APIKeyID:  apiKeyID,
	}, nil
}

func TestSubmitHandler_PassthroughSuccess(t *testing.T) {
	upstreamBody := []byte(`{"code":10000,"message":"ok","data":{"task_id":"task_123"}}`)
	fake := &fakeSubmitClient{
		resp: &upstream.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type":     []string{"application/json; charset=utf-8"},
				"X-Upstream-Trace": []string{"trace-1"},
			},
			Body: upstreamBody,
		},
	}
	auditSvc, dsRepo, usRepo, aeRepo := newTestAuditService(t, nil, nil, nil)
	h := NewSubmitHandler(fake, auditSvc, &mockBillingService{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil))).Routes()

	requestBody := []byte(`{"prompt":"cat","req_key":"jimeng_t2i_v40"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 fake")
	req.Header.Set("X-Request-Id", "req-1")
	req.Header.Set("X-Trace-Id", "trace-downstream")
	req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k1"))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), upstreamBody) {
		t.Fatalf("expected body passthrough, got %q", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("expected content-type passthrough, got %q", got)
	}
	if fake.calls != 1 {
		t.Fatalf("expected 1 upstream call, got %d", fake.calls)
	}
	if !bytes.Equal(fake.reqBody, requestBody) {
		t.Fatalf("expected upstream request body passthrough")
	}
	if fake.reqHeaders.Get("Authorization") != "" {
		t.Fatalf("authorization header should not be forwarded to upstream client")
	}
	if fake.apiKeyID != "k1" {
		t.Fatalf("expected apiKeyID k1, got %q", fake.apiKeyID)
	}
	if len(dsRepo.created) != 1 || len(usRepo.created) != 1 || len(aeRepo.created) != 1 {
		t.Fatalf("expected full audit chain writes, got downstream=%d upstream=%d events=%d", len(dsRepo.created), len(usRepo.created), len(aeRepo.created))
	}
	if dsRepo.created[0].RequestID != "req-1" || usRepo.created[0].RequestID != "req-1" || aeRepo.created[0].RequestID != "req-1" {
		t.Fatalf("expected request_id propagated to audit chain")
	}
	if dsRepo.created[0].Headers["Authorization"] != "***" {
		t.Fatalf("expected downstream authorization redacted")
	}
	if v, ok := aeRepo.created[0].Metadata[models.AuditMetaComputedCredit].(string); !ok || v == "" {
		t.Fatalf("expected audit metadata %s set", models.AuditMetaComputedCredit)
	}
	if v, ok := aeRepo.created[0].Metadata[models.AuditMetaPreAuthID].(string); !ok || v != "bled_test" {
		t.Fatalf("expected audit metadata %s=bled_test, got %T %v", models.AuditMetaPreAuthID, aeRepo.created[0].Metadata[models.AuditMetaPreAuthID], aeRepo.created[0].Metadata[models.AuditMetaPreAuthID])
	}
	if v, ok := aeRepo.created[0].Metadata[models.AuditMetaResultState].(string); !ok || v != "preauth" {
		t.Fatalf("expected audit metadata %s=preauth, got %T %v", models.AuditMetaResultState, aeRepo.created[0].Metadata[models.AuditMetaResultState], aeRepo.created[0].Metadata[models.AuditMetaResultState])
	}
}

func TestSubmitHandler_PassthroughBusinessError(t *testing.T) {
	upstreamBody := []byte(`{"code":50430,"status":50430,"message":"Request Has Reached API Concurrent Limit"}`)
	fake := &fakeSubmitClient{
		resp: &upstream.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       upstreamBody,
		},
		err: internalerrors.New(internalerrors.ErrUpstreamFailed, "upstream submit returned 429", nil),
	}
	auditSvc, dsRepo, usRepo, aeRepo := newTestAuditService(t, nil, nil, nil)
	h := NewSubmitHandler(fake, auditSvc, &mockBillingService{}, nil, nil, nil).Routes()

	req := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader([]byte(`{"prompt":"cat"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", "req-2")
	req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k1"))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status 429, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), upstreamBody) {
		t.Fatalf("expected business error body passthrough, got %q", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected content-type passthrough, got %q", got)
	}
	if len(dsRepo.created) != 1 || len(usRepo.created) != 1 || len(aeRepo.created) != 1 {
		t.Fatalf("expected full audit chain writes, got downstream=%d upstream=%d events=%d", len(dsRepo.created), len(usRepo.created), len(aeRepo.created))
	}
	if usRepo.created[0].ResponseStatus != http.StatusTooManyRequests {
		t.Fatalf("expected upstream attempt to record response status")
	}
}

func TestSubmitHandler_UpstreamNetworkError(t *testing.T) {
	fake := &fakeSubmitClient{
		err: internalerrors.New(internalerrors.ErrUpstreamFailed, "upstream request failed", errors.New("dial tcp: i/o timeout")),
	}
	auditSvc, _, _, _ := newTestAuditService(t, nil, nil, nil)
	h := NewSubmitHandler(fake, auditSvc, &mockBillingService{}, nil, nil, nil).Routes()

	req := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader([]byte(`{"prompt":"cat"}`)))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k1"))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected status 502, got %d", rec.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal response: %v", err)
	}
	errorObj, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error object, got %T", payload["error"])
	}
	code, ok := errorObj["code"].(string)
	if !ok {
		t.Fatalf("expected error code string, got %T", errorObj["code"])
	}
	if code != string(internalerrors.ErrUpstreamFailed) {
		t.Fatalf("expected error code %q, got %q", internalerrors.ErrUpstreamFailed, code)
	}
}

func TestSubmitHandler_KeyRevokedError_PropagatesUnauthorized(t *testing.T) {
	fake := &fakeSubmitClient{
		err: internalerrors.New(internalerrors.ErrKeyRevoked, "api key revoked", nil),
	}
	auditSvc, _, _, _ := newTestAuditService(t, nil, nil, nil)
	h := NewSubmitHandler(fake, auditSvc, &mockBillingService{}, nil, nil, nil).Routes()

	req := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader([]byte(`{"prompt":"cat"}`)))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k1"))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal response: %v", err)
	}
	errorObj, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error object, got %T", payload["error"])
	}
	code, ok := errorObj["code"].(string)
	if !ok {
		t.Fatalf("expected error code string, got %T", errorObj["code"])
	}
	if code != string(internalerrors.ErrKeyRevoked) {
		t.Fatalf("expected error code %q, got %q", internalerrors.ErrKeyRevoked, code)
	}
}

func TestSubmitHandler_CompatibleActionPath(t *testing.T) {
	upstreamBody := []byte(`{"code":10000,"message":"ok"}`)
	fake := &fakeSubmitClient{resp: &upstream.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: upstreamBody}}
	auditSvc, _, _, _ := newTestAuditService(t, nil, nil, nil)
	h := NewSubmitHandler(fake, auditSvc, &mockBillingService{}, nil, nil, nil).Routes()

	req := httptest.NewRequest(http.MethodPost, "/?Action=CVSync2AsyncSubmitTask&Version=2022-08-31", bytes.NewReader([]byte(`{"prompt":"cat"}`)))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k1"))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), upstreamBody) {
		t.Fatalf("expected body passthrough for compatible path")
	}
}

func TestSubmitHandler_AuditFailure_FailClosed(t *testing.T) {
	upstreamBody := []byte(`{"code":10000,"message":"ok","data":{"task_id":"task_123"}}`)
	fake := &fakeSubmitClient{resp: &upstream.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: upstreamBody}}
	auditSvc, _, _, _ := newTestAuditService(t, errors.New("db down"), nil, nil)
	h := NewSubmitHandler(fake, auditSvc, &mockBillingService{}, nil, nil, nil).Routes()

	req := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader([]byte(`{"prompt":"cat"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", "req-audit-fail")
	req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k1"))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.calls != 0 {
		t.Fatalf("expected audit failure to short-circuit before upstream, got calls=%d", fake.calls)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal response: %v", err)
	}
	errorObj, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error object, got %T", payload["error"])
	}
	code, ok := errorObj["code"].(string)
	if !ok {
		t.Fatalf("expected error code string, got %T", errorObj["code"])
	}
	if code != string(internalerrors.ErrAuditFailed) {
		t.Fatalf("expected error code %q, got %q", internalerrors.ErrAuditFailed, code)
	}
}

func TestSubmitHandler_FakeUpstreamContract_Passthrough(t *testing.T) {
	tests := []struct {
		name                string
		upstreamStatus      int
		upstreamContentType string
		upstreamBody        []byte
	}{
		{
			name:                "success passthrough",
			upstreamStatus:      http.StatusOK,
			upstreamContentType: "application/json; charset=utf-8",
			upstreamBody:        []byte(`{"code":10000,"message":"ok","data":{"task_id":"task_ok"}}`),
		},
		{
			name:                "business error passthrough",
			upstreamStatus:      http.StatusBadRequest,
			upstreamContentType: "application/problem+json",
			upstreamBody:        []byte(`{"code":50013,"status":50013,"message":"invalid prompt","detail":{"hint":"trim input"}}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("Action") != "CVSync2AsyncSubmitTask" {
					t.Fatalf("unexpected Action query: %s", r.URL.RawQuery)
				}
				if r.Method != http.MethodPost {
					t.Fatalf("unexpected method %s", r.Method)
				}
				w.Header().Set("Content-Type", tt.upstreamContentType)
				w.WriteHeader(tt.upstreamStatus)
				if _, err := w.Write(tt.upstreamBody); err != nil {
					return
				}
			}))
			defer fakeUpstream.Close()

			c, err := upstream.NewClient(config.Config{
				Credentials: config.Credentials{AccessKey: "ak_upstream", SecretKey: "sk_upstream"},
				Host:        fakeUpstream.URL,
				Region:      "cn-north-1",
			}, upstream.Options{})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			auditSvc, _, _, _ := newTestAuditService(t, nil, nil, nil)
			h := NewSubmitHandler(c, auditSvc, &mockBillingService{}, nil, nil, nil).Routes()
			req := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader([]byte(`{"prompt":"cat"}`)))
			req.Header.Set("Content-Type", "application/json")
			req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k1"))
			rec := httptest.NewRecorder()

			h.ServeHTTP(rec, req)

			if rec.Code != tt.upstreamStatus {
				t.Fatalf("expected status %d, got %d", tt.upstreamStatus, rec.Code)
			}
			if !bytes.Equal(rec.Body.Bytes(), tt.upstreamBody) {
				t.Fatalf("expected body passthrough: %q, got %q", string(tt.upstreamBody), rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != tt.upstreamContentType {
				t.Fatalf("expected content-type passthrough %q, got %q", tt.upstreamContentType, got)
			}
		})
	}
}

func TestSubmitHandler_IdempotencyReplaySameHash(t *testing.T) {
	upstreamBody := []byte(`{"code":10000,"message":"ok","data":{"task_id":"task_123"}}`)
	fake := &fakeSubmitClient{resp: &upstream.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: upstreamBody}}
	auditSvc, dsRepo, usRepo, aeRepo := newTestAuditService(t, nil, nil, nil)
	idemRepo := newRecordingIdempotencyRepo()
	base := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	idemSvc := idempotencyservice.NewService(idemRepo, idempotencyservice.Config{Now: func() time.Time { return base }})
	h := NewSubmitHandler(fake, auditSvc, &mockBillingService{}, idemSvc, idemRepo, nil).Routes()

	body := []byte(`{"prompt":"cat","req_key":"jimeng_t2i_v40"}`)
	req1 := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("Idempotency-Key", "idem-1")
	req1 = req1.WithContext(context.WithValue(req1.Context(), sigv4.ContextAPIKeyID, "k1"))
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("expected status 200 on first request, got %d body=%s", rec1.Code, rec1.Body.String())
	}
	if fake.calls != 1 {
		t.Fatalf("expected first request to hit upstream once, got %d", fake.calls)
	}
	if idemRepo.createCalls != 1 {
		t.Fatalf("expected first request to create idempotency record, got %d", idemRepo.createCalls)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Idempotency-Key", "idem-1")
	req2 = req2.WithContext(context.WithValue(req2.Context(), sigv4.ContextAPIKeyID, "k1"))
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected status 200 on replay request, got %d body=%s", rec2.Code, rec2.Body.String())
	}
	if rec2.Body.String() != string(upstreamBody) {
		t.Fatalf("expected replay body match, got %q", rec2.Body.String())
	}
	if fake.calls != 1 {
		t.Fatalf("expected replay to skip upstream call, got calls=%d", fake.calls)
	}
	if idemRepo.createCalls != 1 {
		t.Fatalf("expected replay not creating a second idempotency record, got %d", idemRepo.createCalls)
	}
	if len(dsRepo.created) != 1 || len(usRepo.created) != 1 || len(aeRepo.created) != 1 {
		t.Fatalf("expected audit writes only for first call, got downstream=%d upstream=%d events=%d", len(dsRepo.created), len(usRepo.created), len(aeRepo.created))
	}
}

func TestSubmitHandler_IdempotencyHashMismatchReturnsValidationError(t *testing.T) {
	upstreamBody := []byte(`{"code":10000,"message":"ok","data":{"task_id":"task_123"}}`)
	fake := &fakeSubmitClient{resp: &upstream.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: upstreamBody}}
	auditSvc, _, _, _ := newTestAuditService(t, nil, nil, nil)
	idemRepo := newRecordingIdempotencyRepo()
	base := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	idemSvc := idempotencyservice.NewService(idemRepo, idempotencyservice.Config{Now: func() time.Time { return base }})
	h := NewSubmitHandler(fake, auditSvc, &mockBillingService{}, idemSvc, idemRepo, nil).Routes()

	req1 := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader([]byte(`{"prompt":"cat"}`)))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("Idempotency-Key", "idem-2")
	req1 = req1.WithContext(context.WithValue(req1.Context(), sigv4.ContextAPIKeyID, "k1"))
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("expected status 200 on first request, got %d body=%s", rec1.Code, rec1.Body.String())
	}
	if fake.calls != 1 {
		t.Fatalf("expected first request to hit upstream once, got %d", fake.calls)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader([]byte(`{"prompt":"dog"}`)))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Idempotency-Key", "idem-2")
	req2 = req2.WithContext(context.WithValue(req2.Context(), sigv4.ContextAPIKeyID, "k1"))
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400 on hash mismatch, got %d body=%s", rec2.Code, rec2.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal mismatch response: %v", err)
	}
	errorObj, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error object, got %T", payload["error"])
	}
	code, ok := errorObj["code"].(string)
	if !ok {
		t.Fatalf("expected error code string, got %T", errorObj["code"])
	}
	if code != string(internalerrors.ErrValidationFailed) {
		t.Fatalf("expected validation error code, got %q", code)
	}
	if fake.calls != 1 {
		t.Fatalf("expected hash mismatch to skip upstream call, got calls=%d", fake.calls)
	}
}

func TestSubmitHandler_MissingAPIKeyID(t *testing.T) {
	fake := &fakeSubmitClient{}
	auditSvc, _, _, _ := newTestAuditService(t, nil, nil, nil)
	h := NewSubmitHandler(fake, auditSvc, &mockBillingService{}, nil, nil, nil).Routes()

	req := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader([]byte(`{"prompt":"cat"}`)))
	req.Header.Set("Content-Type", "application/json")
	// No APIKeyID in context
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal response: %v", err)
	}
	errorObj, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error object, got %T", payload["error"])
	}
	code, ok := errorObj["code"].(string)
	if !ok {
		t.Fatalf("expected error code string, got %T", errorObj["code"])
	}
	if code != string(internalerrors.ErrAuthFailed) {
		t.Fatalf("expected error code %q, got %q", internalerrors.ErrAuthFailed, code)
	}
}

func TestSubmitHandler_EmptyAPIKeyID(t *testing.T) {
	fake := &fakeSubmitClient{}
	auditSvc, _, _, _ := newTestAuditService(t, nil, nil, nil)
	h := NewSubmitHandler(fake, auditSvc, &mockBillingService{}, nil, nil, nil).Routes()

	req := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader([]byte(`{"prompt":"cat"}`)))
	req.Header.Set("Content-Type", "application/json")
	// Empty APIKeyID in context
	req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "  "))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}
func TestSubmitHandler_RateLimitedError_PropagatesTooManyRequests(t *testing.T) {
	fake := &fakeSubmitClient{
		err: internalerrors.New(internalerrors.ErrRateLimited, "rate limit exceeded", nil),
	}
	auditSvc, _, _, _ := newTestAuditService(t, nil, nil, nil)
	h := NewSubmitHandler(fake, auditSvc, &mockBillingService{}, nil, nil, nil).Routes()

	req := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader([]byte(`{"prompt":"cat"}`)))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k1"))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status 429, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal response: %v", err)
	}
	errorObj, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error object, got %T", payload["error"])
	}
	code, ok := errorObj["code"].(string)
	if !ok {
		t.Fatalf("expected error code string, got %T", errorObj["code"])
	}
	if code != string(internalerrors.ErrRateLimited) {
		t.Fatalf("expected error code %q, got %q", internalerrors.ErrRateLimited, code)
	}
}
func TestSubmitHandler_StatusMapping(t *testing.T) {
	tests := []struct {
		name           string
		err            error
		expectedStatus int
		expectedCode   internalerrors.Code
	}{
		{
			name:           "key expired -> 401",
			err:            internalerrors.New(internalerrors.ErrKeyExpired, "key expired", nil),
			expectedStatus: http.StatusUnauthorized,
			expectedCode:   internalerrors.ErrKeyExpired,
		},
		{
			name:           "invalid signature -> 401",
			err:            internalerrors.New(internalerrors.ErrInvalidSignature, "invalid signature", nil),
			expectedStatus: http.StatusUnauthorized,
			expectedCode:   internalerrors.ErrInvalidSignature,
		},
		{
			name:           "validation failed -> 400",
			err:            internalerrors.New(internalerrors.ErrValidationFailed, "invalid input", nil),
			expectedStatus: http.StatusBadRequest,
			expectedCode:   internalerrors.ErrValidationFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeSubmitClient{err: tt.err}
			auditSvc, _, _, _ := newTestAuditService(t, nil, nil, nil)
			h := NewSubmitHandler(fake, auditSvc, &mockBillingService{}, nil, nil, nil).Routes()

			req := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader([]byte(`{"prompt":"cat"}`)))
			req.Header.Set("Content-Type", "application/json")
			req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k1"))
			rec := httptest.NewRecorder()

			h.ServeHTTP(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Fatalf("expected status %d, got %d body=%s", tt.expectedStatus, rec.Code, rec.Body.String())
			}
			var payload map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("json.Unmarshal response: %v", err)
			}
			errorObj, ok := payload["error"].(map[string]any)
			if !ok {
				t.Fatalf("expected error object, got %T", payload["error"])
			}
			code, ok := errorObj["code"].(string)
			if !ok {
				t.Fatalf("expected error code string, got %T", errorObj["code"])
			}
			if code != string(tt.expectedCode) {
				t.Fatalf("expected error code %q, got %q", tt.expectedCode, code)
			}
		})
	}
}
func TestSubmitHandler_WrappedRateLimited_ReturnsBadGateway(t *testing.T) {
	// If we wrap a specific error in ErrUpstreamFailed, it should return 502, not the specific status.
	// This ensures that our handler logic for NOT wrapping is what's providing the 429.
	fake := &fakeSubmitClient{
		err: internalerrors.New(internalerrors.ErrUpstreamFailed, "wrapped", internalerrors.New(internalerrors.ErrRateLimited, "rate limit", nil)),
	}
	auditSvc, _, _, _ := newTestAuditService(t, nil, nil, nil)
	h := NewSubmitHandler(fake, auditSvc, &mockBillingService{}, nil, nil, nil).Routes()

	req := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader([]byte(`{"prompt":"cat"}`)))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k1"))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected status 502 for wrapped error, got %d", rec.Code)
	}
}

func TestSubmitHandler_BodyLarge_ShouldBeAccepted(t *testing.T) {
	// Ensure we accept realistic larger payloads (e.g. dual-image inline video requests)
	// while still enforcing an upper bound via maxDownstreamBodyBytes.
	upstreamBody := []byte(`{"code":10000,"message":"ok"}`)
	fake := &fakeSubmitClient{resp: &upstream.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: upstreamBody}}
	auditSvc, _, _, _ := newTestAuditService(t, nil, nil, nil)
	h := NewSubmitHandler(fake, auditSvc, &mockBillingService{}, nil, nil, nil).Routes()

	largeBody := make([]byte, 15<<20) // 15MiB
	req := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader(largeBody))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k1"))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200 for 15MiB payload, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSubmitHandler_BodyTooLarge(t *testing.T) {
	fake := &fakeSubmitClient{}
	auditSvc, _, _, _ := newTestAuditService(t, nil, nil, nil)
	h := NewSubmitHandler(fake, auditSvc, &mockBillingService{}, nil, nil, nil).Routes()

	// maxDownstreamBodyBytes + 1 byte should fail.
	largeBody := make([]byte, int(maxDownstreamBodyBytes)+1)
	req := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader(largeBody))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k1"))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected status 413, got %d", rec.Code)
	}
}

func TestSubmitHandler_BodyNearLimit(t *testing.T) {
	upstreamBody := []byte(`{"code":10000,"message":"ok"}`)
	fake := &fakeSubmitClient{resp: &upstream.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: upstreamBody}}
	auditSvc, _, _, _ := newTestAuditService(t, nil, nil, nil)
	h := NewSubmitHandler(fake, auditSvc, &mockBillingService{}, nil, nil, nil).Routes()

	// maxDownstreamBodyBytes should be accepted.
	nearLimitBody := make([]byte, int(maxDownstreamBodyBytes))
	req := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader(nearLimitBody))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k1"))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.calls != 1 {
		t.Fatalf("expected 1 upstream call, got %d", fake.calls)
	}
}

func TestSubmitHandler_UpstreamErrorPassthrough(t *testing.T) {
	upstreamBody := []byte(`{"code":50000,"message":"internal server error"}`)
	fake := &fakeSubmitClient{
		resp: &upstream.Response{
			StatusCode: http.StatusInternalServerError,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
				"X-Request-Id": []string{"upstream-req-id"},
			},
			Body: upstreamBody,
		},
		err: internalerrors.New(internalerrors.ErrUpstreamFailed, "upstream error", nil),
	}
	auditSvc, _, _, _ := newTestAuditService(t, nil, nil, nil)
	h := NewSubmitHandler(fake, auditSvc, &mockBillingService{}, nil, nil, nil).Routes()

	req := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader([]byte(`{"prompt":"cat"}`)))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k1"))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), upstreamBody) {
		t.Fatalf("expected body passthrough, got %q", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected content-type passthrough, got %q", got)
	}
	// This is expected to FAIL currently as Request-Id is not passed through in util.go
	if got := rec.Header().Get("X-Request-Id"); got != "upstream-req-id" {
		t.Fatalf("expected x-request-id passthrough, got %q", got)
	}
}
