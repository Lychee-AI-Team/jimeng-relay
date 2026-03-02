package models

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

type BillingLedgerEntry struct {
	ID        string          `json:"id"`
	RequestID string          `json:"request_id"`
	APIKeyID  string          `json:"api_key_id"`
	Amount    decimal.Decimal `json:"amount"`
	Currency  string          `json:"currency"`
	Status    string          `json:"status"`
	CreatedAt time.Time       `json:"created_at"`
	SettledAt *time.Time      `json:"settled_at,omitempty"`
}

func (e BillingLedgerEntry) Validate() error {
	if e.ID == "" {
		return fmt.Errorf("id is required")
	}
	if e.RequestID == "" {
		return fmt.Errorf("request_id is required")
	}
	if e.APIKeyID == "" {
		return fmt.Errorf("api_key_id is required")
	}
	if e.Amount.IsZero() {
		return fmt.Errorf("amount is required")
	}
	if e.Currency == "" {
		return fmt.Errorf("currency is required")
	}
	if e.Status == "" {
		return fmt.Errorf("status is required")
	}
	if e.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	if e.SettledAt != nil && e.SettledAt.IsZero() {
		return fmt.Errorf("settled_at must not be zero")
	}
	if e.SettledAt != nil && e.SettledAt.Before(e.CreatedAt) {
		return fmt.Errorf("settled_at must be after created_at")
	}
	return nil
}
