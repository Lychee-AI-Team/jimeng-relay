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
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jimeng-relay/server/internal/config"
	internalerrors "github.com/jimeng-relay/server/internal/errors"
	"github.com/jimeng-relay/server/internal/middleware/sigv4"
	"github.com/jimeng-relay/server/internal/models"
	"github.com/jimeng-relay/server/internal/relay/upstream"
	auditservice "github.com/jimeng-relay/server/internal/service/audit"
	"github.com/jimeng-relay/server/internal/service/keymanager"
	"github.com/jimeng-relay/server/internal/testharness"
)

const (
	videoSubmitReqKey    = "jimeng_video_v30"
	videoGetResultReqKey = "jimeng_video_query_v30"
	videoProReqKey       = "jimeng_ti2v_v30_pro"
)

func TestSubmitVideoReqKeyPassthrough(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "submit route", path: "/v1/submit"},
		{name: "compatible action route", path: "/?Action=CVSync2AsyncSubmitTask&Version=2022-08-31"},
	}

	reqKeys := []string{videoSubmitReqKey, videoProReqKey}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, reqKey := range reqKeys {
				t.Run("req_key="+reqKey, func(t *testing.T) {
					upstreamBody := []byte(`{"code":10000,"message":"ok","data":{"task_id":"video_task_1"}}`)
					fake := &fakeSubmitClient{resp: &upstream.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: upstreamBody}}
					auditSvc, dsRepo, usRepo, aeRepo := newTestAuditService(t, nil, nil, nil)
					h := NewSubmitHandler(fake, auditSvc, &mockBillingService{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil))).Routes()

					requestBody := []byte(`{"prompt":"video test","req_key":"` + reqKey + `"}`)
					req := httptest.NewRequest(http.MethodPost, tt.path, bytes.NewReader(requestBody))
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("X-Request-Id", "video-submit-req-1")
					req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k-video"))
					rec := httptest.NewRecorder()

					h.ServeHTTP(rec, req)

					if rec.Code != http.StatusOK {
						t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
					}
					var payload map[string]any
					if err := json.Unmarshal(fake.reqBody, &payload); err != nil {
						t.Fatalf("json.Unmarshal upstream request body: %v", err)
					}
					if got, ok := payload["req_key"].(string); !ok || got != reqKey {
						t.Fatalf("expected req_key %q, got %#v", reqKey, payload["req_key"])
					}
					if got, ok := payload["prompt"].(string); !ok || got != "video test" {
						t.Fatalf("expected prompt passthrough, got %#v", payload["prompt"])
					}
					if got, ok := payload["frames"].(float64); !ok || int(got) != 121 {
						t.Fatalf("expected normalized frames=121, got %#v", payload["frames"])
					}
					if fake.calls != 1 {
						t.Fatalf("expected 1 upstream call, got %d", fake.calls)
					}
					if len(dsRepo.created) != 1 || len(usRepo.created) != 1 || len(aeRepo.created) != 1 {
						t.Fatalf("expected full audit chain writes, got downstream=%d upstream=%d events=%d", len(dsRepo.created), len(usRepo.created), len(aeRepo.created))
					}
					if dsRepo.created[0].Action != models.DownstreamActionCVSync2AsyncSubmitTask {
						t.Fatalf("expected submit action in audit, got %q", dsRepo.created[0].Action)
					}
					if got, ok := dsRepo.created[0].Body["req_key"].(string); !ok || got != reqKey {
						t.Fatalf("expected audited req_key %q, got %#v", reqKey, dsRepo.created[0].Body["req_key"])
					}
				})
			}
		})
	}
}

