package relay

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	internalerrors "github.com/jimeng-relay/server/internal/errors"
	"github.com/jimeng-relay/server/internal/logging"
	"github.com/jimeng-relay/server/internal/middleware/sigv4"
	"github.com/jimeng-relay/server/internal/models"
	"github.com/jimeng-relay/server/internal/relay/upstream"
	"github.com/jimeng-relay/server/internal/repository"
	auditservice "github.com/jimeng-relay/server/internal/service/audit"
	billingservice "github.com/jimeng-relay/server/internal/service/billing"
	idempotencyservice "github.com/jimeng-relay/server/internal/service/idempotency"
)

const submitAction = "CVSync2AsyncSubmitTask"

type submitClient interface {
	Submit(ctx context.Context, body []byte, headers http.Header) (*upstream.Response, error)
}

type billingService interface {
	PreAuthorize(ctx context.Context, requestID, apiKeyID string, requestBody []byte) (billingservice.PreAuthResult, error)
}

type SubmitHandler struct {
	client      submitClient
	audit       *auditservice.Service
	billing     billingService
	idempotency *idempotencyservice.Service
	idemRepo    repository.IdempotencyRecordRepository
	logger      *slog.Logger
}

func NewSubmitHandler(client submitClient, auditSvc *auditservice.Service, billingSvc billingService, idempotencySvc *idempotencyservice.Service, idemRepo repository.IdempotencyRecordRepository, logger *slog.Logger) *SubmitHandler {
	if logger == nil {
		logger = slog.Default()
	}
	if billingSvc == nil {
		billingSvc = billingservice.NewService(billingservice.Config{})
	}
	return &SubmitHandler{
		client:      client,
		audit:       auditSvc,
		billing:     billingSvc,
		idempotency: idempotencySvc,
		idemRepo:    idemRepo,
		logger:      logger,
	}
}

func (h *SubmitHandler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/submit", h.handleSubmit)
	mux.HandleFunc("/", h.handleCompatibleSubmit)
	return mux
}

func (h *SubmitHandler) handleCompatibleSubmit(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" || r.Method != http.MethodPost || r.URL.Query().Get("Action") != submitAction {
		http.NotFound(w, r)
		return
	}
	h.proxySubmit(w, r, false)
}

func (h *SubmitHandler) handleSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeRelayError(w, internalerrors.New(internalerrors.ErrValidationFailed, "method not allowed", nil), http.StatusMethodNotAllowed)
		return
	}
	h.proxySubmit(w, r, true)
}

