package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	internalerrors "github.com/jimeng-relay/server/internal/errors"
	"github.com/jimeng-relay/server/internal/models"
	"github.com/jimeng-relay/server/internal/repository"
)

type contextKey string

const ContextAdminUser contextKey = "admin_user"

type Config struct {
	Now        func() time.Time
	CookieName string
}

type Middleware struct {
	sessions   repository.AdminSessionRepository
	users      repository.AdminUserRepository
	now        func() time.Time
	cookieName string
}

func New(sessions repository.AdminSessionRepository, users repository.AdminUserRepository, cfg Config) func(http.Handler) http.Handler {
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}
	cookieName := strings.TrimSpace(cfg.CookieName)
	if cookieName == "" {
		cookieName = "admin_session"
	}
	m := &Middleware{sessions: sessions, users: users, now: nowFn, cookieName: cookieName}
	return m.wrap
}

func (m *Middleware) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicAdminPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if m.sessions == nil || m.users == nil {
			writeError(w, internalerrors.New(internalerrors.ErrInternalError, "admin auth repositories are not configured", nil), http.StatusInternalServerError)
			return
		}
		token, ok := readCookieToken(r, m.cookieName)
		if !ok {
			writeError(w, internalerrors.New(internalerrors.ErrAuthFailed, "missing admin session", nil), http.StatusUnauthorized)
			return
		}

		now := m.now().UTC()
		tokenHash := sha256Hex([]byte(token))
		sess, err := m.sessions.GetByTokenHash(r.Context(), tokenHash)
		if err != nil {
			if repository.IsNotFound(err) {
				writeError(w, internalerrors.New(internalerrors.ErrAuthFailed, "invalid admin session", nil), http.StatusUnauthorized)
				return
			}
			writeError(w, internalerrors.New(internalerrors.ErrDatabaseError, "get admin session", err), http.StatusInternalServerError)
			return
		}
		if !sess.ExpiresAt.After(now) {
			writeError(w, internalerrors.New(internalerrors.ErrAuthFailed, "admin session expired", nil), http.StatusUnauthorized)
			return
		}

		user, err := m.users.GetByID(r.Context(), sess.AdminUserID)
		if err != nil {
			if repository.IsNotFound(err) {
				writeError(w, internalerrors.New(internalerrors.ErrAuthFailed, "admin user not found", nil), http.StatusUnauthorized)
				return
			}
			writeError(w, internalerrors.New(internalerrors.ErrDatabaseError, "get admin user", err), http.StatusInternalServerError)
			return
		}

		ctx := context.WithValue(r.Context(), ContextAdminUser, user)
		*r = *r.WithContext(ctx)
		next.ServeHTTP(w, r)
	})
}

func AdminUserFromContext(ctx context.Context) (models.AdminUser, bool) {
	if ctx == nil {
		return models.AdminUser{}, false
	}
	v := ctx.Value(ContextAdminUser)
	user, ok := v.(models.AdminUser)
	return user, ok
}

func isPublicAdminPath(path string) bool {
	p := strings.TrimSpace(path)
	if p == "/admin/login" || p == "/admin/bootstrap" || p == "/admin/reset-request" || p == "/admin/reset" {
		return true
	}
	if strings.HasPrefix(p, "/admin/reset/") {
		return true
	}
	return false
}

func readCookieToken(r *http.Request, cookieName string) (string, bool) {
	if r == nil {
		return "", false
	}
	c, err := r.Cookie(cookieName)
	if err != nil {
		return "", false
	}
	val := strings.TrimSpace(c.Value)
	if val == "" {
		return "", false
	}
	return val, true
}

func sha256Hex(v []byte) string {
	s := sha256.Sum256(v)
	return hex.EncodeToString(s[:])
}

func writeError(w http.ResponseWriter, err error, status int) {
	code := internalerrors.GetCode(err)
	if code == "" {
		code = internalerrors.ErrUnknown
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": err.Error(),
		},
	})
}
