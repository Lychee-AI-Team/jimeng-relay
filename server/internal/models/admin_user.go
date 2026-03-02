package models

import (
	"fmt"
	"time"
)

type AdminUser struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"password_hash"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (u AdminUser) Validate() error {
	if u.ID == "" {
		return fmt.Errorf("id is required")
	}
	if u.Email == "" {
		return fmt.Errorf("email is required")
	}
	if u.PasswordHash == "" {
		return fmt.Errorf("password_hash is required")
	}
	if u.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	if u.UpdatedAt.IsZero() {
		return fmt.Errorf("updated_at is required")
	}
	return nil
}
