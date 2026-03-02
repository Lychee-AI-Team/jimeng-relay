package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jimeng-relay/server/internal/models"
	"github.com/jimeng-relay/server/internal/repository"
	"golang.org/x/crypto/bcrypt"
)

type testResetUserRepo struct {
	usersByID    map[string]models.AdminUser
	usersByEmail map[string]string
}

func (r *testResetUserRepo) Create(_ context.Context, _ models.AdminUser) error {
	return nil
}

func (r *testResetUserRepo) GetByID(_ context.Context, id string) (models.AdminUser, error) {
	u, ok := r.usersByID[id]
	if !ok {
		return models.AdminUser{}, repository.ErrNotFound
	}
	return u, nil
}

func (r *testResetUserRepo) GetByEmail(_ context.Context, email string) (models.AdminUser, error) {
	id, ok := r.usersByEmail[email]
	if !ok {
		return models.AdminUser{}, repository.ErrNotFound
	}
	u, ok := r.usersByID[id]
	if !ok {
		return models.AdminUser{}, repository.ErrNotFound
	}
	return u, nil
}

func (r *testResetUserRepo) Update(_ context.Context, user models.AdminUser) error {
	if r.usersByID == nil {
		r.usersByID = make(map[string]models.AdminUser)
	}
	r.usersByID[user.ID] = user
	return nil
}

type testResetTokenRepo struct {
	byID   map[string]models.PasswordResetToken
	byHash map[string]string
	marked []string
	create int
}

func (r *testResetTokenRepo) Create(_ context.Context, token models.PasswordResetToken) error {
	if r.byID == nil {
		r.byID = make(map[string]models.PasswordResetToken)
	}
	if r.byHash == nil {
		r.byHash = make(map[string]string)
	}
	r.byID[token.ID] = token
	r.byHash[token.TokenHash] = token.ID
	r.create++
	return nil
}

func (r *testResetTokenRepo) GetByTokenHash(_ context.Context, tokenHash string) (models.PasswordResetToken, error) {
	id, ok := r.byHash[tokenHash]
	if !ok {
		return models.PasswordResetToken{}, repository.ErrNotFound
	}
	tok, ok := r.byID[id]
	if !ok {
		return models.PasswordResetToken{}, repository.ErrNotFound
	}
	return tok, nil
}

func (r *testResetTokenRepo) MarkUsed(_ context.Context, id string, usedAt time.Time) error {
	tok, ok := r.byID[id]
	if !ok {
		return repository.ErrNotFound
	}
	tok.UsedAt = &usedAt
	r.byID[id] = tok
	r.marked = append(r.marked, id)
	return nil
}

func (r *testResetTokenRepo) DeleteExpired(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

type testResetEmailProvider struct {
	requests []struct {
		to   string
		link string
	}
}

func (p *testResetEmailProvider) SendPasswordReset(_ context.Context, toEmail, resetLink string) error {
	p.requests = append(p.requests, struct {
		to   string
		link string
	}{to: toEmail, link: resetLink})
	return nil
}

func TestResetHandlerRequestResponseIsSameForExistingAndMissingEmail(t *testing.T) {
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name            string
		email           string
		usersByID       map[string]models.AdminUser
		usersByEmail    map[string]string
		wantTokenCreate int
		wantEmails      int
	}{
		{
			name:  "existing email",
			email: "root@example.com",
			usersByID: map[string]models.AdminUser{
				"admin_1": {
					ID:    "admin_1",
					Email: "root@example.com",
				},
			},
			usersByEmail:    map[string]string{"root@example.com": "admin_1"},
			wantTokenCreate: 1,
			wantEmails:      1,
		},
		{
			name:            "missing email",
			email:           "missing@example.com",
			usersByID:       map[string]models.AdminUser{},
			usersByEmail:    map[string]string{},
			wantTokenCreate: 0,
			wantEmails:      0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			users := &testResetUserRepo{usersByID: tc.usersByID, usersByEmail: tc.usersByEmail}
			tokens := &testResetTokenRepo{}
			emails := &testResetEmailProvider{}
			h := NewResetHandler(users, tokens, emails, nil, ResetHandlerConfig{Now: func() time.Time { return now }})

			req := httptest.NewRequest(http.MethodPost, "/admin/reset-request", strings.NewReader(`{"email":"`+tc.email+`"}`))
			res := httptest.NewRecorder()

			h.Routes().ServeHTTP(res, req)

			if res.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d", res.Code)
			}

			var body map[string]any
			if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body["message"] != requestResponseText {
				t.Fatalf("expected message %q, got %v", requestResponseText, body["message"])
			}

			if tokens.create != tc.wantTokenCreate {
				t.Fatalf("expected token create count %d, got %d", tc.wantTokenCreate, tokens.create)
			}
			if len(emails.requests) != tc.wantEmails {
				t.Fatalf("expected email send count %d, got %d", tc.wantEmails, len(emails.requests))
			}
		})
	}
}

