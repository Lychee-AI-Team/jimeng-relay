package apikey

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"

	internalerrors "github.com/jimeng-relay/server/internal/errors"
	"github.com/jimeng-relay/server/internal/models"
	"github.com/jimeng-relay/server/internal/repository"
	"github.com/jimeng-relay/server/internal/secretcrypto"
	"golang.org/x/crypto/bcrypt"
)

const (
	defaultBcryptCost = 12
)

type Config struct {
	Now          func() time.Time
	Random       io.Reader
	BcryptCost   int
	SecretCipher secretcrypto.Cipher
}

type Service struct {
	repo         repository.APIKeyRepository
	now          func() time.Time
	random       io.Reader
	bcryptCost   int
	secretCipher secretcrypto.Cipher
}

type CreateRequest struct {
	Description string
	ExpiresAt   *time.Time
}

type RotateRequest struct {
	ID          string
	Description *string
	ExpiresAt   *time.Time
	GracePeriod time.Duration
}

type KeyWithSecret struct {
	ID          string              `json:"id"`
	AccessKey   string              `json:"access_key"`
	SecretKey   string              `json:"secret_key"`
	Description string              `json:"description,omitempty"`
	CreatedAt   time.Time           `json:"created_at"`
	ExpiresAt   *time.Time          `json:"expires_at,omitempty"`
	RotationOf  *string             `json:"rotation_of,omitempty"`
	Status      models.APIKeyStatus `json:"status"`
}

