package admin

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jimeng-relay/server/internal/config"
	internalerrors "github.com/jimeng-relay/server/internal/errors"
	adminmiddleware "github.com/jimeng-relay/server/internal/middleware/admin"
	"github.com/jimeng-relay/server/internal/repository"
	"github.com/jimeng-relay/server/internal/repository/postgres"
	"github.com/jimeng-relay/server/internal/repository/sqlite"
	"github.com/jimeng-relay/server/internal/secretcrypto"
	apikeyservice "github.com/jimeng-relay/server/internal/service/apikey"
)

const maxCreateKeyBodyBytes int64 = 1 << 20

func CreateKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, internalerrors.New(internalerrors.ErrValidationFailed, "method not allowed", nil), http.StatusMethodNotAllowed)
		return
	}

	adminUser, ok := adminmiddleware.AdminUserFromContext(r.Context())
	if !ok {
		writeError(w, internalerrors.New(internalerrors.ErrAuthFailed, "admin authentication required", nil), http.StatusUnauthorized)
		return
	}
	_ = adminUser

	_ = config.LoadEnvFile(".env")

	dbType := strings.ToLower(strings.TrimSpace(os.Getenv(config.EnvDatabaseType)))
	if dbType == "" {
		dbType = config.DefaultDatabaseType
	}
	dbURL := strings.TrimSpace(os.Getenv(config.EnvDatabaseURL))
	if dbURL == "" {
		dbURL = config.DefaultDatabaseURL
	}

	encodedKey := strings.TrimSpace(os.Getenv(config.EnvAPIKeyEncryptionKey))
	if encodedKey == "" {
		writeError(w, internalerrors.New(internalerrors.ErrInternalError, config.EnvAPIKeyEncryptionKey+" is required", nil), http.StatusInternalServerError)
		return
	}
	rawKey, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil {
		writeError(w, internalerrors.New(internalerrors.ErrInternalError, "decode api key encryption key", err), http.StatusInternalServerError)
		return
	}
	secretCipher, err := secretcrypto.NewAESCipher(rawKey)
	if err != nil {
		writeError(w, internalerrors.New(internalerrors.ErrInternalError, "init api key secret cipher", err), http.StatusInternalServerError)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxCreateKeyBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var payload struct {
		Description string     `json:"description"`
		ExpiresAt   *time.Time `json:"expires_at"`
	}
	if err := dec.Decode(&payload); err != nil {
		if !errors.Is(err, io.EOF) {
			writeError(w, internalerrors.New(internalerrors.ErrValidationFailed, "invalid json body", err), http.StatusBadRequest)
			return
		}
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, internalerrors.New(internalerrors.ErrValidationFailed, "invalid json body", nil), http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	var apiKeysRepo repository.APIKeyRepository
	var cleanup func()
	switch dbType {
	case "", "sqlite":
		repos, err := sqlite.Open(ctx, dbURL)
		if err != nil {
			writeError(w, internalerrors.New(internalerrors.ErrDatabaseError, "open sqlite repository", err), http.StatusInternalServerError)
			return
		}
		cleanup = func() { _ = repos.Close() }
		apiKeysRepo = repos.APIKeys
	case "postgres", "postgresql":
		db, err := postgres.Open(ctx, dbURL)
		if err != nil {
			writeError(w, internalerrors.New(internalerrors.ErrDatabaseError, "open postgres repository", err), http.StatusInternalServerError)
			return
		}
		cleanup = db.Close
		apiKeysRepo = db.APIKeys()
	default:
		writeError(w, internalerrors.New(internalerrors.ErrInternalError, "unsupported database type: "+dbType, nil), http.StatusInternalServerError)
		return
	}
	defer cleanup()

	svc := apikeyservice.NewService(apiKeysRepo, apikeyservice.Config{SecretCipher: secretCipher})
	created, err := svc.Create(ctx, apikeyservice.CreateRequest{Description: strings.TrimSpace(payload.Description), ExpiresAt: payload.ExpiresAt})
	if err != nil {
		writeError(w, err, statusFromError(err))
		return
	}
	writeJSON(w, http.StatusCreated, created)
}
