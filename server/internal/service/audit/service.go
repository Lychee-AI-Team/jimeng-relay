package audit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"strings"
	"time"

	internalerrors "github.com/jimeng-relay/server/internal/errors"
	"github.com/jimeng-relay/server/internal/models"
	"github.com/jimeng-relay/server/internal/repository"
)

type Config struct {
	Now    func() time.Time
	Random io.Reader
}

type Service struct {
	downstreamRepo repository.DownstreamRequestRepository
	upstreamRepo   repository.UpstreamAttemptRepository
	auditRepo      repository.AuditEventRepository

	now    func() time.Time
	random io.Reader
}

func NewService(
	downstreamRepo repository.DownstreamRequestRepository,
	upstreamRepo repository.UpstreamAttemptRepository,
	auditRepo repository.AuditEventRepository,
	cfg Config,
) *Service {
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}
	rnd := cfg.Random
	if rnd == nil {
		rnd = rand.Reader
	}
	return &Service{downstreamRepo: downstreamRepo, upstreamRepo: upstreamRepo, auditRepo: auditRepo, now: nowFn, random: rnd}
}

type RelayCall struct {
	RequestID string
	APIKeyID  string
	Action    models.DownstreamAction

	Method string
	Path   string
	Query  string

	DownstreamHeaders map[string]any
	DownstreamBody    map[string]any
	ClientIP          string

	Billing BillingTrace

	Upstream UpstreamAttempt
	Events   []Event
}

type BillingTrace struct {
	ComputedCredit string
	PreAuthID      string
	SettlementID   string
	ResultState    string
}

