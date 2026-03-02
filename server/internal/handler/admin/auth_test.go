package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jimeng-relay/server/internal/models"
	"github.com/jimeng-relay/server/internal/repository"
	"golang.org/x/crypto/bcrypt"
)

type testAuthUserRepo struct {
	usersByEmail map[string]models.AdminUser
}

func (r *testAuthUserRepo) Create(_ context.Context, _ models.AdminUser) error {
	return nil
}

func (r *testAuthUserRepo) GetByID(_ context.Context, _ string) (models.AdminUser, error) {
	return models.AdminUser{}, repository.ErrNotFound
}

func (r *testAuthUserRepo) GetByEmail(_ context.Context, email string) (models.AdminUser, error) {
	u, ok := r.usersByEmail[email]
	if !ok {
		return models.AdminUser{}, repository.ErrNotFound
	}
	return u, nil
}

func (r *testAuthUserRepo) Update(_ context.Context, _ models.AdminUser) error {
	return nil
}

type testAuthSessionRepo struct {
	created []models.AdminSession
	byHash  map[string]models.AdminSession
	deleted []string
}

func (r *testAuthSessionRepo) Create(_ context.Context, session models.AdminSession) error {
	r.created = append(r.created, session)
	if r.byHash == nil {
		r.byHash = make(map[string]models.AdminSession)
	}
	r.byHash[session.TokenHash] = session
	return nil
}

func (r *testAuthSessionRepo) GetByTokenHash(_ context.Context, tokenHash string) (models.AdminSession, error) {
	if r.byHash == nil {
		return models.AdminSession{}, repository.ErrNotFound
	}
	s, ok := r.byHash[tokenHash]
	if !ok {
		return models.AdminSession{}, repository.ErrNotFound
	}
	return s, nil
}

func (r *testAuthSessionRepo) Delete(_ context.Context, id string) error {
	r.deleted = append(r.deleted, id)
	return nil
}

func (r *testAuthSessionRepo) DeleteExpired(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

func TestAuthHandlerLogin(t *testing.T) {
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	hash, err := bcrypt.GenerateFromPassword([]byte("Password123"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("generate password hash: %v", err)
	}

	tests := []struct {
		name           string
		email          string
		password       string
		usersByEmail   map[string]models.AdminUser
		wantStatusCode int
		wantCreateSess bool
		wantCookie     bool
	}{
		{
			name:     "success with valid credentials",
			email:    "root@example.com",
			password: "Password123",
			usersByEmail: map[string]models.AdminUser{
				"root@example.com": {
					ID:           "admin_1",
					Email:        "root@example.com",
					PasswordHash: string(hash),
				},
			},
			wantStatusCode: http.StatusOK,
			wantCreateSess: true,
			wantCookie:     true,
		},
		{
			name:           "failure with invalid credentials",
			email:          "root@example.com",
			password:       "wrong-password",
			usersByEmail:   map[string]models.AdminUser{},
			wantStatusCode: http.StatusUnauthorized,
			wantCreateSess: false,
			wantCookie:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			userRepo := &testAuthUserRepo{usersByEmail: tc.usersByEmail}
			sessionRepo := &testAuthSessionRepo{}
			h := NewAuthHandler(userRepo, sessionRepo, AuthConfig{Now: func() time.Time { return now }, SessionTTL: 2 * time.Hour})

			reqBody := `{"email":"` + tc.email + `","password":"` + tc.password + `"}`
			req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(reqBody))
			res := httptest.NewRecorder()

			h.Login(res, req)

			if res.Code != tc.wantStatusCode {
				t.Fatalf("expected status %d, got %d", tc.wantStatusCode, res.Code)
			}

			if got := len(sessionRepo.created) > 0; got != tc.wantCreateSess {
				t.Fatalf("expected session create %v, got %v", tc.wantCreateSess, got)
			}

			cookies := res.Result().Cookies()
			if got := len(cookies) > 0; got != tc.wantCookie {
				t.Fatalf("expected cookie set %v, got %v", tc.wantCookie, got)
			}

			if tc.wantCookie {
				c := cookies[0]
				if c.Name != defaultSessionCookieName {
					t.Fatalf("expected cookie %q, got %q", defaultSessionCookieName, c.Name)
				}
				if !c.HttpOnly || !c.Secure || c.Path != "/admin" || c.SameSite != http.SameSiteStrictMode {
					t.Fatalf("unexpected cookie flags: %+v", c)
				}

				var body map[string]any
				if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if body["status"] != "ok" {
					t.Fatalf("expected status ok in response, got %v", body["status"])
				}
			}
		})
	}
}

func TestAuthHandlerLogout(t *testing.T) {
	tests := []struct {
		name             string
		cookieValue      string
		seedSession      *models.AdminSession
		wantDeleteCalled bool
	}{
		{
			name:             "clears cookie and deletes session when token exists",
			cookieValue:      "session-token-1",
			seedSession:      &models.AdminSession{ID: "asess_1", TokenHash: sha256Hex([]byte("session-token-1"))},
			wantDeleteCalled: true,
		},
		{
			name:             "clears cookie without server session",
			cookieValue:      "",
			seedSession:      nil,
			wantDeleteCalled: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sessionRepo := &testAuthSessionRepo{}
			if tc.seedSession != nil {
				sessionRepo.byHash = map[string]models.AdminSession{tc.seedSession.TokenHash: *tc.seedSession}
			}
			h := NewAuthHandler(nil, sessionRepo, AuthConfig{})

			req := httptest.NewRequest(http.MethodPost, "/admin/logout", nil)
			if tc.cookieValue != "" {
				req.AddCookie(&http.Cookie{Name: defaultSessionCookieName, Value: tc.cookieValue})
			}
			res := httptest.NewRecorder()

			h.Logout(res, req)

			if res.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d", res.Code)
			}

			if got := len(sessionRepo.deleted) > 0; got != tc.wantDeleteCalled {
				t.Fatalf("expected delete called %v, got %v", tc.wantDeleteCalled, got)
			}

			cookies := res.Result().Cookies()
			if len(cookies) == 0 {
				t.Fatalf("expected cleared session cookie")
			}
			cleared := cookies[0]
			if cleared.Name != defaultSessionCookieName || cleared.MaxAge != -1 || cleared.Value != "" {
				t.Fatalf("expected cleared cookie, got %+v", cleared)
			}
		})
	}
}
