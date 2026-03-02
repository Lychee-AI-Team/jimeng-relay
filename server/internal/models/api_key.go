package models

import (
	"fmt"
	"time"
)

type APIKeyStatus string

const (
	APIKeyStatusActive  APIKeyStatus = "active"
	APIKeyStatusExpired APIKeyStatus = "expired"
	APIKeyStatusRevoked APIKeyStatus = "revoked"
)

type APIKey struct {
	ID                  string       `json:"id"`
	AccessKey           string       `json:"access_key"`
	SecretKeyHash       string       `json:"secret_key_hash"`
	SecretKeyCiphertext string       `json:"-"`
	Description         string       `json:"description,omitempty"`
	Multiplier          int          `json:"multiplier"`
	CreatedAt           time.Time    `json:"created_at"`
	UpdatedAt           time.Time    `json:"updated_at"`
	ExpiresAt           *time.Time   `json:"expires_at,omitempty"`
	RevokedAt           *time.Time   `json:"revoked_at,omitempty"`
	RotationOf          *string      `json:"rotation_of,omitempty"`
	Status              APIKeyStatus `json:"status"`
}

func (k APIKey) IsActive() bool {
	return k.Status == APIKeyStatusActive && !k.IsExpired() && !k.IsRevoked()
}

func (k APIKey) IsExpired() bool {
	if k.Status == APIKeyStatusExpired {
		return true
	}
	if k.ExpiresAt == nil || k.ExpiresAt.IsZero() {
		return false
	}
	return k.ExpiresAt.UTC().Before(time.Now().UTC())
}

func (k APIKey) IsRevoked() bool {
	if k.Status == APIKeyStatusRevoked {
		return true
	}
	return k.RevokedAt != nil && !k.RevokedAt.IsZero()
}

func (k APIKey) Validate() error {
	if k.ID == "" {
		return fmt.Errorf("id is required")
	}
	if k.AccessKey == "" {
		return fmt.Errorf("access_key is required")
	}
	if k.SecretKeyHash == "" {
		return fmt.Errorf("secret_key_hash is required")
	}
	if k.SecretKeyCiphertext == "" {
		return fmt.Errorf("secret_key_ciphertext is required")
	}
	if k.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	if k.UpdatedAt.IsZero() {
		return fmt.Errorf("updated_at is required")
	}
	switch k.Status {
	case APIKeyStatusActive, APIKeyStatusExpired, APIKeyStatusRevoked:
	default:
		return fmt.Errorf("invalid status: %q", k.Status)
	}
	return nil
}
