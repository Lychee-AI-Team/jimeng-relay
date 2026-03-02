package models

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

type KeyBudget struct {
	APIKeyID         string          `json:"api_key_id"`
	CreditsAvailable decimal.Decimal `json:"credits_available"`
	CreditsReserved  decimal.Decimal `json:"credits_reserved"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

func (b KeyBudget) Validate() error {
	if b.APIKeyID == "" {
		return fmt.Errorf("api_key_id is required")
	}
	if b.CreditsAvailable.IsNegative() {
		return fmt.Errorf("credits_available must be zero or positive")
	}
	if b.CreditsReserved.IsNegative() {
		return fmt.Errorf("credits_reserved must be zero or positive")
	}
	if b.CreditsReserved.GreaterThan(b.CreditsAvailable) {
		return fmt.Errorf("credits_reserved must be less than or equal to credits_available")
	}
	if b.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	if b.UpdatedAt.IsZero() {
		return fmt.Errorf("updated_at is required")
	}
	return nil
}
