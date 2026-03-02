package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	internalerrors "github.com/jimeng-relay/server/internal/errors"
	"github.com/jimeng-relay/server/internal/models"
	"github.com/jimeng-relay/server/internal/repository"
	"golang.org/x/crypto/bcrypt"
)

const maxLoginBodyBytes int64 = 1 << 20

type AuthHandler struct {
	users      repository.AdminUserRepository
	sessions   repository.AdminSessionRepository
	now        func() time.Time
	cookieName string
	sessionTTL time.Duration
	cookiePath string
	sameSite   http.SameSite
}

type AuthConfig struct {
	Now        func() time.Time
	CookieName string
	SessionTTL time.Duration
}

func NewAuthHandler(users repository.AdminUserRepository, sessions repository.AdminSessionRepository, cfg AuthConfig) *AuthHandler {
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}
	ttl := cfg.SessionTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	name := strings.TrimSpace(cfg.CookieName)
	if name == "" {
		name = "admin_session"
	}
	return &AuthHandler{
		users:      users,
		sessions:   sessions,
		now:        nowFn,
		cookieName: name,
		sessionTTL: ttl,
		cookiePath: "/admin",
		sameSite:   http.SameSiteStrictMode,
	}
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, internalerrors.New(internalerrors.ErrValidationFailed, "method not allowed", nil), http.StatusMethodNotAllowed)
		return
	}
	if h.users == nil || h.sessions == nil {
		writeError(w, internalerrors.New(internalerrors.ErrInternalError, "admin repositories are not configured", nil), http.StatusInternalServerError)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := dec.Decode(&req); err != nil {
		writeError(w, internalerrors.New(internalerrors.ErrValidationFailed, "invalid json body", err), http.StatusBadRequest)
		return
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))
	password := req.Password
	if email == "" || password == "" {
		writeError(w, internalerrors.New(internalerrors.ErrValidationFailed, "email and password are required", nil), http.StatusBadRequest)
		return
	}

	user, err := h.users.GetByEmail(r.Context(), email)
	if err != nil {
		if repository.IsNotFound(err) {
			writeError(w, internalerrors.New(internalerrors.ErrAuthFailed, "invalid credentials", nil), http.StatusUnauthorized)
			return
		}
		writeError(w, internalerrors.New(internalerrors.ErrDatabaseError, "get admin user by email", err), http.StatusInternalServerError)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		writeError(w, internalerrors.New(internalerrors.ErrAuthFailed, "invalid credentials", nil), http.StatusUnauthorized)
		return
	}

	now := h.now().UTC()
	expiresAt := now.Add(h.sessionTTL)
	token, err := randomHexToken(32)
	if err != nil {
		writeError(w, internalerrors.New(internalerrors.ErrInternalError, "generate session token", err), http.StatusInternalServerError)
		return
	}
	tokenHash := sha256Hex([]byte(token))

	sess := models.AdminSession{
		ID:          "asess_" + randomHexFallback(12),
		AdminUserID: user.ID,
		TokenHash:   tokenHash,
		CreatedAt:   now,
		ExpiresAt:   expiresAt,
	}
	if err := sess.Validate(); err != nil {
		writeError(w, internalerrors.New(internalerrors.ErrInternalError, "invalid admin session", err), http.StatusInternalServerError)
		return
	}
	if err := h.sessions.Create(r.Context(), sess); err != nil {
		writeError(w, internalerrors.New(internalerrors.ErrDatabaseError, "create admin session", err), http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     h.cookieName,
		Value:    token,
		Path:     h.cookiePath,
		MaxAge:   int(h.sessionTTL.Seconds()),
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   !isLocalhostRequest(r),
		SameSite: h.sameSite,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"expires_at": expiresAt,
		"user": map[string]any{
			"id":    user.ID,
			"email": user.Email,
		},
	})
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, internalerrors.New(internalerrors.ErrValidationFailed, "method not allowed", nil), http.StatusMethodNotAllowed)
		return
	}

	// Best-effort: attempt server-side session deletion if we have a cookie.
	if h.sessions != nil {
		if c, err := r.Cookie(h.cookieName); err == nil {
			token := strings.TrimSpace(c.Value)
			if token != "" {
				tokenHash := sha256Hex([]byte(token))
				if sess, err := h.sessions.GetByTokenHash(r.Context(), tokenHash); err == nil {
					_ = h.sessions.Delete(r.Context(), sess.ID)
				}
			}
		}
	}

	clearCookie(w, h.cookieName, h.cookiePath, !isLocalhostRequest(r))
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func clearCookie(w http.ResponseWriter, name, path string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     strings.TrimSpace(name),
		Value:    "",
		Path:     path,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0).UTC(),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}

func sha256Hex(v []byte) string {
	s := sha256.Sum256(v)
	return hex.EncodeToString(s[:])
}

func randomHexToken(nbytes int) (string, error) {
	if nbytes <= 0 {
		nbytes = 32
	}
	b := make([]byte, nbytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func randomHexFallback(nbytes int) string {
	tok, err := randomHexToken(nbytes)
	if err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return tok
}

func isLocalhostRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	host := strings.TrimSpace(r.Host)
	if host == "" {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
