package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/jimeng-relay/server/internal/models"
	"github.com/jimeng-relay/server/internal/repository"
	"golang.org/x/crypto/bcrypt"
)

type mockAdminUserRepo struct {
	users map[string]models.AdminUser
}

func (m *mockAdminUserRepo) Create(_ context.Context, user models.AdminUser) error {
	if m.users == nil {
		m.users = map[string]models.AdminUser{}
	}
	if _, ok := m.users[user.ID]; ok {
		return errUnique
	}
	for _, item := range m.users {
		if item.Email == user.Email {
			return errUnique
		}
	}
	m.users[user.ID] = user
	return nil
}

func (m *mockAdminUserRepo) GetByID(_ context.Context, id string) (models.AdminUser, error) {
	if v, ok := m.users[id]; ok {
		return v, nil
	}
	return models.AdminUser{}, repository.ErrNotFound
}

func (m *mockAdminUserRepo) GetByEmail(_ context.Context, email string) (models.AdminUser, error) {
	for _, v := range m.users {
		if v.Email == email {
			return v, nil
		}
	}
	return models.AdminUser{}, repository.ErrNotFound
}

func (m *mockAdminUserRepo) Update(_ context.Context, user models.AdminUser) error {
	if _, ok := m.users[user.ID]; !ok {
		return repository.ErrNotFound
	}
	m.users[user.ID] = user
	return nil
}

type mockAdminSessionRepo struct {
	sessions map[string]models.AdminSession
}

func (m *mockAdminSessionRepo) Create(_ context.Context, session models.AdminSession) error {
	if m.sessions == nil {
		m.sessions = map[string]models.AdminSession{}
	}
	m.sessions[session.ID] = session
	return nil
}

func (m *mockAdminSessionRepo) GetByTokenHash(_ context.Context, tokenHash string) (models.AdminSession, error) {
	for _, v := range m.sessions {
		if v.TokenHash == tokenHash {
			return v, nil
		}
	}
	return models.AdminSession{}, repository.ErrNotFound
}

func (m *mockAdminSessionRepo) Delete(_ context.Context, id string) error {
	if _, ok := m.sessions[id]; !ok {
		return repository.ErrNotFound
	}
	delete(m.sessions, id)
	return nil
}

func (m *mockAdminSessionRepo) DeleteExpired(_ context.Context, now time.Time) (int64, error) {
	var deleted int64
	for id, s := range m.sessions {
		if !s.ExpiresAt.After(now) {
			delete(m.sessions, id)
			deleted++
		}
	}
	return deleted, nil
}

var errUnique = bcrypt.ErrMismatchedHashAndPassword

func TestBootstrapCreatesFirstAdminAndSession(t *testing.T) {
	users := &mockAdminUserRepo{}
	sessions := &mockAdminSessionRepo{}
	now := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)

	svc := NewService(users, sessions, Config{Now: func() time.Time { return now }})
	res, err := svc.Bootstrap(context.Background(), BootstrapRequest{Email: "Root@Example.com", Password: "Password123"})
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}

	if res.AdminUser.ID != bootstrapAdminID {
		t.Fatalf("expected bootstrap admin id %q, got %q", bootstrapAdminID, res.AdminUser.ID)
	}
	if res.AdminUser.Email != "root@example.com" {
		t.Fatalf("expected normalized email, got %q", res.AdminUser.Email)
	}
	if res.SessionExpiresAt.Sub(now) != defaultSessionTTL {
		t.Fatalf("expected ttl %s, got %s", defaultSessionTTL, res.SessionExpiresAt.Sub(now))
	}

	createdUser, ok := users.users[bootstrapAdminID]
	if !ok {
		t.Fatalf("expected user persisted")
	}
	if createdUser.PasswordHash == "Password123" {
		t.Fatalf("password must be hashed")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(createdUser.PasswordHash), []byte("Password123")); err != nil {
		t.Fatalf("stored hash does not match input password: %v", err)
	}

	if len(sessions.sessions) != 1 {
		t.Fatalf("expected one session, got %d", len(sessions.sessions))
	}
	for _, s := range sessions.sessions {
		sum := sha256.Sum256([]byte(res.SessionToken))
		expected := hex.EncodeToString(sum[:])
		if s.TokenHash != expected {
			t.Fatalf("expected token hash %q, got %q", expected, s.TokenHash)
		}
		if s.TokenHash == res.SessionToken {
			t.Fatalf("token hash must not store plaintext token")
		}
	}
}

func TestBootstrapLockedAfterFirstAdmin(t *testing.T) {
	users := &mockAdminUserRepo{}
	sessions := &mockAdminSessionRepo{}
	svc := NewService(users, sessions, Config{})

	if _, err := svc.Bootstrap(context.Background(), BootstrapRequest{Email: "first@example.com", Password: "Password123"}); err != nil {
		t.Fatalf("first Bootstrap() error = %v", err)
	}

	_, err := svc.Bootstrap(context.Background(), BootstrapRequest{Email: "second@example.com", Password: "Password456"})
	if !IsBootstrapLocked(err) {
		t.Fatalf("expected bootstrap locked error, got %v", err)
	}
}
