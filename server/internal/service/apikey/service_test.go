package apikey

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jimeng-relay/server/internal/models"
	"github.com/jimeng-relay/server/internal/repository"
	"github.com/jimeng-relay/server/internal/secretcrypto"
)

type memoryRepo struct {
	keys map[string]models.APIKey
}

func newMemoryRepo() *memoryRepo {
	return &memoryRepo{keys: map[string]models.APIKey{}}
}

func (m *memoryRepo) Create(_ context.Context, key models.APIKey) error {
	if _, exists := m.keys[key.ID]; exists {
		return errors.New("duplicate id")
	}
	for _, existing := range m.keys {
		if existing.AccessKey == key.AccessKey {
			return errors.New("duplicate access key")
		}
	}
	m.keys[key.ID] = key
	return nil
}

func (m *memoryRepo) GetByID(_ context.Context, id string) (models.APIKey, error) {
	key, ok := m.keys[id]
	if !ok {
		return models.APIKey{}, repository.ErrNotFound
	}
	return key, nil
}

func (m *memoryRepo) GetByAccessKey(_ context.Context, accessKey string) (models.APIKey, error) {
	for _, key := range m.keys {
		if key.AccessKey == accessKey {
			return key, nil
		}
	}
	return models.APIKey{}, repository.ErrNotFound
}

func (m *memoryRepo) List(_ context.Context) ([]models.APIKey, error) {
	out := make([]models.APIKey, 0, len(m.keys))
	for _, key := range m.keys {
		out = append(out, key)
	}
	return out, nil
}

func (m *memoryRepo) Revoke(_ context.Context, id string, revokedAt time.Time) error {
	key, ok := m.keys[id]
	if !ok {
		return repository.ErrNotFound
	}
	key.RevokedAt = &revokedAt
	key.Status = models.APIKeyStatusRevoked
	key.UpdatedAt = revokedAt
	m.keys[id] = key
	return nil
}

func (m *memoryRepo) SetExpired(_ context.Context, id string, expiredAt time.Time) error {
	key, ok := m.keys[id]
	if !ok {
		return repository.ErrNotFound
	}
	key.ExpiresAt = &expiredAt
	key.Status = models.APIKeyStatusExpired
	key.UpdatedAt = expiredAt
	m.keys[id] = key
	return nil
}

func (m *memoryRepo) SetExpiresAt(_ context.Context, id string, expiresAt time.Time) error {
	key, ok := m.keys[id]
	if !ok {
		return repository.ErrNotFound
	}
	key.ExpiresAt = &expiresAt
	key.UpdatedAt = expiresAt
	m.keys[id] = key
	return nil
}

func TestServiceLifecycle_CreateListRevokeRotate(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryRepo()
	base := time.Date(2026, 2, 24, 9, 30, 0, 0, time.UTC)
	svc := NewService(repo, Config{
		Now:          func() time.Time { return base },
		BcryptCost:   4,
		SecretCipher: mustTestCipher(t),
	})

	created, err := svc.Create(ctx, CreateRequest{
		Description: "first key",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.SecretKey == "" {
		t.Fatalf("expected plaintext secret key returned once")
	}
	stored, err := repo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.Multiplier != 10000 {
		t.Errorf("expected default multiplier 10000, got %d", stored.Multiplier)
	}
	if stored.SecretKeyHash == created.SecretKey {

		t.Fatalf("GetByID: %v", err)
	}
	if stored.SecretKeyHash == created.SecretKey {
		t.Fatalf("secret key must not be stored as plaintext")
	}
	if stored.SecretKeyCiphertext == "" || stored.SecretKeyCiphertext == created.SecretKey {
		t.Fatalf("secret key ciphertext must be present and not plaintext")
	}
	if len(stored.SecretKeyHash) < 4 || stored.SecretKeyHash[:4] != "$2a$" {
		t.Fatalf("expected bcrypt hash, got %q", stored.SecretKeyHash)
	}

	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 key, got %d", len(list))
	}
	if list[0].AccessKey != created.AccessKey {
		t.Fatalf("unexpected listed key")
	}

	if err := svc.Revoke(ctx, created.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	revoked, err := repo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID after revoke: %v", err)
	}
	if revoked.Status != models.APIKeyStatusRevoked {
		t.Fatalf("expected revoked status, got %q", revoked.Status)
	}

	second, err := svc.Create(ctx, CreateRequest{Description: "for rotation"})
	if err != nil {
		t.Fatalf("Create second key: %v", err)
	}
	rotated, err := svc.Rotate(ctx, RotateRequest{ID: second.ID, GracePeriod: 10 * time.Minute})
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if rotated.SecretKey == "" {
		t.Fatalf("expected rotated key plaintext secret")
	}
	newStored, err := repo.GetByID(ctx, rotated.ID)
	if err != nil {
		t.Fatalf("GetByID rotated key: %v", err)
	}
	if newStored.RotationOf == nil || *newStored.RotationOf != second.ID {
		t.Fatalf("expected rotation_of to point old key")
	}
	oldStored, err := repo.GetByID(ctx, second.ID)
	if err != nil {
		t.Fatalf("GetByID old key after rotation: %v", err)
	}
	if oldStored.ExpiresAt == nil {
		t.Fatalf("expected old key to receive rotation window expiration")
	}
	if oldStored.ExpiresAt.Sub(base) != 10*time.Minute {
		t.Fatalf("unexpected rotation window: %s", oldStored.ExpiresAt.Sub(base))
	}
}

func TestCreate_WithPastExpirationFails(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryRepo()
	base := time.Date(2026, 2, 24, 9, 30, 0, 0, time.UTC)
	svc := NewService(repo, Config{Now: func() time.Time { return base }, BcryptCost: 4, SecretCipher: mustTestCipher(t)})

	past := base.Add(-time.Second)
	_, err := svc.Create(ctx, CreateRequest{ExpiresAt: &past})
	if err == nil {
		t.Fatalf("expected validation error for past expiration")
	}
}

func mustTestCipher(t *testing.T) secretcrypto.Cipher {
	t.Helper()
	c, err := secretcrypto.NewAESCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewAESCipher: %v", err)
	}
	return c
}