func TestResetHandlerTokenSingleUse(t *testing.T) {
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	oldHash, err := bcrypt.GenerateFromPassword([]byte("OldPassword123"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("generate old password hash: %v", err)
	}

	plaintext := "single-use-token"
	tokenHash := hashResetToken(plaintext)
	token := models.PasswordResetToken{
		ID:          "prt_1",
		AdminUserID: "admin_1",
		TokenHash:   tokenHash,
		CreatedAt:   now.Add(-time.Minute),
		ExpiresAt:   now.Add(10 * time.Minute),
	}

	users := &testResetUserRepo{
		usersByID: map[string]models.AdminUser{
			"admin_1": {
				ID:           "admin_1",
				Email:        "root@example.com",
				PasswordHash: string(oldHash),
				CreatedAt:    now.Add(-time.Hour),
				UpdatedAt:    now.Add(-time.Hour),
			},
		},
		usersByEmail: map[string]string{"root@example.com": "admin_1"},
	}
	tokens := &testResetTokenRepo{
		byID:   map[string]models.PasswordResetToken{token.ID: token},
		byHash: map[string]string{tokenHash: token.ID},
	}
	h := NewResetHandler(users, tokens, &testResetEmailProvider{}, nil, ResetHandlerConfig{Now: func() time.Time { return now }, BcryptCost: bcrypt.MinCost})

	steps := []struct {
		name       string
		wantStatus int
		wantBody   string
	}{
		{name: "first use succeeds", wantStatus: http.StatusOK, wantBody: resetDoneResponseText},
		{name: "second use rejected", wantStatus: http.StatusBadRequest, wantBody: invalidTokenText},
	}

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/admin/reset", strings.NewReader(`{"token":"`+plaintext+`","new_password":"NewPassword123"}`))
			res := httptest.NewRecorder()

			h.Routes().ServeHTTP(res, req)

			if res.Code != step.wantStatus {
				t.Fatalf("expected status %d, got %d", step.wantStatus, res.Code)
			}

			var body map[string]any
			if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if msg, ok := body["message"]; ok {
				if msg != step.wantBody {
					t.Fatalf("expected message %q, got %v", step.wantBody, msg)
				}
			} else if errMsg, ok := body["error"]; ok {
				if errMsg != step.wantBody {
					t.Fatalf("expected error %q, got %v", step.wantBody, errMsg)
				}
			} else {
				t.Fatalf("expected response message or error")
			}
		})
	}

	if len(tokens.marked) != 1 {
		t.Fatalf("expected token marked used once, got %d", len(tokens.marked))
	}

	updated, ok := users.usersByID["admin_1"]
	if !ok {
		t.Fatalf("expected updated admin user")
	}
	if bcrypt.CompareHashAndPassword([]byte(updated.PasswordHash), []byte("NewPassword123")) != nil {
		t.Fatalf("expected password to be updated")
	}
}

func TestBuildResetLinkIncludesToken(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		token   string
	}{
		{name: "empty base url", baseURL: "", token: "abc123"},
		{name: "valid base url", baseURL: "http://localhost:8080/admin/reset", token: "abc123"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			link := buildResetLink(tc.baseURL, tc.token)
			u, err := url.Parse(link)
			if err != nil {
				t.Fatalf("parse link: %v", err)
			}
			if u.Query().Get("token") != tc.token {
				t.Fatalf("expected token %q in reset link, got %q", tc.token, u.Query().Get("token"))
			}
		})
	}
}
