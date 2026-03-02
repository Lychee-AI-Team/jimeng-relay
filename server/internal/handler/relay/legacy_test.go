package relay

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jimeng-relay/server/internal/middleware/sigv4"
	"github.com/jimeng-relay/server/internal/models"
	"github.com/jimeng-relay/server/internal/relay/upstream"
	"github.com/jimeng-relay/server/internal/repository"
	"github.com/jimeng-relay/server/internal/secretcrypto"
)

type memoryAPIKeyRepo struct {
	keys map[string]models.APIKey
}

func (m *memoryAPIKeyRepo) Create(_ context.Context, key models.APIKey) error {
	if m.keys == nil {
		m.keys = map[string]models.APIKey{}
	}
	m.keys[key.ID] = key
	return nil
}

func (m *memoryAPIKeyRepo) GetByID(_ context.Context, id string) (models.APIKey, error) {
	key, ok := m.keys[id]
	if !ok {
		return models.APIKey{}, repository.ErrNotFound
	}
	return key, nil
}

func (m *memoryAPIKeyRepo) GetByAccessKey(_ context.Context, accessKey string) (models.APIKey, error) {
	for _, key := range m.keys {
		if key.AccessKey == accessKey {
			return key, nil
		}
	}
	return models.APIKey{}, repository.ErrNotFound
}

func (m *memoryAPIKeyRepo) List(_ context.Context) ([]models.APIKey, error) {
	out := make([]models.APIKey, 0, len(m.keys))
	for _, k := range m.keys {
		out = append(out, k)
	}
	return out, nil
}

func (m *memoryAPIKeyRepo) Revoke(_ context.Context, id string, revokedAt time.Time) error {
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

func (m *memoryAPIKeyRepo) SetExpired(_ context.Context, id string, expiredAt time.Time) error {
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

func (m *memoryAPIKeyRepo) SetExpiresAt(_ context.Context, id string, expiresAt time.Time) error {
	key, ok := m.keys[id]
	if !ok {
		return repository.ErrNotFound
	}
	key.ExpiresAt = &expiresAt
	key.UpdatedAt = expiresAt
	m.keys[id] = key
	return nil
}

func TestLegacyCompatibility_SigV4AndRelayRoutes(t *testing.T) {
	now := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	c := mustTestCipher(t)

	secret := "sk_test_secret"
	ct, err := c.Encrypt(secret)
	if err != nil {
		t.Fatalf("Encrypt secret: %v", err)
	}
	key := models.APIKey{
		ID:                  "key_1",
		AccessKey:           "ak_test",
		SecretKeyHash:       "bcrypt-placeholder",
		SecretKeyCiphertext: ct,
		Status:              models.APIKeyStatusActive,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	repo := &memoryAPIKeyRepo{keys: map[string]models.APIKey{key.ID: key}}
	authn := sigv4.New(repo, sigv4.Config{
		Now:             func() time.Time { return now },
		SecretCipher:    c,
		ExpectedRegion:  "cn-north-1",
		ExpectedService: "cv",
	})

	tests := []struct {
		name         string
		url          string
		body         []byte
		assertSubmit bool
	}{
		{name: "v1 submit", url: "http://relay.local/v1/submit", body: []byte(`{"prompt":"cat"}`), assertSubmit: true},
		{name: "v1 get-result", url: "http://relay.local/v1/get-result", body: []byte(`{"task_id":"t1"}`)},
		{name: "compatible submit action", url: "http://relay.local/?Action=CVSync2AsyncSubmitTask&Version=2022-08-31", body: []byte(`{"prompt":"cat"}`), assertSubmit: true},
		{name: "compatible get-result action", url: "http://relay.local/?Action=CVSync2AsyncGetResult&Version=2022-08-31", body: []byte(`{"task_id":"t1"}`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auditSvc, _, _, _ := newTestAuditService(t, nil, nil, nil)
			submitClient := &fakeSubmitClient{resp: &upstream.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"code":10000,"message":"ok"}`)}}
			getResultClient := &fakeGetResultClient{resp: &upstream.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"code":10000,"message":"ok"}`)}}

			submitRoutes := NewSubmitHandler(submitClient, auditSvc, &mockBillingService{}, nil, nil, nil).Routes()
			getResultRoutes := NewGetResultHandler(getResultClient, auditSvc, nil, nil).Routes()

			app := http.NewServeMux()
			app.Handle("/v1/submit", submitRoutes)
			app.Handle("/v1/get-result", getResultRoutes)
			app.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Query().Get("Action") {
				case "CVSync2AsyncSubmitTask":
					submitRoutes.ServeHTTP(w, r)
				case "CVSync2AsyncGetResult":
					getResultRoutes.ServeHTTP(w, r)
				default:
					http.NotFound(w, r)
				}
			})

			h := authn(app)
			req := newAWS4SignedRequest(t, http.MethodPost, tt.url, tt.body, key.AccessKey, secret, now)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			if tt.assertSubmit {
				if submitClient.apiKeyID != key.ID {
					t.Fatalf("expected submit handler to receive api key id %q, got %q", key.ID, submitClient.apiKeyID)
				}
			} else {
				if got := upstream.GetAPIKeyID(getResultClient.ctx); got != key.ID {
					t.Fatalf("expected get-result handler to receive api key id %q, got %q", key.ID, got)
				}
			}
		})
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

