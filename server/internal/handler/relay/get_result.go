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
	auditservice "github.com/jimeng-relay/server/internal/service/audit"
	billingservice "github.com/jimeng-relay/server/internal/service/billing"
)

const getResultAction = "CVSync2AsyncGetResult"

type getResultClient interface {
	GetResult(ctx context.Context, body []byte, headers http.Header) (*upstream.Response, error)
}

type getResultBillingService interface {
	Settle(ctx context.Context, requestID, apiKeyID string) error
	Release(ctx context.Context, requestID, apiKeyID string) error
}

type GetResultHandler struct {
	client  getResultClient
	audit   *auditservice.Service
	billing getResultBillingService
	logger  *slog.Logger
}

func NewGetResultHandler(client getResultClient, auditSvc *auditservice.Service, billingSvc getResultBillingService, logger *slog.Logger) *GetResultHandler {
	if logger == nil {
		logger = slog.Default()
	}
	if billingSvc == nil {
		billingSvc = billingservice.NewService(billingservice.Config{})
	}
	return &GetResultHandler{client: client, audit: auditSvc, billing: billingSvc, logger: logger}
}

func (h *GetResultHandler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/get-result", h.handleGetResult)
	mux.HandleFunc("/", h.handleCompatibleGetResult)
	return mux
}

func (h *GetResultHandler) handleCompatibleGetResult(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" || r.Method != http.MethodPost || r.URL.Query().Get("Action") != getResultAction {
		http.NotFound(w, r)
		return
	}
	h.proxyGetResult(w, r)
}

func (h *GetResultHandler) handleGetResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeRelayError(w, internalerrors.New(internalerrors.ErrValidationFailed, "method not allowed", nil), http.StatusMethodNotAllowed)
		return
	}
	h.proxyGetResult(w, r)
}

func (h *GetResultHandler) proxyGetResult(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var upstreamStatus int
	var finalErr error

	reqID := requestIDFromRequest(r)
	ctx := context.WithValue(r.Context(), logging.RequestIDKey, reqID)

	defer func() {
		logResponse(ctx, h.logger, start, upstreamStatus, finalErr)
	}()

	if h.client == nil {
		finalErr = internalerrors.New(internalerrors.ErrInternalError, "get-result upstream client is not configured", nil)
		writeRelayError(w, finalErr, http.StatusInternalServerError)
		return
	}
	if h.audit == nil {
		finalErr = internalerrors.New(internalerrors.ErrInternalError, "audit service is not configured", nil)
		writeRelayError(w, finalErr, http.StatusInternalServerError)
		return
	}
	if h.billing == nil {
		finalErr = internalerrors.New(internalerrors.ErrInternalError, "billing service is not configured", nil)
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

	headers := pickForwardHeaders(r.Header)
	call := auditservice.RelayCall{
		RequestID:         reqID,
		APIKeyID:          apiKeyID,
		Action:            models.DownstreamActionCVSync2AsyncGetResult,
		Method:            r.Method,
		Path:              r.URL.Path,
		Query:             r.URL.RawQuery,
		ClientIP:          strings.TrimSpace(r.RemoteAddr),
		DownstreamHeaders: headerToMapAny(r.Header),
		DownstreamBody:    decodeJSONMap(body),
		Upstream: auditservice.UpstreamAttempt{
			AttemptNumber:  1,
			UpstreamAction: getResultAction,
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
	resp, callErr := h.client.GetResult(ctx, body, headers)
	if resp != nil {
		upstreamStatus = resp.StatusCode
		latencyMs := time.Since(start).Milliseconds()
		var upstreamErr *string
		if callErr != nil {
			s := callErr.Error()
			upstreamErr = &s
			h.logger.WarnContext(ctx, "get-result upstream returned error status", "error", callErr.Error(), "status", resp.StatusCode)
		}
		call.Upstream.ResponseStatus = resp.StatusCode
		call.Upstream.ResponseHeaders = headerToMapAny(resp.Header)
		call.Upstream.ResponseBody = nil
		call.Upstream.LatencyMs = latencyMs
		call.Upstream.Error = upstreamErr

		taskStatus := parseGetResultTaskStatus(resp.Body)
		call.Billing = auditservice.BillingTrace{ResultState: plannedBillingResultState(taskStatus, callErr)}
		if call.Billing.ResultState == "settled" || call.Billing.ResultState == "released" {
			call.Billing.SettlementID = reqID
		}
		if err := h.audit.RecordRelayUpstreamAndEvents(ctx, call); err != nil {
			finalErr = err
			writeRelayError(w, finalErr, http.StatusInternalServerError)
			return
		}
		if err := h.handleResultBilling(ctx, reqID, apiKeyID, taskStatus, callErr); err != nil {
			finalErr = err
			writeRelayError(w, finalErr, http.StatusInternalServerError)
			return
		}
		writeRelayPassthrough(w, resp)
		return
	}
	if callErr != nil {
		if err := h.billing.Release(ctx, reqID, apiKeyID); err != nil {
			finalErr = err
			writeRelayError(w, finalErr, http.StatusInternalServerError)
			return
		}
		code := internalerrors.GetCode(callErr)
		if code != "" && code != internalerrors.ErrUnknown && code != internalerrors.ErrUpstreamFailed {
			finalErr = callErr
			writeRelayError(w, finalErr, 0)
			return
		}
		finalErr = internalerrors.New(internalerrors.ErrUpstreamFailed, "get-result upstream request failed", callErr)
		writeRelayError(w, finalErr, http.StatusBadGateway)
		return
	}

	if err := h.billing.Release(ctx, reqID, apiKeyID); err != nil {
		finalErr = err
		writeRelayError(w, finalErr, http.StatusInternalServerError)
		return
	}
	finalErr = internalerrors.New(internalerrors.ErrUpstreamFailed, "get-result upstream returned empty response", nil)
	writeRelayError(w, finalErr, http.StatusBadGateway)
}

func (h *GetResultHandler) handleResultBilling(ctx context.Context, requestID, apiKeyID, taskStatus string, callErr error) error {
	if callErr != nil {
		return h.billing.Release(ctx, requestID, apiKeyID)
	}
	switch {
	case strings.EqualFold(taskStatus, "Done"):
		return h.billing.Settle(ctx, requestID, apiKeyID)
	case strings.EqualFold(taskStatus, "Failed"):
		return h.billing.Release(ctx, requestID, apiKeyID)
	default:
		return nil
	}
}

func plannedBillingResultState(taskStatus string, callErr error) string {
	if callErr != nil {
		return "released"
	}
	switch {
	case strings.EqualFold(taskStatus, "Done"):
		return "settled"
	case strings.EqualFold(taskStatus, "Failed"):
		return "released"
	default:
		return "pending"
	}
}

func parseGetResultTaskStatus(body []byte) string {
	payload := decodeJSONMap(body)
	if payload == nil {
		return ""
	}
	if v := taskStatusFromMap(payload); v != "" {
		return v
	}
	data, ok := payload["data"].(map[string]any)
	if !ok {
		return ""
	}
	return taskStatusFromMap(data)
}

func taskStatusFromMap(m map[string]any) string {
	v, ok := m["task_status"].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(v)
}
