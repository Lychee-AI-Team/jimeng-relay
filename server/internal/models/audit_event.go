package models

import (
	"fmt"
	"time"
)

type EventType string

const (
	EventTypeRequestReceived  EventType = "request_received"
	EventTypeAuthSuccess      EventType = "auth_success"
	EventTypeAuthFailed       EventType = "auth_failed"
	EventTypeUpstreamCall     EventType = "upstream_call"
	EventTypeUpstreamResponse EventType = "upstream_response"
	EventTypeResponseSent     EventType = "response_sent"
	EventTypeError            EventType = "error"
)

const (
	AuditMetaComputedCredit = "computed_credit"
	AuditMetaPreAuthID      = "preauth_id"
	AuditMetaSettlementID   = "settlement_id"
	AuditMetaResultState    = "result_state"
)

type AuditEvent struct {
	ID        string         `json:"id"`
	RequestID string         `json:"request_id"`
	EventType EventType      `json:"event_type"`
	Actor     string         `json:"actor"`
	Action    string         `json:"action"`
	Resource  string         `json:"resource"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

func (e AuditEvent) Validate() error {
	if e.ID == "" {
		return fmt.Errorf("id is required")
	}
	if e.RequestID == "" {
		return fmt.Errorf("request_id is required")
	}
	switch e.EventType {
	case EventTypeRequestReceived,
		EventTypeAuthSuccess,
		EventTypeAuthFailed,
		EventTypeUpstreamCall,
		EventTypeUpstreamResponse,
		EventTypeResponseSent,
		EventTypeError:
	default:
		return fmt.Errorf("invalid event_type: %q", e.EventType)
	}
	if e.Action == "" {
		return fmt.Errorf("action is required")
	}
	if e.Resource == "" {
		return fmt.Errorf("resource is required")
	}
	if e.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	return nil
}