func (t BillingTrace) metadata() map[string]any {
	out := map[string]any{}
	if strings.TrimSpace(t.ComputedCredit) != "" {
		out[models.AuditMetaComputedCredit] = strings.TrimSpace(t.ComputedCredit)
	}
	if strings.TrimSpace(t.PreAuthID) != "" {
		out[models.AuditMetaPreAuthID] = strings.TrimSpace(t.PreAuthID)
	}
	if strings.TrimSpace(t.SettlementID) != "" {
		out[models.AuditMetaSettlementID] = strings.TrimSpace(t.SettlementID)
	}
	if strings.TrimSpace(t.ResultState) != "" {
		out[models.AuditMetaResultState] = strings.TrimSpace(t.ResultState)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type UpstreamAttempt struct {
	AttemptNumber  int
	UpstreamAction string

	RequestHeaders map[string]any
	RequestBody    map[string]any

	ResponseStatus  int
	ResponseHeaders map[string]any
	ResponseBody    any

	LatencyMs int64
	Error     *string
}

type Event struct {
	Type     models.EventType
	Actor    string
	Action   string
	Resource string
	Metadata map[string]any
}

func (s *Service) RecordRelayCall(ctx context.Context, call RelayCall) error {
	if err := s.RecordRelayDownstream(ctx, call); err != nil {
		return err
	}
	if err := s.RecordRelayUpstreamAndEvents(ctx, call); err != nil {
		return err
	}
	return nil
}

func (s *Service) RecordRelayDownstream(ctx context.Context, call RelayCall) error {
	if s.downstreamRepo == nil || s.upstreamRepo == nil || s.auditRepo == nil {
		return internalerrors.New(internalerrors.ErrInternalError, "audit repositories are required", nil)
	}

	normalizeRelayCall(&call)

	if call.RequestID == "" {
		return internalerrors.New(internalerrors.ErrValidationFailed, "request_id is required", nil)
	}
	if call.APIKeyID == "" {
		return internalerrors.New(internalerrors.ErrValidationFailed, "api_key_id is required", nil)
	}
	if call.Method == "" {
		return internalerrors.New(internalerrors.ErrValidationFailed, "method is required", nil)
	}
	if call.Path == "" {
		return internalerrors.New(internalerrors.ErrValidationFailed, "path is required", nil)
	}

	now := s.now().UTC()

	dsID, err := generateID(s.random, "dreq_")
	if err != nil {
		return internalerrors.New(internalerrors.ErrInternalError, "generate downstream request id", err)
	}
	ds := models.DownstreamRequest{
		ID:          dsID,
		RequestID:   call.RequestID,
		APIKeyID:    call.APIKeyID,
		Action:      call.Action,
		Method:      call.Method,
		Path:        call.Path,
		QueryString: call.Query,
		Headers:     sanitizeMap(call.DownstreamHeaders),
		Body:        call.DownstreamBody,
		ClientIP:    call.ClientIP,
		ReceivedAt:  now,
	}
	if err := ds.Validate(); err != nil {
		return internalerrors.New(internalerrors.ErrValidationFailed, "validate downstream request", err)
	}
	if err := s.downstreamRepo.Create(ctx, ds); err != nil {
		return internalerrors.New(internalerrors.ErrAuditFailed, "create downstream request", err)
	}

	return nil
}

func (s *Service) RecordRelayUpstreamAndEvents(ctx context.Context, call RelayCall) error {
	if s.downstreamRepo == nil || s.upstreamRepo == nil || s.auditRepo == nil {
		return internalerrors.New(internalerrors.ErrInternalError, "audit repositories are required", nil)
	}

	normalizeRelayCall(&call)

	if call.RequestID == "" {
		return internalerrors.New(internalerrors.ErrValidationFailed, "request_id is required", nil)
	}
	if call.APIKeyID == "" {
		return internalerrors.New(internalerrors.ErrValidationFailed, "api_key_id is required", nil)
	}
	if call.Method == "" {
		return internalerrors.New(internalerrors.ErrValidationFailed, "method is required", nil)
	}
	if call.Path == "" {
		return internalerrors.New(internalerrors.ErrValidationFailed, "path is required", nil)
	}
	if call.Upstream.AttemptNumber <= 0 {
		return internalerrors.New(internalerrors.ErrValidationFailed, "attempt_number must be > 0", nil)
	}
	if call.Upstream.UpstreamAction == "" {
		return internalerrors.New(internalerrors.ErrValidationFailed, "upstream_action is required", nil)
	}

	now := s.now().UTC()

	usID, err := generateID(s.random, "uattempt_")
	if err != nil {
		return internalerrors.New(internalerrors.ErrInternalError, "generate upstream attempt id", err)
	}
	ua := models.UpstreamAttempt{
		ID:              usID,
		RequestID:       call.RequestID,
		AttemptNumber:   call.Upstream.AttemptNumber,
		UpstreamAction:  call.Upstream.UpstreamAction,
		RequestHeaders:  sanitizeMap(call.Upstream.RequestHeaders),
		RequestBody:     call.Upstream.RequestBody,
		ResponseStatus:  call.Upstream.ResponseStatus,
		ResponseHeaders: sanitizeMap(call.Upstream.ResponseHeaders),
		ResponseBody:    call.Upstream.ResponseBody,
		LatencyMs:       call.Upstream.LatencyMs,
		Error:           call.Upstream.Error,
		SentAt:          now,
	}
	if err := ua.Validate(); err != nil {
		return internalerrors.New(internalerrors.ErrValidationFailed, "validate upstream attempt", err)
	}
	if err := s.upstreamRepo.Create(ctx, ua); err != nil {
		return internalerrors.New(internalerrors.ErrAuditFailed, "create upstream attempt", err)
	}

	events := call.Events
	if len(events) == 0 {
		events = []Event{{
			Type:     models.EventTypeUpstreamResponse,
			Actor:    "system",
			Action:   "relay_call",
			Resource: "relay.call",
			Metadata: map[string]any{"response_status": ua.ResponseStatus, "latency_ms": ua.LatencyMs},
		}}
		if ua.Error != nil {
			events[0].Metadata["error"] = *ua.Error
		}
	}

	billingMeta := sanitizeMap(call.Billing.metadata())

	for _, ev := range events {
		id, err := generateID(s.random, "aevt_")
		if err != nil {
			return internalerrors.New(internalerrors.ErrInternalError, "generate audit event id", err)
		}
		actor := strings.TrimSpace(ev.Actor)
		if actor == "" {
			actor = "system"
		}
		meta := mergeMaps(sanitizeMap(ev.Metadata), billingMeta)
		e := models.AuditEvent{
			ID:        id,
			RequestID: call.RequestID,
			EventType: ev.Type,
			Actor:     actor,
			Action:    strings.TrimSpace(ev.Action),
			Resource:  strings.TrimSpace(ev.Resource),
			Metadata:  meta,
			CreatedAt: now,
		}
		if err := e.Validate(); err != nil {
			return internalerrors.New(internalerrors.ErrValidationFailed, "validate audit event", err)
		}
		if err := s.auditRepo.Create(ctx, e); err != nil {
			return internalerrors.New(internalerrors.ErrAuditFailed, "create audit event", err)
		}
	}

	return nil
}

func normalizeRelayCall(call *RelayCall) {
	call.RequestID = strings.TrimSpace(call.RequestID)
	call.APIKeyID = strings.TrimSpace(call.APIKeyID)
	call.Method = strings.TrimSpace(call.Method)
	call.Path = strings.TrimSpace(call.Path)
	call.Query = strings.TrimSpace(call.Query)
	call.ClientIP = strings.TrimSpace(call.ClientIP)
	call.Billing.ComputedCredit = strings.TrimSpace(call.Billing.ComputedCredit)
	call.Billing.PreAuthID = strings.TrimSpace(call.Billing.PreAuthID)
	call.Billing.SettlementID = strings.TrimSpace(call.Billing.SettlementID)
	call.Billing.ResultState = strings.TrimSpace(call.Billing.ResultState)
	call.Upstream.UpstreamAction = strings.TrimSpace(call.Upstream.UpstreamAction)
}

func generateID(r io.Reader, prefix string) (string, error) {
	b := make([]byte, 8)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b), nil
}

func sanitizeMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		if shouldRedactKey(k) {
			out[k] = "***"
			continue
		}
		out[k] = sanitizeAny(v)
	}
	return out
}

func sanitizeAny(v any) any {
	switch vv := v.(type) {
	case map[string]any:
		return sanitizeMap(vv)
	case map[string]string:
		m := make(map[string]any, len(vv))
		for k, v := range vv {
			m[k] = v
		}
		return sanitizeMap(m)
	case []any:
		out := make([]any, len(vv))
		for i := range vv {
			out[i] = sanitizeAny(vv[i])
		}
		return out
	case []string:
		out := make([]any, len(vv))
		for i := range vv {
			out[i] = vv[i]
		}
		return out
	default:
		return v
	}
}

func mergeMaps(a, b map[string]any) map[string]any {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func shouldRedactKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	switch {
	case k == "authorization":
		return true
	case k == "x-date" || k == "x-security-token" || strings.HasPrefix(k, "x-amz-"):
		return true
	case k == "signature" || k == "x-amz-signature":
		return true
	case k == "secret_key" || k == "sk" || k == "secretkey" || k == "access_key_secret":
		return true
	default:
		return false
	}
}
