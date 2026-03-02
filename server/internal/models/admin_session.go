package models

import (
	"fmt"
	"time"
)

type AdminSession struct {
	ID          string    `json:"id"`
	AdminUserID string    `json:"admin_user_id"`
	TokenHash   string    `json:"token_hash"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func (s AdminSession) Validate() error {
	if s.ID == "" {
		return fmt.Errorf("id is required")
	}
	if s.AdminUserID == "" {
		return fmt.Errorf("admin_user_id is required")
	}
	if s.TokenHash == "" {
		return fmt.Errorf("token_hash is required")
	}
	if s.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	if s.ExpiresAt.IsZero() {
		return fmt.Errorf("expires_at is required")
	}
	if !s.ExpiresAt.After(s.CreatedAt) {
		return fmt.Errorf("expires_at must be after created_at")
	}
	return nil
}
