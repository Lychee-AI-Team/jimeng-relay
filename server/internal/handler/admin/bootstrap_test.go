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
	adminservice "github.com/jimeng-relay/server/internal/service/admin"
)

type testAdminUserRepo struct {
	user *models.AdminUser
}

func (r *testAdminUserRepo) Create(_ context.Context, user models.AdminUser) error {
	if r.user != nil {
		return repository.ErrNotFound
	}
	u := user
	r.user = &u
	return nil
}

func (r *testAdminUserRepo) GetByID(_ context.Context, _ string) (models.AdminUser, error) {
	if r.user == nil {
		return models.AdminUser{}, repository.ErrNotFound
	}
	return *r.user, nil
}

func (r *testAdminUserRepo) GetByEmail(_ context.Context, _ string) (models.AdminUser, error) {
	if r.user == nil {
		return models.AdminUser{}, repository.ErrNotFound
	}
	return *r.user, nil
}

func (r *testAdminUserRepo) Update(_ context.Context, user models.AdminUser) error {
	u := user
	r.user = &u
	return nil
}

type testAdminSessionRepo struct{}

func (r *testAdminSessionRepo) Create(_ context.Context, _ models.AdminSession) error { return nil }
func (r *testAdminSessionRepo) GetByTokenHash(_ context.Context, _ string) (models.AdminSession, error) {
	return models.AdminSession{}, repository.ErrNotFound
}
func (r *testAdminSessionRepo) Delete(_ context.Context, _ string) error { return nil }
func (r *testAdminSessionRepo) DeleteExpired(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

func TestBootstrapHandlerSetsSessionCookie(t *testing.T) {
	users := &testAdminUserRepo{}
	sessions := &testAdminSessionRepo{}
	svc := adminservice.NewService(users, sessions, adminservice.Config{})
	h := NewBootstrapHandler(svc, nil)

	req := httptest.NewRequest(http.MethodPost, "/admin/bootstrap", strings.NewReader(`{"email":"root@example.com","password":"Password123"}`))
	res := httptest.NewRecorder()
	h.Routes().ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d", res.Code)
	}

	cookies := res.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatalf("expected session cookie")
	}
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
	if _, ok := body["admin_user"]; !ok {
		t.Fatalf("expected admin_user in response")
	}
}

func TestBootstrapHandlerLockedReturns409(t *testing.T) {
	users := &testAdminUserRepo{}
	first := models.AdminUser{ID: "admin_bootstrap_root", Email: "root@example.com", PasswordHash: "hash"}
	users.user = &first
	svc := adminservice.NewService(users, &testAdminSessionRepo{}, adminservice.Config{})
	h := NewBootstrapHandler(svc, nil)

	req := httptest.NewRequest(http.MethodPost, "/admin/bootstrap", strings.NewReader(`{"email":"root@example.com","password":"Password123"}`))
	res := httptest.NewRecorder()
	h.Routes().ServeHTTP(res, req)

	if res.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d", res.Code)
	}
}