func (h *SubmitHandler) proxySubmit(w http.ResponseWriter, r *http.Request, applyIdempotency bool) {
	start := time.Now()
	var upstreamStatus int
	var finalErr error

	reqID := requestIDFromRequest(r)
	ctx := context.WithValue(r.Context(), logging.RequestIDKey, reqID)

	defer func() {
		logResponse(ctx, h.logger, start, upstreamStatus, finalErr)
	}()

	if h.client == nil {
		finalErr = internalerrors.New(internalerrors.ErrInternalError, "submit upstream client is not configured", nil)
		writeRelayError(w, finalErr, http.StatusInternalServerError)
		return
	}
	if h.audit == nil {
		finalErr = internalerrors.New(internalerrors.ErrInternalError, "audit service is not configured", nil)
		writeRelayError(w, finalErr, http.StatusInternalServerError)
		return
	}

	val := r.Context().Value(sigv4.ContextAPIKeyID)
	apiKeyID, ok := val.(string)
	if val != nil && !ok {
		finalErr = internalerrors.New(internalerrors.ErrInternalError, "invalid api_key_id type in context", nil)
		writeRelayError(w, finalErr, http.StatusInternalServerError)
		return
	}
	apiKeyID = strings.TrimSpace(apiKeyID)
	if apiKeyID == "" {
		finalErr = internalerrors.New(internalerrors.ErrAuthFailed, "missing api_key_id in context", nil)
		writeRelayError(w, finalErr, http.StatusUnauthorized)
		return
	}

	body, err := readRequestBodyLimited(r)
	if err != nil {
		finalErr = internalerrors.New(internalerrors.ErrValidationFailed, "read downstream request body", err)
		writeRelayError(w, finalErr, http.StatusRequestEntityTooLarge)
		return
	}
	body, err = normalizeSubmitVideoFrames(body)
	if err != nil {
		finalErr = internalerrors.New(internalerrors.ErrValidationFailed, err.Error(), nil)
		writeRelayError(w, finalErr, http.StatusBadRequest)
		return
	}

	idempotencyKey := ""
	requestHash := ""
	if applyIdempotency {
		idempotencyKey = strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if idempotencyKey != "" {
			if h.idempotency == nil || h.idemRepo == nil {
				finalErr = internalerrors.New(internalerrors.ErrInternalError, "idempotency service is not configured", nil)
				writeRelayError(w, finalErr, http.StatusInternalServerError)
				return
			}
			requestHash = hashRequestBody(body)
			rec, err := h.idemRepo.GetByKey(ctx, idempotencyKey)
			if err == nil {
				if !rec.ExpiresAt.After(time.Now().UTC()) {
					finalErr = internalerrors.New(internalerrors.ErrValidationFailed, "idempotency key has expired", nil)
					writeRelayError(w, finalErr, http.StatusBadRequest)
					return
				}
				if rec.RequestHash != requestHash {
					finalErr = internalerrors.New(internalerrors.ErrValidationFailed, "idempotency key request hash mismatch", nil)
					writeRelayError(w, finalErr, http.StatusBadRequest)
					return
				}
				writeReplayResponse(w, rec.ResponseStatus, rec.ResponseBody)
				return
			}
			if !repository.IsNotFound(err) {
				finalErr = internalerrors.New(internalerrors.ErrDatabaseError, "get idempotency record", err)
				writeRelayError(w, finalErr, http.StatusInternalServerError)
				return
			}
		}
	}

	if h.billing == nil {
		finalErr = internalerrors.New(internalerrors.ErrInternalError, "billing service is not configured", nil)
		writeRelayError(w, finalErr, http.StatusInternalServerError)
		return
	}
	preAuth, err := h.billing.PreAuthorize(ctx, reqID, apiKeyID, body)
	if err != nil {
		if billingservice.IsInsufficientBudget(err) {
			finalErr = err
			writeRelayError(w, finalErr, http.StatusPaymentRequired)
			return
		}
		finalErr = err
		writeRelayError(w, finalErr, 0)
		return
	}
	ctx = billingservice.WithPreAuthResult(ctx, preAuth)

	headers := pickForwardHeaders(r.Header)
	call := auditservice.RelayCall{
		RequestID:         reqID,
		APIKeyID:          apiKeyID,
		Action:            models.DownstreamActionCVSync2AsyncSubmitTask,
		Method:            r.Method,
		Path:              r.URL.Path,
		Query:             r.URL.RawQuery,
		ClientIP:          strings.TrimSpace(r.RemoteAddr),
		DownstreamHeaders: headerToMapAny(r.Header),
		DownstreamBody:    decodeJSONMap(body),
		Billing: auditservice.BillingTrace{
			ComputedCredit: preAuth.EstimatedCost.String(),
			PreAuthID:      preAuth.LedgerID,
			ResultState:    "preauth",
		},
		Upstream: auditservice.UpstreamAttempt{
			AttemptNumber:  1,
			UpstreamAction: submitAction,
			RequestHeaders: headerToMapAny(headers),
			RequestBody:    nil,
		},
	}
	if err := h.audit.RecordRelayDownstream(ctx, call); err != nil {
		finalErr = err
		writeRelayError(w, finalErr, http.StatusInternalServerError)
		return
	}

	ctx = upstream.WithAPIKeyID(ctx, apiKeyID)
	resp, callErr := h.client.Submit(ctx, body, headers)
	if resp != nil {
		upstreamStatus = resp.StatusCode
		latencyMs := time.Since(start).Milliseconds()
		var upstreamErr *string
		if callErr != nil {
			s := callErr.Error()
			upstreamErr = &s
			h.logger.WarnContext(ctx, "submit upstream returned error status", "error", callErr.Error(), "status", resp.StatusCode)
		}
		call.Upstream.ResponseStatus = resp.StatusCode
		call.Upstream.ResponseHeaders = headerToMapAny(resp.Header)
		call.Upstream.ResponseBody = nil
		call.Upstream.LatencyMs = latencyMs
		call.Upstream.Error = upstreamErr
		if err := h.audit.RecordRelayUpstreamAndEvents(ctx, call); err != nil {
			finalErr = err
			writeRelayError(w, finalErr, http.StatusInternalServerError)
			return
		}
		if idempotencyKey != "" {
			_, err := h.idempotency.ResolveOrStore(ctx, idempotencyservice.ResolveRequest{
				IdempotencyKey: idempotencyKey,
				RequestHash:    requestHash,
				ResponseStatus: resp.StatusCode,
				ResponseBody: map[string]any{
					"content_type": strings.TrimSpace(resp.Header.Get("Content-Type")),
					"body":         string(resp.Body),
				},
			})
			if err != nil {
				finalErr = err
				writeRelayError(w, finalErr, http.StatusInternalServerError)
				return
			}
		}
		writeRelayPassthrough(w, resp)
		return
	}
	if callErr != nil {
		code := internalerrors.GetCode(callErr)
		if code != "" && code != internalerrors.ErrUnknown && code != internalerrors.ErrUpstreamFailed {
			finalErr = callErr
			writeRelayError(w, finalErr, 0)
			return
		}
		finalErr = internalerrors.New(internalerrors.ErrUpstreamFailed, "submit upstream request failed", callErr)
		writeRelayError(w, finalErr, http.StatusBadGateway)
		return
	}

	finalErr = internalerrors.New(internalerrors.ErrUpstreamFailed, "submit upstream returned empty response", nil)
	writeRelayError(w, finalErr, http.StatusBadGateway)
}

func writeReplayResponse(w http.ResponseWriter, statusCode int, responseBody any) {
	if contentType, ok := replayContentType(responseBody); ok {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(statusCode)
	if _, err := w.Write(replayBody(responseBody)); err != nil {
		return
	}
}

func replayContentType(body any) (string, bool) {
	b, ok := body.(map[string]any)
	if !ok {
		return "", false
	}
	contentType, ok := b["content_type"].(string)
	contentType = strings.TrimSpace(contentType)
	if !ok || contentType == "" {
		return "", false
	}
	return contentType, true
}

func replayBody(body any) []byte {
	b, ok := body.(map[string]any)
	if !ok {
		return nil
	}
	payload, ok := b["body"].(string)
	if !ok {
		return nil
	}
	return []byte(payload)
}
