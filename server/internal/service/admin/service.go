package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"time"

	internalerrors "github.com/jimeng-relay/server/internal/errors"
	"github.com/jimeng-relay/server/internal/models"
	"github.com/jimeng-relay/server/internal/repository"
	"golang.org/x/crypto/bcrypt"
)

const (
	bootstrapAdminID  = "admin_bootstrap_root"
	defaultSessionTTL = 24 * time.Hour
	defaultBcryptCost = 12
	sessionTokenBytes = 32
)

var errBootstrapLocked = errors.New("admin bootstrap is locked")

type Config struct {
	Now        func() time.Time
	Random     io.Reader
	SessionTTL time.Duration
	BcryptCost int
}

type Service struct {
	adminUsers    repository.AdminUserRepository
	adminSessions repository.AdminSessionRepository
	now           func() time.Time
	random        io.Reader
	sessionTTL    time.Duration
	bcryptCost    int
}

type BootstrapRequest struct {
	Email    string
	Password string
}

type BootstrapResult struct {
	AdminUser         models.AdminUser
	SessionToken      string
	SessionExpiresAt  time.Time
	SessionCookieName string
}

func NewService(adminUsers repository.AdminUserRepository, adminSessions repository.AdminSessionRepository, cfg Config) *Service {
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}
	rnd := cfg.Random
	if rnd == nil {
		rnd = rand.Reader
	}
	ttl := cfg.SessionTTL
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	cost := cfg.BcryptCost
	if cost <= 0 {
		cost = defaultBcryptCost
	}

	return &Service{
		adminUsers:    adminUsers,
		adminSessions: adminSessions,
		now:           nowFn,
		random:        rnd,
		sessionTTL:    ttl,
		bcryptCost:    cost,
	}
}

func IsBootstrapLocked(err error) bool {
	return errors.Is(err, errBootstrapLocked)
}

func (s *Service) Bootstrap(ctx context.Context, req BootstrapRequest) (BootstrapResult, error) {
	if s.adminUsers == nil || s.adminSessions == nil {
		return BootstrapResult{}, internalerrors.New(internalerrors.ErrInternalError, "admin repositories are required", nil)
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))
	password := strings.TrimSpace(req.Password)
	if email == "" || !strings.Contains(email, "@") {
		return BootstrapResult{}, internalerrors.New(internalerrors.ErrValidationFailed, "valid email is required", nil)
	}
	if len(password) < 8 {
		return BootstrapResult{}, internalerrors.New(internalerrors.ErrValidationFailed, "password must be at least 8 characters", nil)
	}

	if _, err := s.adminUsers.GetByID(ctx, bootstrapAdminID); err == nil {
		return BootstrapResult{}, errBootstrapLocked
	} else if !repository.IsNotFound(err) {
		return BootstrapResult{}, internalerrors.New(internalerrors.ErrDatabaseError, "check bootstrap admin", err)
	}

	now := s.now().UTC()
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), s.bcryptCost)
	if err != nil {
		return BootstrapResult{}, internalerrors.New(internalerrors.ErrInternalError, "hash admin password", err)
	}

	user := models.AdminUser{
		ID:           bootstrapAdminID,
		Email:        email,
		PasswordHash: string(passwordHash),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.adminUsers.Create(ctx, user); err != nil {
		if isConflictError(err) {
			return BootstrapResult{}, errBootstrapLocked
		}
		return BootstrapResult{}, internalerrors.New(internalerrors.ErrDatabaseError, "create bootstrap admin", err)
	}

	sessionToken, err := randomToken(s.random, sessionTokenBytes)
	if err != nil {
		return BootstrapResult{}, internalerrors.New(internalerrors.ErrInternalError, "generate session token", err)
	}
	sessionID, err := randomID(s.random, "as")
	if err != nil {
		return BootstrapResult{}, internalerrors.New(internalerrors.ErrInternalError, "generate session id", err)
	}

	session := models.AdminSession{
		ID:          sessionID,
		AdminUserID: user.ID,
		TokenHash:   sha256Hex(sessionToken),
		CreatedAt:   now,
		ExpiresAt:   now.Add(s.sessionTTL),
	}
	if err := s.adminSessions.Create(ctx, session); err != nil {
		return BootstrapResult{}, internalerrors.New(internalerrors.ErrDatabaseError, "create bootstrap session", err)
	}

	return BootstrapResult{
		AdminUser:        user,
		SessionToken:     sessionToken,
		SessionExpiresAt: session.ExpiresAt,
	}, nil
}

func isConflictError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "duplicate")
}

func randomID(r io.Reader, prefix string) (string, error) {
	b := make([]byte, 8)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b), nil
}

func randomToken(r io.Reader, n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func sha256Hex(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}