func TestSubmitVideoFramesParity(t *testing.T) {
	t.Run("rejects invalid frames with 400", func(t *testing.T) {
		fake := &fakeSubmitClient{resp: &upstream.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"code":10000}`)}}
		auditSvc, _, _, _ := newTestAuditService(t, nil, nil, nil)
		h := NewSubmitHandler(fake, auditSvc, &mockBillingService{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil))).Routes()

		requestBody := []byte(`{"prompt":"video test","req_key":"jimeng_t2v_v30","frames":120}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader(requestBody))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k-video"))
		rec := httptest.NewRecorder()

		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
		}
		if fake.calls != 0 {
			t.Fatalf("expected no upstream call, got %d", fake.calls)
		}
		var payload map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("json.Unmarshal response: %v", err)
		}
		errorObj, ok := payload["error"].(map[string]any)
		if !ok {
			t.Fatalf("expected error object, got %T", payload["error"])
		}
		if got, ok := errorObj["code"].(string); !ok || got != string(internalerrors.ErrValidationFailed) {
			t.Fatalf("expected error code %q, got %#v", internalerrors.ErrValidationFailed, errorObj["code"])
		}
	})

	t.Run("accepts explicit 241 frames", func(t *testing.T) {
		upstreamBody := []byte(`{"code":10000,"message":"ok","data":{"task_id":"video_task_241"}}`)
		fake := &fakeSubmitClient{resp: &upstream.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: upstreamBody}}
		auditSvc, _, _, _ := newTestAuditService(t, nil, nil, nil)
		h := NewSubmitHandler(fake, auditSvc, &mockBillingService{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil))).Routes()

		requestBody := []byte(`{"prompt":"video test","req_key":"jimeng_t2v_v30","frames":241}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader(requestBody))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k-video"))
		rec := httptest.NewRecorder()

		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
		}
		if fake.calls != 1 {
			t.Fatalf("expected 1 upstream call, got %d", fake.calls)
		}
		var payload map[string]any
		if err := json.Unmarshal(fake.reqBody, &payload); err != nil {
			t.Fatalf("json.Unmarshal upstream request body: %v", err)
		}
		if got, ok := payload["frames"].(float64); !ok || int(got) != 241 {
			t.Fatalf("expected frames=241, got %#v", payload["frames"])
		}
	})
}

func TestGetResultVideoReqKeyPassthrough(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "get-result route", path: "/v1/get-result"},
		{name: "compatible action route", path: "/?Action=CVSync2AsyncGetResult&Version=2022-08-31"},
	}

	reqKeys := []string{videoGetResultReqKey, videoProReqKey}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, reqKey := range reqKeys {
				t.Run("req_key="+reqKey, func(t *testing.T) {
					upstreamBody := []byte(`{"code":10000,"message":"ok","data":{"status":"running"}}`)
					fake := &fakeGetResultClient{resp: &upstream.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: upstreamBody}}
					auditSvc, dsRepo, usRepo, aeRepo := newTestAuditService(t, nil, nil, nil)
					h := NewGetResultHandler(fake, auditSvc, nil, slog.New(slog.NewTextHandler(io.Discard, nil))).Routes()

					requestBody := []byte(`{"task_id":"video_task_1","req_key":"` + reqKey + `"}`)
					req := httptest.NewRequest(http.MethodPost, tt.path, bytes.NewReader(requestBody))
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("X-Request-Id", "video-get-result-req-1")
					req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k-video"))
					rec := httptest.NewRecorder()

					h.ServeHTTP(rec, req)

					if rec.Code != http.StatusOK {
						t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
					}
					if !bytes.Equal(fake.reqBody, requestBody) {
						t.Fatalf("expected video get-result body passthrough to upstream")
					}
					if fake.calls != 1 {
						t.Fatalf("expected 1 upstream call, got %d", fake.calls)
					}
					if len(dsRepo.created) != 1 || len(usRepo.created) != 1 || len(aeRepo.created) != 1 {
						t.Fatalf("expected full audit chain writes, got downstream=%d upstream=%d events=%d", len(dsRepo.created), len(usRepo.created), len(aeRepo.created))
					}
					if dsRepo.created[0].Action != models.DownstreamActionCVSync2AsyncGetResult {
						t.Fatalf("expected get-result action in audit, got %q", dsRepo.created[0].Action)
					}
					if got, ok := dsRepo.created[0].Body["req_key"].(string); !ok || got != reqKey {
						t.Fatalf("expected audited req_key %q, got %#v", reqKey, dsRepo.created[0].Body["req_key"])
					}
				})
			}
		})
	}
}

func TestSubmitVideoRateLimitedByKey(t *testing.T) {
	fake := &fakeSubmitClient{err: internalerrors.New(internalerrors.ErrRateLimited, "rate limit exceeded", nil)}
	auditSvc, dsRepo, _, _ := newTestAuditService(t, nil, nil, nil)
	h := NewSubmitHandler(fake, auditSvc, &mockBillingService{}, nil, nil, nil).Routes()

	requestBody := []byte(`{"prompt":"video test","req_key":"` + videoSubmitReqKey + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", "video-submit-rate-limit")
	req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k-video"))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status 429, got %d body=%s", rec.Code, rec.Body.String())
	}
	if fake.calls != 1 {
		t.Fatalf("expected 1 upstream call, got %d", fake.calls)
	}
	if len(dsRepo.created) != 1 {
		t.Fatalf("expected downstream audit record to be written before upstream error, got %d", len(dsRepo.created))
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal response: %v", err)
	}
	errorObj, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error object, got %T", payload["error"])
	}
	if got, ok := errorObj["code"].(string); !ok || got != string(internalerrors.ErrRateLimited) {
		t.Fatalf("expected error code %q, got %#v", internalerrors.ErrRateLimited, errorObj["code"])
	}
}

