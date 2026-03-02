package models

import (
	"fmt"
	"time"
)

type PasswordResetToken struct {
	ID          string     `json:"id"`
	AdminUserID string     `json:"admin_user_id"`
	TokenHash   string     `json:"token_hash"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	UsedAt      *time.Time `json:"used_at,omitempty"`
}

func (t PasswordResetToken) Validate() error {
	if t.ID == "" {
		return fmt.Errorf("id is required")
	}
	if t.AdminUserID == "" {
		return fmt.Errorf("admin_user_id is required")
	}
	if t.TokenHash == "" {
		return fmt.Errorf("token_hash is required")
	}
	if t.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	if t.ExpiresAt.IsZero() {
		return fmt.Errorf("expires_at is required")
	}
	if !t.ExpiresAt.After(t.CreatedAt) {
		return fmt.Errorf("expires_at must be after created_at")
	}
	if t.UsedAt != nil && t.UsedAt.IsZero() {
		return fmt.Errorf("used_at must not be zero")
	}
	if t.UsedAt != nil && t.UsedAt.Before(t.CreatedAt) {
		return fmt.Errorf("used_at must be after created_at")
	}
	return nil
}
