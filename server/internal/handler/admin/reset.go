package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jimeng-relay/server/internal/models"
	"github.com/jimeng-relay/server/internal/repository"
	adminservice "github.com/jimeng-relay/server/internal/service/admin"
	"golang.org/x/crypto/bcrypt"
)

const (
	resetTokenTTL         = 15 * time.Minute
	defaultBcryptCost     = 12
	requestResponseText   = "If the email exists, a reset link has been sent."
	resetDoneResponseText = "Password has been reset successfully."
	invalidTokenText      = "Invalid or expired reset token."
)

type ResetHandler struct {
	adminUsers   repository.AdminUserRepository
	resetTokens  repository.PasswordResetTokenRepository
	email        adminservice.EmailProvider
	logger       *slog.Logger
	now          func() time.Time
	bcryptCost   int
	resetBaseURL string
}

type ResetHandlerConfig struct {
	Now          func() time.Time
	BcryptCost   int
	ResetBaseURL string
}

func NewResetHandler(
	adminUsers repository.AdminUserRepository,
	resetTokens repository.PasswordResetTokenRepository,
	emailProvider adminservice.EmailProvider,
	logger *slog.Logger,
	cfg ResetHandlerConfig,
) *ResetHandler {
	if logger == nil {
		logger = slog.Default()
	}
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	bcryptCost := cfg.BcryptCost
	if bcryptCost <= 0 {
		bcryptCost = defaultBcryptCost
	}
	baseURL := strings.TrimSpace(cfg.ResetBaseURL)
	if baseURL == "" {
		baseURL = "http://localhost:8080/admin/reset"
	}
	return &ResetHandler{
		adminUsers:   adminUsers,
		resetTokens:  resetTokens,
		email:        emailProvider,
		logger:       logger,
		now:          now,
		bcryptCost:   bcryptCost,
		resetBaseURL: baseURL,
	}
}

func (h *ResetHandler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/reset-request", h.handleResetRequest)
	mux.HandleFunc("/admin/reset", h.handleReset)
	return mux
}

type resetRequestPayload struct {
	Email string `json:"email"`
}

type resetPayload struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

func (h *ResetHandler) handleResetRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if h.adminUsers == nil || h.resetTokens == nil || h.email == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "service unavailable"})
		return
	}

	payload := resetRequestPayload{}
	if err := decodeJSON(r.Body, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request payload"})
		return
	}

	ctx := r.Context()
	now := h.now().UTC()
	email := normalizeEmail(payload.Email)
	if email != "" {
		if err := h.issueResetToken(ctx, email, now); err != nil {
			h.logger.WarnContext(ctx, "issue reset token failed", "email", email, "error", err.Error())
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"message": requestResponseText})
}

func (h *ResetHandler) issueResetToken(ctx context.Context, email string, now time.Time) error {
	user, err := h.adminUsers.GetByEmail(ctx, email)
	if err != nil {
		if repository.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get admin user by email: %w", err)
	}

	plaintextToken, tokenHash, err := newResetToken()
	if err != nil {
		return err
	}

	resetToken := models.PasswordResetToken{
		ID:          "prt_" + randomHex(8),
		AdminUserID: user.ID,
		TokenHash:   tokenHash,
		CreatedAt:   now,
		ExpiresAt:   now.Add(resetTokenTTL),
	}
	if err := h.resetTokens.Create(ctx, resetToken); err != nil {
		return fmt.Errorf("create reset token: %w", err)
	}

	resetLink := buildResetLink(h.resetBaseURL, plaintextToken)
	if err := h.email.SendPasswordReset(ctx, user.Email, resetLink); err != nil {
		return fmt.Errorf("send reset email: %w", err)
	}

	return nil
}

func (h *ResetHandler) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if h.adminUsers == nil || h.resetTokens == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "service unavailable"})
		return
	}

	payload := resetPayload{}
	if err := decodeJSON(r.Body, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request payload"})
		return
	}

	token := strings.TrimSpace(payload.Token)
	newPassword := strings.TrimSpace(payload.NewPassword)
	if token == "" || newPassword == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": invalidTokenText})
		return
	}

	ctx := r.Context()
	now := h.now().UTC()
	tokenHash := hashResetToken(token)
	resetToken, err := h.resetTokens.GetByTokenHash(ctx, tokenHash)
	if err != nil {
		if repository.IsNotFound(err) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": invalidTokenText})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to reset password"})
		return
	}

	if resetToken.UsedAt != nil || !resetToken.ExpiresAt.After(now) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": invalidTokenText})
		return
	}

	if err := h.resetTokens.MarkUsed(ctx, resetToken.ID, now); err != nil {
		if repository.IsNotFound(err) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": invalidTokenText})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to reset password"})
		return
	}

	adminUser, err := h.adminUsers.GetByID(ctx, resetToken.AdminUserID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": invalidTokenText})
		return
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(newPassword), h.bcryptCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to reset password"})
		return
	}

	adminUser.PasswordHash = string(hashed)
	adminUser.UpdatedAt = now
	if err := h.adminUsers.Update(ctx, adminUser); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to reset password"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"message": resetDoneResponseText})
}

func decodeJSON(body io.ReadCloser, dst any) error {
	if body == nil {
		return fmt.Errorf("body is required")
	}
	defer body.Close()

	dec := json.NewDecoder(io.LimitReader(body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}

func normalizeEmail(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

func newResetToken() (string, string, error) {
	buf := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", "", fmt.Errorf("generate reset token: %w", err)
	}
	plaintext := base64.RawURLEncoding.EncodeToString(buf)
	return plaintext, hashResetToken(plaintext), nil
}

func hashResetToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func buildResetLink(baseURL, token string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = "http://localhost:8080/admin/reset"
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return baseURL + "?token=" + url.QueryEscape(token)
	}
	q := u.Query()
	q.Set("token", token)
	u.RawQuery = q.Encode()
	return u.String()
}

func randomHex(size int) string {
	if size <= 0 {
		size = 8
	}
	b := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "fallback"
	}
	return hex.EncodeToString(b)
}