func TestRateLimitVideo_SameAPIKeyConcurrentSubmitReturns429(t *testing.T) {
	routes := []struct {
		name string
		path string
	}{
		{name: "submit route", path: "/v1/submit"},
		{name: "compatible action route", path: "/?Action=CVSync2AsyncSubmitTask&Version=2022-08-31"},
	}

	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			startedUpstream := make(chan struct{})
			releaseUpstream := make(chan struct{})
			var releaseOnce sync.Once
			releaseGate := func() { releaseOnce.Do(func() { close(releaseUpstream) }) }
			t.Cleanup(releaseGate)

			var upstreamCalls int32
			var startedOnce sync.Once
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&upstreamCalls, 1)
				startedOnce.Do(func() { close(startedUpstream) })
				<-releaseUpstream
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				if _, err := w.Write([]byte(`{"code":10000,"message":"ok","data":{"task_id":"video_task_1"}}`)); err != nil {
					return
				}
			}))
			defer srv.Close()

			c, err := upstream.NewClient(config.Config{
				Credentials: config.Credentials{AccessKey: "ak", SecretKey: "sk"},
				Region:      "cn-north-1",
				Host:        srv.URL,
				Timeout:     2 * time.Second,
			}, upstream.Options{
				MaxConcurrent: 2,
				MaxQueue:      10,
				KeyManager:    keymanager.NewService(nil),
			})
			if err != nil {
				t.Fatalf("upstream.NewClient: %v", err)
			}

			auditSvc := newConcurrentTestAuditService(t)
			h := NewSubmitHandler(c, auditSvc, &mockBillingService{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil))).Routes()

			body := []byte(`{"prompt":"video test","req_key":"` + videoSubmitReqKey + `"}`)

			firstDone := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				req := httptest.NewRequest(http.MethodPost, rt.path, bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Request-Id", "video-rate-limit-1")
				req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k-video"))
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				firstDone <- rec
			}()

			select {
			case <-startedUpstream:
			case <-time.After(2 * time.Second):
				t.Fatalf("first submit did not reach upstream in time")
			}

			secondDone := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				req := httptest.NewRequest(http.MethodPost, rt.path, bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Request-Id", "video-rate-limit-2")
				req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k-video"))
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				secondDone <- rec
			}()

			select {
			case rec := <-secondDone:
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
				if got, ok := errorObj["code"].(string); !ok || got != string(internalerrors.ErrRateLimited) {
					t.Fatalf("expected error code %q, got %#v", internalerrors.ErrRateLimited, errorObj["code"])
				}
			case <-time.After(200 * time.Millisecond):
				t.Fatalf("same-key video rejection should be immediate, but second request did not finish in time")
			}

			if got := atomic.LoadInt32(&upstreamCalls); got != 1 {
				t.Fatalf("expected only 1 upstream call, got %d", got)
			}

			releaseGate()
			select {
			case rec := <-firstDone:
				if rec.Code != http.StatusOK {
					t.Fatalf("expected first status 200, got %d body=%s", rec.Code, rec.Body.String())
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("first request did not complete after releasing upstream")
			}
		})
	}
}

