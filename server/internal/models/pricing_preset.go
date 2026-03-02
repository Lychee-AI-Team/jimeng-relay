package models

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

type PricingPreset struct {
	Preset         string          `json:"preset"`
	Description    string          `json:"description,omitempty"`
	PricePerCredit decimal.Decimal `json:"price_per_credit"`
	Currency       string          `json:"currency"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

func (p PricingPreset) Validate() error {
	if p.Preset == "" {
		return fmt.Errorf("preset is required")
	}
	if p.Currency == "" {
		return fmt.Errorf("currency is required")
	}
	if p.PricePerCredit.IsNegative() {
		return fmt.Errorf("price_per_credit must be zero or positive")
	}
	if p.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	if p.UpdatedAt.IsZero() {
		return fmt.Errorf("updated_at is required")
	}
	return nil
}