func newAWS4SignedRequest(t *testing.T, method, target string, body []byte, accessKey, secret string, ts time.Time) *http.Request {
	t.Helper()
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	req := httptest.NewRequest(method, parsed.String(), bytes.NewReader(body))
	req.Host = parsed.Host

	date := ts.UTC().Format("20060102T150405Z")
	dateShort := ts.UTC().Format("20060102")
	region := "cn-north-1"
	service := "cv"

	payloadHash := sha256Hex(body)
	req.Header.Set("X-Date", date)
	req.Header.Set("X-Content-Sha256", payloadHash)

	signedHeaders := []string{"host", "x-content-sha256", "x-date"}
	canon, err := buildCanonicalRequestForTest(req, signedHeaders, payloadHash)
	if err != nil {
		t.Fatalf("build canonical request: %v", err)
	}

	scope := dateShort + "/" + region + "/" + service + "/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		date,
		scope,
		sha256Hex([]byte(canon)),
	}, "\n")

	signingKey := deriveSigningKeyForTest(secret, dateShort, region, service)
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKey+"/"+scope+", SignedHeaders="+strings.Join(signedHeaders, ";")+", Signature="+signature)
	return req
}

func buildCanonicalRequestForTest(r *http.Request, signedHeaders []string, payloadHash string) (string, error) {
	headers := append([]string(nil), signedHeaders...)
	sort.Strings(headers)

	canonHeaders := strings.Builder{}
	for _, h := range headers {
		v := canonicalHeaderValueForTest(r, h)
		canonHeaders.WriteString(h)
		canonHeaders.WriteByte(':')
		canonHeaders.WriteString(v)
		canonHeaders.WriteByte('\n')
	}

	return strings.Join([]string{
		r.Method,
		canonicalURIForTest(r.URL.Path),
		canonicalQueryStringForTest(r.URL.Query()),
		canonHeaders.String(),
		strings.Join(headers, ";"),
		payloadHash,
	}, "\n"), nil
}

func canonicalHeaderValueForTest(r *http.Request, name string) string {
	if name == "host" {
		return strings.TrimSpace(strings.ToLower(r.Host))
	}
	vals := r.Header.Values(name)
	for i := range vals {
		vals[i] = strings.Join(strings.Fields(vals[i]), " ")
	}
	return strings.TrimSpace(strings.Join(vals, ","))
}

func canonicalURIForTest(path string) string {
	if path == "" {
		return "/"
	}
	parts := strings.Split(path, "/")
	for i := range parts {
		parts[i] = awsEscapeForTest(parts[i])
	}
	uri := strings.Join(parts, "/")
	if !strings.HasPrefix(uri, "/") {
		uri = "/" + uri
	}
	return uri
}

func canonicalQueryStringForTest(values url.Values) string {
	if len(values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	pairs := make([]string, 0)
	for _, k := range keys {
		vals := append([]string(nil), values[k]...)
		sort.Strings(vals)
		ek := awsEscapeForTest(k)
		for _, v := range vals {
			pairs = append(pairs, ek+"="+awsEscapeForTest(v))
		}
	}
	return strings.Join(pairs, "&")
}

func awsEscapeForTest(s string) string {
	e := url.QueryEscape(s)
	e = strings.ReplaceAll(e, "+", "%20")
	e = strings.ReplaceAll(e, "*", "%2A")
	e = strings.ReplaceAll(e, "%7E", "~")
	return e
}

func deriveSigningKeyForTest(secret, date, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), date)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}

func hmacSHA256(key []byte, message string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(message))
	return h.Sum(nil)
}

func sha256Hex(v []byte) string {
	s := sha256.Sum256(v)
	return hex.EncodeToString(s[:])
}