func TestRateLimitVideo_GlobalQueueFullSubmitReturns429(t *testing.T) {
	routes := []struct {
		name string
		path string
	}{
		{name: "submit route", path: "/v1/submit"},
		{name: "compatible action route", path: "/?Action=CVSync2AsyncSubmitTask&Version=2022-08-31"},
	}

	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			startedUpstream := make(chan struct{})
			releaseUpstream := make(chan struct{})
			var releaseOnce sync.Once
			releaseGate := func() { releaseOnce.Do(func() { close(releaseUpstream) }) }
			t.Cleanup(releaseGate)

			var upstreamCalls int32
			var startedOnce sync.Once
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := atomic.AddInt32(&upstreamCalls, 1)
				startedOnce.Do(func() { close(startedUpstream) })
				if n == 1 {
					<-releaseUpstream
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				if _, err := w.Write([]byte(`{"code":10000,"message":"ok","data":{"task_id":"video_task_1"}}`)); err != nil {
					return
				}
			}))
			defer srv.Close()

			c, err := upstream.NewClient(config.Config{
				Credentials: config.Credentials{AccessKey: "ak", SecretKey: "sk"},
				Region:      "cn-north-1",
				Host:        srv.URL,
				Timeout:     2 * time.Second,
			}, upstream.Options{
				MaxConcurrent: 1,
				MaxQueue:      1,
				KeyManager:    keymanager.NewService(nil),
			})
			if err != nil {
				t.Fatalf("upstream.NewClient: %v", err)
			}

			auditSvc := newConcurrentTestAuditService(t)
			h := NewSubmitHandler(c, auditSvc, &mockBillingService{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil))).Routes()

			body := []byte(`{"prompt":"video test","req_key":"` + videoSubmitReqKey + `"}`)

			firstDone := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				req := httptest.NewRequest(http.MethodPost, rt.path, bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Request-Id", "video-queue-full-1")
				req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k-video-a"))
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				firstDone <- rec
			}()

			select {
			case <-startedUpstream:
			case <-time.After(2 * time.Second):
				t.Fatalf("first submit did not reach upstream in time")
			}

			secondDone := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				req := httptest.NewRequest(http.MethodPost, rt.path, bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Request-Id", "video-queue-full-2")
				req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k-video-b"))
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				secondDone <- rec
			}()

			waitForUpstreamWaitersLen(t, c, 1)

			thirdDone := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				req := httptest.NewRequest(http.MethodPost, rt.path, bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Request-Id", "video-queue-full-3")
				req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k-video-c"))
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				thirdDone <- rec
			}()

			select {
			case rec := <-thirdDone:
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
				if got, ok := errorObj["code"].(string); !ok || got != string(internalerrors.ErrRateLimited) {
					t.Fatalf("expected error code %q, got %#v", internalerrors.ErrRateLimited, errorObj["code"])
				}
			case <-time.After(200 * time.Millisecond):
				t.Fatalf("queue-full video rejection should be immediate, but third request did not finish in time")
			}

			if got := atomic.LoadInt32(&upstreamCalls); got != 1 {
				t.Fatalf("expected only 1 upstream call while first is in-flight, got %d", got)
			}

			releaseGate()
			select {
			case rec := <-firstDone:
				if rec.Code != http.StatusOK {
					t.Fatalf("expected first status 200, got %d body=%s", rec.Code, rec.Body.String())
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("first request did not complete after releasing upstream")
			}
			select {
			case rec := <-secondDone:
				if rec.Code != http.StatusOK {
					t.Fatalf("expected second status 200 after dequeuing, got %d body=%s", rec.Code, rec.Body.String())
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("second queued request did not complete after releasing upstream")
			}

			if got := atomic.LoadInt32(&upstreamCalls); got != 2 {
				t.Fatalf("expected 2 upstream calls total (first + dequeued second), got %d", got)
			}
		})
	}
}