type KeyView struct {
	ID          string              `json:"id"`
	AccessKey   string              `json:"access_key"`
	Description string              `json:"description,omitempty"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
	ExpiresAt   *time.Time          `json:"expires_at,omitempty"`
	RevokedAt   *time.Time          `json:"revoked_at,omitempty"`
	RotationOf  *string             `json:"rotation_of,omitempty"`
	Status      models.APIKeyStatus `json:"status"`
}

func NewService(repo repository.APIKeyRepository, cfg Config) *Service {
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}
	rnd := cfg.Random
	if rnd == nil {
		rnd = rand.Reader
	}
	cost := cfg.BcryptCost
	if cost <= 0 {
		cost = defaultBcryptCost
	}
	return &Service{repo: repo, now: nowFn, random: rnd, bcryptCost: cost, secretCipher: cfg.SecretCipher}
}

func (s *Service) Create(ctx context.Context, req CreateRequest) (KeyWithSecret, error) {
	now := s.now().UTC()
	expiresAt, err := validateExpiresAt(req.ExpiresAt, now)
	if err != nil {
		return KeyWithSecret{}, err
	}
	return s.createKey(ctx, strings.TrimSpace(req.Description), expiresAt, nil)
}

func (s *Service) createKey(ctx context.Context, description string, expiresAt *time.Time, rotationOf *string) (KeyWithSecret, error) {
	now := s.now().UTC()
	id, err := generateID(s.random)
	if err != nil {
		return KeyWithSecret{}, internalerrors.New(internalerrors.ErrInternalError, "generate key id", err)
	}
	accessKey, err := generateToken(s.random, "ak", 10)
	if err != nil {
		return KeyWithSecret{}, internalerrors.New(internalerrors.ErrInternalError, "generate access key", err)
	}
	secretKey, err := generateToken(s.random, "sk", 20)
	if err != nil {
		return KeyWithSecret{}, internalerrors.New(internalerrors.ErrInternalError, "generate secret key", err)
	}
	secretHash, err := bcrypt.GenerateFromPassword([]byte(secretKey), s.bcryptCost)
	if err != nil {
		return KeyWithSecret{}, internalerrors.New(internalerrors.ErrInternalError, "hash secret key", err)
	}
	if s.secretCipher == nil {
		return KeyWithSecret{}, internalerrors.New(internalerrors.ErrInternalError, "secret cipher is required", nil)
	}
	secretCiphertext, err := s.secretCipher.Encrypt(secretKey)
	if err != nil {
		return KeyWithSecret{}, internalerrors.New(internalerrors.ErrInternalError, "encrypt secret key", err)
	}

	key := models.APIKey{
		ID:                  id,
		AccessKey:           accessKey,
		SecretKeyHash:       string(secretHash),
		SecretKeyCiphertext: secretCiphertext,
		Description:         description,
		CreatedAt:           now,
		UpdatedAt:           now,
		ExpiresAt:           expiresAt,
		RotationOf:          rotationOf,
		Multiplier:          10000,
		Status:              models.APIKeyStatusActive,
	}

	if err := key.Validate(); err != nil {
		return KeyWithSecret{}, internalerrors.New(internalerrors.ErrValidationFailed, "validate api key", err)
	}
	if err := s.repo.Create(ctx, key); err != nil {
		return KeyWithSecret{}, internalerrors.New(internalerrors.ErrDatabaseError, "create api key", err)
	}

	return KeyWithSecret{
		ID:          key.ID,
		AccessKey:   key.AccessKey,
		SecretKey:   secretKey,
		Description: key.Description,
		CreatedAt:   key.CreatedAt,
		ExpiresAt:   key.ExpiresAt,
		RotationOf:  key.RotationOf,
		Status:      effectiveStatus(key),
	}, nil
}

func (s *Service) List(ctx context.Context) ([]KeyView, error) {
	keys, err := s.repo.List(ctx)
	if err != nil {
		return nil, internalerrors.New(internalerrors.ErrDatabaseError, "list api keys", err)
	}
	out := make([]KeyView, 0, len(keys))
	for _, key := range keys {
		out = append(out, KeyView{
			ID:          key.ID,
			AccessKey:   key.AccessKey,
			Description: key.Description,
			CreatedAt:   key.CreatedAt,
			UpdatedAt:   key.UpdatedAt,
			ExpiresAt:   key.ExpiresAt,
			RevokedAt:   key.RevokedAt,
			RotationOf:  key.RotationOf,
			Status:      effectiveStatus(key),
		})
	}
	return out, nil
}

func (s *Service) Revoke(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return internalerrors.New(internalerrors.ErrValidationFailed, "id is required", nil)
	}
	if err := s.repo.Revoke(ctx, id, s.now().UTC()); err != nil {
		if repository.IsNotFound(err) {
			return internalerrors.New(internalerrors.ErrValidationFailed, "api key not found", err)
		}
		return internalerrors.New(internalerrors.ErrDatabaseError, "revoke api key", err)
	}
	return nil
}

func (s *Service) Rotate(ctx context.Context, req RotateRequest) (KeyWithSecret, error) {
	req.ID = strings.TrimSpace(req.ID)
	if req.ID == "" {
		return KeyWithSecret{}, internalerrors.New(internalerrors.ErrValidationFailed, "id is required", nil)
	}
	if req.GracePeriod < 0 {
		return KeyWithSecret{}, internalerrors.New(internalerrors.ErrValidationFailed, "grace_period must be >= 0", nil)
	}

	oldKey, err := s.repo.GetByID(ctx, req.ID)
	if err != nil {
		if repository.IsNotFound(err) {
			return KeyWithSecret{}, internalerrors.New(internalerrors.ErrValidationFailed, "api key not found", err)
		}
		return KeyWithSecret{}, internalerrors.New(internalerrors.ErrDatabaseError, "get api key", err)
	}
	if oldKey.IsRevoked() {
		return KeyWithSecret{}, internalerrors.New(internalerrors.ErrKeyRevoked, "api key is revoked", nil)
	}
	if oldKey.IsExpired() {
		return KeyWithSecret{}, internalerrors.New(internalerrors.ErrKeyExpired, "api key is expired", nil)
	}

	description := oldKey.Description
	if req.Description != nil {
		description = strings.TrimSpace(*req.Description)
	}
	now := s.now().UTC()
	expiresAt, err := validateExpiresAt(req.ExpiresAt, now)
	if err != nil {
		return KeyWithSecret{}, err
	}

	created, err := s.createKey(ctx, description, expiresAt, &oldKey.ID)
	if err != nil {
		return KeyWithSecret{}, err
	}
	if req.GracePeriod == 0 {
		if err := s.repo.Revoke(ctx, oldKey.ID, now); err != nil {
			return KeyWithSecret{}, internalerrors.New(internalerrors.ErrDatabaseError, "revoke old api key", err)
		}
	} else {
		if err := s.repo.SetExpiresAt(ctx, oldKey.ID, now.Add(req.GracePeriod)); err != nil {
			return KeyWithSecret{}, internalerrors.New(internalerrors.ErrDatabaseError, "set old api key rotation window", err)
		}
	}
	created.Status = models.APIKeyStatusActive
	return created, nil
}

func validateExpiresAt(expiresAt *time.Time, now time.Time) (*time.Time, error) {
	if expiresAt == nil {
		return nil, nil
	}
	v := expiresAt.UTC()
	if !v.After(now) {
		return nil, internalerrors.New(internalerrors.ErrValidationFailed, "expires_at must be in the future", nil)
	}
	return &v, nil
}

func effectiveStatus(key models.APIKey) models.APIKeyStatus {
	if key.IsRevoked() {
		return models.APIKeyStatusRevoked
	}
	if key.IsExpired() {
		return models.APIKeyStatusExpired
	}
	return models.APIKeyStatusActive
}

func generateID(r io.Reader) (string, error) {
	b := make([]byte, 8)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return "key_" + hex.EncodeToString(b), nil
}

func generateToken(r io.Reader, prefix string, bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	return fmt.Sprintf("%s_%s", prefix, strings.ToLower(enc)), nil
}
