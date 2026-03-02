package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/jimeng-relay/server/internal/models"
	"github.com/shopspring/decimal"
)

type APIKeyRepository interface {
	Create(ctx context.Context, key models.APIKey) error
	GetByID(ctx context.Context, id string) (models.APIKey, error)
	GetByAccessKey(ctx context.Context, accessKey string) (models.APIKey, error)
	List(ctx context.Context) ([]models.APIKey, error)
	Revoke(ctx context.Context, id string, revokedAt time.Time) error
	SetExpired(ctx context.Context, id string, expiredAt time.Time) error
	SetExpiresAt(ctx context.Context, id string, expiresAt time.Time) error
}

type DownstreamRequestRepository interface {
	Create(ctx context.Context, request models.DownstreamRequest) error
	GetByID(ctx context.Context, id string) (models.DownstreamRequest, error)
	GetByRequestID(ctx context.Context, requestID string) (models.DownstreamRequest, error)
}

type UpstreamAttemptRepository interface {
	Create(ctx context.Context, attempt models.UpstreamAttempt) error
	ListByRequestID(ctx context.Context, requestID string) ([]models.UpstreamAttempt, error)
}

type AuditEventRepository interface {
	Create(ctx context.Context, event models.AuditEvent) error
	ListByRequestID(ctx context.Context, requestID string) ([]models.AuditEvent, error)
	ListByTimeRange(ctx context.Context, start, end time.Time) ([]models.AuditEvent, error)
}

type IdempotencyRecordRepository interface {
	GetByKey(ctx context.Context, idempotencyKey string) (models.IdempotencyRecord, error)
	Create(ctx context.Context, record models.IdempotencyRecord) error
	DeleteExpired(ctx context.Context, now time.Time) (int64, error)
}

type AdminUserRepository interface {
	Create(ctx context.Context, user models.AdminUser) error
	GetByID(ctx context.Context, id string) (models.AdminUser, error)
	GetByEmail(ctx context.Context, email string) (models.AdminUser, error)
	Update(ctx context.Context, user models.AdminUser) error
}

type AdminSessionRepository interface {
	Create(ctx context.Context, session models.AdminSession) error
	GetByTokenHash(ctx context.Context, tokenHash string) (models.AdminSession, error)
	Delete(ctx context.Context, id string) error
	DeleteExpired(ctx context.Context, now time.Time) (int64, error)
}

type PasswordResetTokenRepository interface {
	Create(ctx context.Context, token models.PasswordResetToken) error
	GetByTokenHash(ctx context.Context, tokenHash string) (models.PasswordResetToken, error)
	MarkUsed(ctx context.Context, id string, usedAt time.Time) error
	DeleteExpired(ctx context.Context, now time.Time) (int64, error)
}

type PricingPresetRepository interface {
	Upsert(ctx context.Context, preset models.PricingPreset) error
	Get(ctx context.Context, preset string) (models.PricingPreset, error)
	List(ctx context.Context) ([]models.PricingPreset, error)
}

type KeyBudgetRepository interface {
	GetByAPIKeyID(ctx context.Context, apiKeyID string) (models.KeyBudget, error)
	UpdateCredits(ctx context.Context, apiKeyID string, delta decimal.Decimal, tx *sql.Tx) error
	ReserveCredits(ctx context.Context, apiKeyID string, amount decimal.Decimal, tx *sql.Tx) error
}

type BillingLedgerRepository interface {
	Create(ctx context.Context, entry models.BillingLedgerEntry, tx *sql.Tx) error
	GetByRequestID(ctx context.Context, requestID string) (models.BillingLedgerEntry, error)
	UpdateStatus(ctx context.Context, id string, status string, settledAt time.Time, tx *sql.Tx) error
}