func TestConcurrencyPolicy_SameKeyImmediate429(t *testing.T) {
	gate := testharness.NewBlockingGate()
	mockUpstream := testharness.NewMockUpstreamServer([]testharness.MockHTTPResponse{{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       []byte(`{"code":10000,"message":"ok","data":{"task_id":"video_task_1"}}`),
	}}, gate)
	defer mockUpstream.Close()

	c, err := upstream.NewClient(config.Config{
		Credentials: config.Credentials{AccessKey: "ak", SecretKey: "sk"},
		Region:      "cn-north-1",
		Host:        mockUpstream.URL(),
		Timeout:     2 * time.Second,
	}, upstream.Options{
		MaxConcurrent: 2,
		MaxQueue:      10,
		KeyManager:    keymanager.NewService(nil),
	})
	if err != nil {
		t.Fatalf("upstream.NewClient: %v", err)
	}

	h := NewSubmitHandler(c, newConcurrentTestAuditService(t), &mockBillingService{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil))).Routes()
	body := []byte(`{"prompt":"video test","req_key":"` + videoSubmitReqKey + `"}`)

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Request-Id", "policy-same-key-1")
		req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k-policy"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		firstDone <- rec
	}()

	select {
	case <-mockUpstream.Started():
	case <-time.After(2 * time.Second):
		t.Fatalf("first request did not reach upstream in time")
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader(body))
	secondReq.Header.Set("Content-Type", "application/json")
	secondReq.Header.Set("X-Request-Id", "policy-same-key-2")
	secondReq = secondReq.WithContext(context.WithValue(secondReq.Context(), sigv4.ContextAPIKeyID, "k-policy"))
	secondRec := httptest.NewRecorder()

	start := time.Now()
	h.ServeHTTP(secondRec, secondReq)
	elapsed := time.Since(start)

	if secondRec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status 429, got %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	if elapsed >= 100*time.Millisecond {
		t.Fatalf("expected immediate same-key rejection (<100ms), got %s", elapsed)
	}
	if got := mockUpstream.CallCount(); got != 1 {
		t.Fatalf("expected only 1 upstream call for same-key concurrency, got %d", got)
	}

	gate.Release()
	select {
	case rec := <-firstDone:
		if rec.Code != http.StatusOK {
			t.Fatalf("expected first status 200, got %d body=%s", rec.Code, rec.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("first request did not complete after releasing upstream")
	}
}

func TestConcurrencyPolicy_DifferentKeyFIFO(t *testing.T) {
	gate := testharness.NewBlockingGate()
	mockUpstream := testharness.NewMockUpstreamServer([]testharness.MockHTTPResponse{
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       []byte(`{"code":10000,"message":"ok","data":{"task_id":"video_task_1"}}`),
		},
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       []byte(`{"code":10000,"message":"ok","data":{"task_id":"video_task_2"}}`),
		},
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       []byte(`{"code":10000,"message":"ok","data":{"task_id":"video_task_3"}}`),
		},
	}, gate)
	defer mockUpstream.Close()

	c, err := upstream.NewClient(config.Config{
		Credentials: config.Credentials{AccessKey: "ak", SecretKey: "sk"},
		Region:      "cn-north-1",
		Host:        mockUpstream.URL(),
		Timeout:     2 * time.Second,
	}, upstream.Options{
		MaxConcurrent: 1,
		MaxQueue:      2,
		KeyManager:    keymanager.NewService(nil),
	})
	if err != nil {
		t.Fatalf("upstream.NewClient: %v", err)
	}

	h := NewSubmitHandler(c, newConcurrentTestAuditService(t), &mockBillingService{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil))).Routes()
	body := []byte(`{"prompt":"video test","req_key":"` + videoSubmitReqKey + `"}`)

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go serveVideoSubmitRequest(h, body, "k-fifo-a", "policy-fifo-1", "/v1/submit", firstDone)

	select {
	case <-mockUpstream.Started():
	case <-time.After(2 * time.Second):
		t.Fatalf("first request did not reach upstream in time")
	}

	secondDone := make(chan *httptest.ResponseRecorder, 1)
	go serveVideoSubmitRequest(h, body, "k-fifo-b", "policy-fifo-2", "/v1/submit", secondDone)
	waitForUpstreamWaitersLen(t, c, 1)

	thirdDone := make(chan *httptest.ResponseRecorder, 1)
	go serveVideoSubmitRequest(h, body, "k-fifo-c", "policy-fifo-3", "/v1/submit", thirdDone)
	waitForUpstreamWaitersLen(t, c, 2)

	gate.Release()

	select {
	case rec := <-firstDone:
		if rec.Code != http.StatusOK {
			t.Fatalf("expected first status 200, got %d body=%s", rec.Code, rec.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("first request did not complete after releasing upstream")
	}

	var secondTaskID string
	select {
	case rec := <-secondDone:
		if rec.Code != http.StatusOK {
			t.Fatalf("expected second status 200, got %d body=%s", rec.Code, rec.Body.String())
		}
		secondTaskID = mustReadTaskID(t, rec)
	case <-time.After(2 * time.Second):
		t.Fatalf("second request did not complete after releasing upstream")
	}

	var thirdTaskID string
	select {
	case rec := <-thirdDone:
		if rec.Code != http.StatusOK {
			t.Fatalf("expected third status 200, got %d body=%s", rec.Code, rec.Body.String())
		}
		thirdTaskID = mustReadTaskID(t, rec)
	case <-time.After(2 * time.Second):
		t.Fatalf("third request did not complete after releasing upstream")
	}

	testharness.AssertFIFOOrder(t, []testharness.Response{{ID: secondTaskID}, {ID: thirdTaskID}}, []string{"video_task_2", "video_task_3"})
	testharness.AssertMaxInFlight(t, c, 1)

	if got := mockUpstream.CallCount(); got != 3 {
		t.Fatalf("expected 3 upstream calls total in fifo flow, got %d", got)
	}
}

func TestVideoValidationErrors(t *testing.T) {
	t.Run("submit validation failed -> 400 with validation code", func(t *testing.T) {
		fake := &fakeSubmitClient{err: internalerrors.New(internalerrors.ErrValidationFailed, "req_key mismatch: expected jimeng_video_v30", nil)}
		auditSvc, _, _, _ := newTestAuditService(t, nil, nil, nil)
		h := NewSubmitHandler(fake, auditSvc, &mockBillingService{}, nil, nil, nil).Routes()

		body := []byte(`{"prompt":"video test","req_key":"jimeng_video_query_v30"}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/submit", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k-video"))
		rec := httptest.NewRecorder()

		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
		}

		var payload map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("json.Unmarshal response: %v", err)
		}
		errorObj, ok := payload["error"].(map[string]any)
		if !ok {
			t.Fatalf("expected error object, got %T", payload["error"])
		}
		if got, ok := errorObj["code"].(string); !ok || got != string(internalerrors.ErrValidationFailed) {
			t.Fatalf("expected error code %q, got %#v", internalerrors.ErrValidationFailed, errorObj["code"])
		}
		if got, ok := errorObj["message"].(string); !ok || got == "" {
			t.Fatalf("expected non-empty error message, got %#v", errorObj["message"])
		}
	})

	t.Run("get-result validation failed -> 400 with validation code", func(t *testing.T) {
		fake := &fakeGetResultClient{err: internalerrors.New(internalerrors.ErrValidationFailed, "invalid i2v combination: i2v-first must not include frames", nil)}
		auditSvc, _, _, _ := newTestAuditService(t, nil, nil, nil)
		h := NewGetResultHandler(fake, auditSvc, nil, nil).Routes()

		body := []byte(`{"task_id":"video-task-1","req_key":"jimeng_video_query_v30"}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/get-result", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, "k-video"))
		rec := httptest.NewRecorder()

		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
		}

		var payload map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("json.Unmarshal response: %v", err)
		}
		errorObj, ok := payload["error"].(map[string]any)
		if !ok {
			t.Fatalf("expected error object, got %T", payload["error"])
		}
		if got, ok := errorObj["code"].(string); !ok || got != string(internalerrors.ErrValidationFailed) {
			t.Fatalf("expected error code %q, got %#v", internalerrors.ErrValidationFailed, errorObj["code"])
		}
	})
}

func upstreamWaitersLen(c *upstream.Client) int {
	if c == nil {
		return 0
	}
	v := reflect.ValueOf(c).Elem().FieldByName("waiters")
	if !v.IsValid() {
		return 0
	}
	return v.Len()
}

func waitForUpstreamWaitersLen(t *testing.T, c *upstream.Client, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := upstreamWaitersLen(c); got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("waiters length did not reach %d, got %d", want, upstreamWaitersLen(c))
}

func serveVideoSubmitRequest(h http.Handler, body []byte, apiKeyID string, requestID string, path string, done chan<- *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", requestID)
	req = req.WithContext(context.WithValue(req.Context(), sigv4.ContextAPIKeyID, apiKeyID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	done <- rec
}

func mustReadTaskID(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var payload struct {
		Data struct {
			TaskID string `json:"task_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal response: %v", err)
	}
	if payload.Data.TaskID == "" {
		t.Fatalf("expected non-empty task_id in response body=%s", rec.Body.String())
	}
	return payload.Data.TaskID
}

type lockedReader struct {
	mu sync.Mutex
	r  io.Reader
}

func (r *lockedReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.r.Read(p)
}

type concurrentDownstreamRepo struct {
}

func (r *concurrentDownstreamRepo) Create(_ context.Context, _ models.DownstreamRequest) error {
	return nil
}

func (r *concurrentDownstreamRepo) GetByID(_ context.Context, _ string) (models.DownstreamRequest, error) {
	return models.DownstreamRequest{}, errors.New("not implemented")
}

func (r *concurrentDownstreamRepo) GetByRequestID(_ context.Context, _ string) (models.DownstreamRequest, error) {
	return models.DownstreamRequest{}, errors.New("not implemented")
}

type concurrentUpstreamRepo struct {
}

func (r *concurrentUpstreamRepo) Create(_ context.Context, _ models.UpstreamAttempt) error {
	return nil
}

func (r *concurrentUpstreamRepo) ListByRequestID(_ context.Context, _ string) ([]models.UpstreamAttempt, error) {
	return nil, errors.New("not implemented")
}

type concurrentAuditRepo struct {
}

func (r *concurrentAuditRepo) Create(_ context.Context, _ models.AuditEvent) error {
	return nil
}

func (r *concurrentAuditRepo) ListByRequestID(_ context.Context, _ string) ([]models.AuditEvent, error) {
	return nil, errors.New("not implemented")
}

func (r *concurrentAuditRepo) ListByTimeRange(_ context.Context, _, _ time.Time) ([]models.AuditEvent, error) {
	return nil, errors.New("not implemented")
}

func newConcurrentTestAuditService(t *testing.T) *auditservice.Service {
	t.Helper()
	base := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	ds := &concurrentDownstreamRepo{}
	us := &concurrentUpstreamRepo{}
	ae := &concurrentAuditRepo{}
	rnd := &lockedReader{r: bytes.NewReader(bytes.Repeat([]byte{0x01}, 4096))}
	return auditservice.NewService(ds, us, ae, auditservice.Config{Now: func() time.Time { return base }, Random: rnd})
}
