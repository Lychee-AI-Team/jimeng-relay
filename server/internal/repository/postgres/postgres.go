package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	internalerrors "github.com/jimeng-relay/server/internal/errors"
	"github.com/jimeng-relay/server/internal/models"
	"github.com/jimeng-relay/server/internal/repository"
)

type DB struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*DB, error) {
	databaseURL = strings.TrimSpace(databaseURL)
	if databaseURL == "" {
		return nil, internalerrors.New(internalerrors.ErrValidationFailed, "databaseURL is required", nil)
	}

	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, internalerrors.New(internalerrors.ErrDatabaseError, "parse DATABASE_URL", err)
	}
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeCacheStatement
	cfg.ConnConfig.StatementCacheCapacity = 512

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, internalerrors.New(internalerrors.ErrDatabaseError, "create postgres pool", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, internalerrors.New(internalerrors.ErrDatabaseError, "ping postgres", err)
	}

	if err := Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}

	return &DB{pool: pool}, nil
}

func OpenFromEnv(ctx context.Context) (*DB, error) {
	dbURL, ok := os.LookupEnv("DATABASE_URL")
	if !ok {
		return nil, internalerrors.New(internalerrors.ErrValidationFailed, "DATABASE_URL is required", nil)
	}
	return Open(ctx, dbURL)
}

func (db *DB) Close() {
	if db == nil || db.pool == nil {
		return
	}
	db.pool.Close()
}

func (db *DB) APIKeys() repository.APIKeyRepository {
	return &apiKeyRepository{pool: db.pool}
}

func (db *DB) DownstreamRequests() repository.DownstreamRequestRepository {
	return &downstreamRequestRepository{pool: db.pool}
}

func (db *DB) UpstreamAttempts() repository.UpstreamAttemptRepository {
	return &upstreamAttemptRepository{pool: db.pool}
}

func (db *DB) AuditEvents() repository.AuditEventRepository {
	return &auditEventRepository{pool: db.pool}
}

func (db *DB) IdempotencyRecords() repository.IdempotencyRecordRepository {
	return &idempotencyRecordRepository{pool: db.pool}
}

func (db *DB) AdminUsers() repository.AdminUserRepository {
	return &adminUserRepository{pool: db.pool}
}

func (db *DB) AdminSessions() repository.AdminSessionRepository {
	return &adminSessionRepository{pool: db.pool}
}

func (db *DB) PasswordResetTokens() repository.PasswordResetTokenRepository {
	return &passwordResetTokenRepository{pool: db.pool}
}

type apiKeyRepository struct {
	pool *pgxpool.Pool
}

func (r *apiKeyRepository) Create(ctx context.Context, key models.APIKey) error {
	if err := key.Validate(); err != nil {
		return internalerrors.New(internalerrors.ErrValidationFailed, "validate api key", err)
	}

	var expiresAt any
	if key.ExpiresAt != nil {
		expiresAt = key.ExpiresAt.UTC()
	}
	var revokedAt any
	if key.RevokedAt != nil {
		revokedAt = key.RevokedAt.UTC()
	}

	_, err := r.pool.Exec(ctx, `INSERT INTO api_keys (
		id, access_key, secret_key_hash, secret_key_ciphertext, description, created_at, updated_at, expires_at, revoked_at, rotation_of, status
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		key.ID,
		key.AccessKey,
		key.SecretKeyHash,
		key.SecretKeyCiphertext,
		key.Description,
		key.CreatedAt.UTC(),
		key.UpdatedAt.UTC(),
		expiresAt,
		revokedAt,
		key.RotationOf,
		string(key.Status),
	)
	if err != nil {
		return internalerrors.New(internalerrors.ErrDatabaseError, "insert api key", err)
	}
	return nil
}

func (r *apiKeyRepository) GetByAccessKey(ctx context.Context, accessKey string) (models.APIKey, error) {
	accessKey = strings.TrimSpace(accessKey)
	if accessKey == "" {
		return models.APIKey{}, internalerrors.New(internalerrors.ErrValidationFailed, "accessKey is required", nil)
	}
	return r.getOne(ctx, `SELECT id, access_key, secret_key_hash, secret_key_ciphertext, description, multiplier, created_at, updated_at, expires_at, revoked_at, rotation_of, status
		FROM api_keys WHERE access_key = $1`, accessKey)
}

func (r *apiKeyRepository) GetByID(ctx context.Context, id string) (models.APIKey, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return models.APIKey{}, internalerrors.New(internalerrors.ErrValidationFailed, "id is required", nil)
	}
	return r.getOne(ctx, `SELECT id, access_key, secret_key_hash, secret_key_ciphertext, description, multiplier, created_at, updated_at, expires_at, revoked_at, rotation_of, status
		FROM api_keys WHERE id = $1`, id)
}

func (r *apiKeyRepository) getOne(ctx context.Context, query string, arg any) (models.APIKey, error) {
	var key models.APIKey
	var expiresAt *time.Time
	var revokedAt *time.Time
	var rotationOf *string
	var status string
	row := r.pool.QueryRow(ctx, query, arg)
	if err := row.Scan(
		&key.ID,
		&key.AccessKey,
		&key.SecretKeyHash,
		&key.SecretKeyCiphertext,
		&key.Description,
		&key.Multiplier,
		&key.CreatedAt,
		&key.UpdatedAt,
		&expiresAt,
		&revokedAt,
		&rotationOf,
		&status,
	); err != nil {
		if err == pgx.ErrNoRows {
			return models.APIKey{}, repository.ErrNotFound
		}
		return models.APIKey{}, internalerrors.New(internalerrors.ErrDatabaseError, "select api key by access_key", err)
	}
	key.ExpiresAt = expiresAt
	key.RevokedAt = revokedAt
	key.RotationOf = rotationOf
	key.Status = models.APIKeyStatus(status)
	return key, nil
}

func (r *apiKeyRepository) List(ctx context.Context) ([]models.APIKey, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, access_key, secret_key_hash, secret_key_ciphertext, description, multiplier, created_at, updated_at, expires_at, revoked_at, rotation_of, status
		FROM api_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, internalerrors.New(internalerrors.ErrDatabaseError, "list api keys", err)
	}
	defer rows.Close()

	keys := make([]models.APIKey, 0)
	for rows.Next() {
		var key models.APIKey
		var expiresAt *time.Time
		var revokedAt *time.Time
		var rotationOf *string
		var status string
		if err := rows.Scan(
			&key.ID,
			&key.AccessKey,
			&key.SecretKeyHash,
			&key.SecretKeyCiphertext,
			&key.Description,
			&key.Multiplier,
			&key.CreatedAt,
			&key.UpdatedAt,
			&expiresAt,
			&revokedAt,
			&rotationOf,
			&status,
		); err != nil {
			return nil, internalerrors.New(internalerrors.ErrDatabaseError, "scan api key", err)
		}
		key.ExpiresAt = expiresAt
		key.RevokedAt = revokedAt
		key.RotationOf = rotationOf
		key.Status = models.APIKeyStatus(status)
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, internalerrors.New(internalerrors.ErrDatabaseError, "iterate api keys", err)
	}
	return keys, nil
}

func (r *apiKeyRepository) Revoke(ctx context.Context, id string, revokedAt time.Time) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return internalerrors.New(internalerrors.ErrValidationFailed, "id is required", nil)
	}
	if revokedAt.IsZero() {
		return internalerrors.New(internalerrors.ErrValidationFailed, "revokedAt is required", nil)
	}

	var returnedID string
	row := r.pool.QueryRow(ctx, `UPDATE api_keys
		SET revoked_at = $2, status = $3, updated_at = $2
		WHERE id = $1
		RETURNING id`, id, revokedAt.UTC(), string(models.APIKeyStatusRevoked))
	if err := row.Scan(&returnedID); err != nil {
		if err == pgx.ErrNoRows {
			return repository.ErrNotFound
		}
		return internalerrors.New(internalerrors.ErrDatabaseError, "revoke api key", err)
	}
	return nil
}

func (r *apiKeyRepository) SetExpired(ctx context.Context, id string, expiredAt time.Time) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return internalerrors.New(internalerrors.ErrValidationFailed, "id is required", nil)
	}
	if expiredAt.IsZero() {
		return internalerrors.New(internalerrors.ErrValidationFailed, "expiredAt is required", nil)
	}

	var returnedID string
	row := r.pool.QueryRow(ctx, `UPDATE api_keys
		SET expires_at = $2,
		    status = CASE WHEN status = $3 THEN status ELSE $4 END,
		    updated_at = $2
		WHERE id = $1
		RETURNING id`, id, expiredAt.UTC(), string(models.APIKeyStatusRevoked), string(models.APIKeyStatusExpired))
	if err := row.Scan(&returnedID); err != nil {
		if err == pgx.ErrNoRows {
			return repository.ErrNotFound
		}
		return internalerrors.New(internalerrors.ErrDatabaseError, "set api key expired", err)
	}
	return nil
}

func (r *apiKeyRepository) SetExpiresAt(ctx context.Context, id string, expiresAt time.Time) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return internalerrors.New(internalerrors.ErrValidationFailed, "id is required", nil)
	}
	if expiresAt.IsZero() {
		return internalerrors.New(internalerrors.ErrValidationFailed, "expiresAt is required", nil)
	}

	var returnedID string
	row := r.pool.QueryRow(ctx, `UPDATE api_keys
		SET expires_at = $2, updated_at = $2
		WHERE id = $1
		RETURNING id`, id, expiresAt.UTC())
	if err := row.Scan(&returnedID); err != nil {
		if err == pgx.ErrNoRows {
			return repository.ErrNotFound
		}
		return internalerrors.New(internalerrors.ErrDatabaseError, "set api key expires_at", err)
	}
	return nil
}

type downstreamRequestRepository struct {
	pool *pgxpool.Pool
}

func (r *downstreamRequestRepository) Create(ctx context.Context, request models.DownstreamRequest) error {
	if err := request.Validate(); err != nil {
		return internalerrors.New(internalerrors.ErrValidationFailed, "validate downstream request", err)
	}

	headers, err := jsonbOrNull(request.Headers)
	if err != nil {
		return internalerrors.New(internalerrors.ErrValidationFailed, "marshal downstream headers", err)
	}
	body, err := jsonbOrNull(request.Body)
	if err != nil {
		return internalerrors.New(internalerrors.ErrValidationFailed, "marshal downstream body", err)
	}

	_, err = r.pool.Exec(ctx, `INSERT INTO downstream_requests (
		id, request_id, api_key_id, action, method, path, query_string, headers, body, client_ip, received_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		request.ID,
		request.RequestID,
		request.APIKeyID,
		string(request.Action),
		request.Method,
		request.Path,
		request.QueryString,
		headers,
		body,
		request.ClientIP,
		request.ReceivedAt.UTC(),
	)
	if err != nil {
		return internalerrors.New(internalerrors.ErrDatabaseError, "insert downstream request", err)
	}
	return nil
}

func (r *downstreamRequestRepository) GetByID(ctx context.Context, id string) (models.DownstreamRequest, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return models.DownstreamRequest{}, internalerrors.New(internalerrors.ErrValidationFailed, "id is required", nil)
	}

	var req models.DownstreamRequest
	var headersBytes []byte
	var bodyBytes []byte
	row := r.pool.QueryRow(ctx, `SELECT id, request_id, api_key_id, action, method, path, query_string, headers, body, client_ip, received_at
		FROM downstream_requests WHERE id = $1`, id)
	var action string
	if err := row.Scan(
		&req.ID,
		&req.RequestID,
		&req.APIKeyID,
		&action,
		&req.Method,
		&req.Path,
		&req.QueryString,
		&headersBytes,
		&bodyBytes,
		&req.ClientIP,
		&req.ReceivedAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			return models.DownstreamRequest{}, repository.ErrNotFound
		}
		return models.DownstreamRequest{}, internalerrors.New(internalerrors.ErrDatabaseError, "select downstream request by id", err)
	}
	meta, err := decodeMap(headersBytes)
	if err != nil {
		return models.DownstreamRequest{}, internalerrors.New(internalerrors.ErrDatabaseError, "decode downstream headers", err)
	}
	bodyMap, err := decodeMap(bodyBytes)
	if err != nil {
		return models.DownstreamRequest{}, internalerrors.New(internalerrors.ErrDatabaseError, "decode downstream body", err)
	}
	req.Headers = meta
	req.Body = bodyMap
	req.Action = models.DownstreamAction(action)
	return req, nil
}

func (r *downstreamRequestRepository) GetByRequestID(ctx context.Context, requestID string) (models.DownstreamRequest, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return models.DownstreamRequest{}, internalerrors.New(internalerrors.ErrValidationFailed, "requestID is required", nil)
	}

	var req models.DownstreamRequest
	var headersBytes []byte
	var bodyBytes []byte
	row := r.pool.QueryRow(ctx, `SELECT id, request_id, api_key_id, action, method, path, query_string, headers, body, client_ip, received_at
		FROM downstream_requests WHERE request_id = $1`, requestID)
	var action string
	if err := row.Scan(
		&req.ID,
		&req.RequestID,
		&req.APIKeyID,
		&action,
		&req.Method,
		&req.Path,
		&req.QueryString,
		&headersBytes,
		&bodyBytes,
		&req.ClientIP,
		&req.ReceivedAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			return models.DownstreamRequest{}, repository.ErrNotFound
		}
		return models.DownstreamRequest{}, internalerrors.New(internalerrors.ErrDatabaseError, "select downstream request by request_id", err)
	}
	meta, err := decodeMap(headersBytes)
	if err != nil {
		return models.DownstreamRequest{}, internalerrors.New(internalerrors.ErrDatabaseError, "decode downstream headers", err)
	}
	bodyMap, err := decodeMap(bodyBytes)
	if err != nil {
		return models.DownstreamRequest{}, internalerrors.New(internalerrors.ErrDatabaseError, "decode downstream body", err)
	}
	req.Headers = meta
	req.Body = bodyMap
	req.Action = models.DownstreamAction(action)
	return req, nil
}

type upstreamAttemptRepository struct {
	pool *pgxpool.Pool
}

func (r *upstreamAttemptRepository) Create(ctx context.Context, attempt models.UpstreamAttempt) error {
	if err := attempt.Validate(); err != nil {
		return internalerrors.New(internalerrors.ErrValidationFailed, "validate upstream attempt", err)
	}

	reqHeaders, err := jsonbOrNull(attempt.RequestHeaders)
	if err != nil {
		return internalerrors.New(internalerrors.ErrValidationFailed, "marshal upstream request headers", err)
	}
	reqBody, err := jsonbOrNull(attempt.RequestBody)
	if err != nil {
		return internalerrors.New(internalerrors.ErrValidationFailed, "marshal upstream request body", err)
	}
	respHeaders, err := jsonbOrNull(attempt.ResponseHeaders)
	if err != nil {
		return internalerrors.New(internalerrors.ErrValidationFailed, "marshal upstream response headers", err)
	}
	respBody, err := jsonbOrNull(attempt.ResponseBody)
	if err != nil {
		return internalerrors.New(internalerrors.ErrValidationFailed, "marshal upstream response body", err)
	}

	_, err = r.pool.Exec(ctx, `INSERT INTO upstream_attempts (
		id, request_id, attempt_number, upstream_action, request_headers, request_body,
		response_status, response_headers, response_body, latency_ms, error, sent_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		attempt.ID,
		attempt.RequestID,
		attempt.AttemptNumber,
		attempt.UpstreamAction,
		reqHeaders,
		reqBody,
		attempt.ResponseStatus,
		respHeaders,
		respBody,
		attempt.LatencyMs,
		attempt.Error,
		attempt.SentAt.UTC(),
	)
	if err != nil {
		return internalerrors.New(internalerrors.ErrDatabaseError, "insert upstream attempt", err)
	}
	return nil
}

func (r *upstreamAttemptRepository) ListByRequestID(ctx context.Context, requestID string) ([]models.UpstreamAttempt, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, internalerrors.New(internalerrors.ErrValidationFailed, "requestID is required", nil)
	}

	rows, err := r.pool.Query(ctx, `SELECT id, request_id, attempt_number, upstream_action,
		request_headers, request_body, response_status, response_headers, response_body,
		latency_ms, error, sent_at
		FROM upstream_attempts WHERE request_id = $1 ORDER BY attempt_number ASC`, requestID)
	if err != nil {
		return nil, internalerrors.New(internalerrors.ErrDatabaseError, "list upstream attempts", err)
	}
	defer rows.Close()

	attempts := make([]models.UpstreamAttempt, 0)
	for rows.Next() {
		var a models.UpstreamAttempt
		var reqHeadersBytes []byte
		var reqBodyBytes []byte
		var respHeadersBytes []byte
		var respBodyBytes []byte
		if err := rows.Scan(
			&a.ID,
			&a.RequestID,
			&a.AttemptNumber,
			&a.UpstreamAction,
			&reqHeadersBytes,
			&reqBodyBytes,
			&a.ResponseStatus,
			&respHeadersBytes,
			&respBodyBytes,
			&a.LatencyMs,
			&a.Error,
			&a.SentAt,
		); err != nil {
			return nil, internalerrors.New(internalerrors.ErrDatabaseError, "scan upstream attempt", err)
		}
		m, err := decodeMap(reqHeadersBytes)
		if err != nil {
			return nil, internalerrors.New(internalerrors.ErrDatabaseError, "decode upstream request headers", err)
		}
		b, err := decodeMap(reqBodyBytes)
		if err != nil {
			return nil, internalerrors.New(internalerrors.ErrDatabaseError, "decode upstream request body", err)
		}
		rh, err := decodeMap(respHeadersBytes)
		if err != nil {
			return nil, internalerrors.New(internalerrors.ErrDatabaseError, "decode upstream response headers", err)
		}
		rb, err := decodeAny(respBodyBytes)
		if err != nil {
			return nil, internalerrors.New(internalerrors.ErrDatabaseError, "decode upstream response body", err)
		}
		a.RequestHeaders = m
		a.RequestBody = b
		a.ResponseHeaders = rh
		a.ResponseBody = rb
		attempts = append(attempts, a)
	}
	if err := rows.Err(); err != nil {
		return nil, internalerrors.New(internalerrors.ErrDatabaseError, "iterate upstream attempts", err)
	}
	return attempts, nil
}

type auditEventRepository struct {
	pool *pgxpool.Pool
}

func (r *auditEventRepository) Create(ctx context.Context, event models.AuditEvent) error {
	if err := event.Validate(); err != nil {
		return internalerrors.New(internalerrors.ErrValidationFailed, "validate audit event", err)
	}

	meta, err := jsonbOrNull(event.Metadata)
	if err != nil {
		return internalerrors.New(internalerrors.ErrValidationFailed, "marshal audit metadata", err)
	}

	_, err = r.pool.Exec(ctx, `INSERT INTO audit_events (
		id, request_id, event_type, actor, action, resource, metadata, created_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		event.ID,
		event.RequestID,
		string(event.EventType),
		event.Actor,
		event.Action,
		event.Resource,
		meta,
		event.CreatedAt.UTC(),
	)
	if err != nil {
		return internalerrors.New(internalerrors.ErrDatabaseError, "insert audit event", err)
	}
	return nil
}

func (r *auditEventRepository) ListByRequestID(ctx context.Context, requestID string) ([]models.AuditEvent, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, internalerrors.New(internalerrors.ErrValidationFailed, "requestID is required", nil)
	}

	rows, err := r.pool.Query(ctx, `SELECT id, request_id, event_type, actor, action, resource, metadata, created_at
		FROM audit_events WHERE request_id = $1 ORDER BY created_at ASC`, requestID)
	if err != nil {
		return nil, internalerrors.New(internalerrors.ErrDatabaseError, "list audit events by request_id", err)
	}
	defer rows.Close()

	events := make([]models.AuditEvent, 0)
	for rows.Next() {
		var e models.AuditEvent
		var metaBytes []byte
		var eventType string
		if err := rows.Scan(
			&e.ID,
			&e.RequestID,
			&eventType,
			&e.Actor,
			&e.Action,
			&e.Resource,
			&metaBytes,
			&e.CreatedAt,
		); err != nil {
			return nil, internalerrors.New(internalerrors.ErrDatabaseError, "scan audit event", err)
		}
		m, err := decodeMap(metaBytes)
		if err != nil {
			return nil, internalerrors.New(internalerrors.ErrDatabaseError, "decode audit metadata", err)
		}
		e.Metadata = m
		e.EventType = models.EventType(eventType)
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, internalerrors.New(internalerrors.ErrDatabaseError, "iterate audit events", err)
	}
	return events, nil
}

func (r *auditEventRepository) ListByTimeRange(ctx context.Context, start, end time.Time) ([]models.AuditEvent, error) {
	if start.IsZero() || end.IsZero() {
		return nil, internalerrors.New(internalerrors.ErrValidationFailed, "start and end are required", nil)
	}

	rows, err := r.pool.Query(ctx, `SELECT id, request_id, event_type, actor, action, resource, metadata, created_at
		FROM audit_events WHERE created_at >= $1 AND created_at <= $2 ORDER BY created_at ASC`, start.UTC(), end.UTC())
	if err != nil {
		return nil, internalerrors.New(internalerrors.ErrDatabaseError, "list audit events by time range", err)
	}
	defer rows.Close()

	events := make([]models.AuditEvent, 0)
	for rows.Next() {
		var e models.AuditEvent
		var metaBytes []byte
		var eventType string
		if err := rows.Scan(
			&e.ID,
			&e.RequestID,
			&eventType,
			&e.Actor,
			&e.Action,
			&e.Resource,
			&metaBytes,
			&e.CreatedAt,
		); err != nil {
			return nil, internalerrors.New(internalerrors.ErrDatabaseError, "scan audit event", err)
		}
		m, err := decodeMap(metaBytes)
		if err != nil {
			return nil, internalerrors.New(internalerrors.ErrDatabaseError, "decode audit metadata", err)
		}
		e.Metadata = m
		e.EventType = models.EventType(eventType)
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, internalerrors.New(internalerrors.ErrDatabaseError, "iterate audit events", err)
	}
	return events, nil
}

type idempotencyRecordRepository struct {
	pool *pgxpool.Pool
}

func (r *idempotencyRecordRepository) GetByKey(ctx context.Context, idempotencyKey string) (models.IdempotencyRecord, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return models.IdempotencyRecord{}, internalerrors.New(internalerrors.ErrValidationFailed, "idempotencyKey is required", nil)
	}

	var rec models.IdempotencyRecord
	var bodyBytes []byte
	row := r.pool.QueryRow(ctx, `SELECT id, idempotency_key, request_hash, response_status, response_body, created_at, expires_at
		FROM idempotency_records WHERE idempotency_key = $1`, idempotencyKey)
	if err := row.Scan(
		&rec.ID,
		&rec.IdempotencyKey,
		&rec.RequestHash,
		&rec.ResponseStatus,
		&bodyBytes,
		&rec.CreatedAt,
		&rec.ExpiresAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			return models.IdempotencyRecord{}, repository.ErrNotFound
		}
		return models.IdempotencyRecord{}, internalerrors.New(internalerrors.ErrDatabaseError, "select idempotency record by key", err)
	}
	bodyAny, err := decodeAny(bodyBytes)
	if err != nil {
		return models.IdempotencyRecord{}, internalerrors.New(internalerrors.ErrDatabaseError, "decode idempotency response body", err)
	}
	rec.ResponseBody = bodyAny
	return rec, nil
}

func (r *idempotencyRecordRepository) Create(ctx context.Context, record models.IdempotencyRecord) error {
	if err := record.Validate(); err != nil {
		return internalerrors.New(internalerrors.ErrValidationFailed, "validate idempotency record", err)
	}

	body, err := jsonbOrNull(record.ResponseBody)
	if err != nil {
		return internalerrors.New(internalerrors.ErrValidationFailed, "marshal idempotency response body", err)
	}

	_, err = r.pool.Exec(ctx, `INSERT INTO idempotency_records (
		id, idempotency_key, request_hash, response_status, response_body, created_at, expires_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		record.ID,
		record.IdempotencyKey,
		record.RequestHash,
		record.ResponseStatus,
		body,
		record.CreatedAt.UTC(),
		record.ExpiresAt.UTC(),
	)
	if err != nil {
		return internalerrors.New(internalerrors.ErrDatabaseError, "insert idempotency record", err)
	}
	return nil
}

func (r *idempotencyRecordRepository) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		return 0, internalerrors.New(internalerrors.ErrValidationFailed, "now is required", nil)
	}

	tag, err := r.pool.Exec(ctx, `DELETE FROM idempotency_records WHERE expires_at <= $1`, now.UTC())
	if err != nil {
		return 0, internalerrors.New(internalerrors.ErrDatabaseError, "delete expired idempotency records", err)
	}
	return tag.RowsAffected(), nil
}

type adminUserRepository struct {
	pool *pgxpool.Pool
}

func (r *adminUserRepository) Create(ctx context.Context, user models.AdminUser) error {
	if err := user.Validate(); err != nil {
		return internalerrors.New(internalerrors.ErrValidationFailed, "validate admin user", err)
	}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO admin_users (id, email, password_hash, created_at, updated_at) VALUES ($1, $2, $3, $4, $5)`,
		user.ID,
		user.Email,
		user.PasswordHash,
		user.CreatedAt.UTC(),
		user.UpdatedAt.UTC(),
	)
	if err != nil {
		return internalerrors.New(internalerrors.ErrDatabaseError, "insert admin user", err)
	}
	return nil
}

func (r *adminUserRepository) GetByID(ctx context.Context, id string) (models.AdminUser, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return models.AdminUser{}, internalerrors.New(internalerrors.ErrValidationFailed, "id is required", nil)
	}
	return r.getOne(ctx, `SELECT id, email, password_hash, created_at, updated_at FROM admin_users WHERE id = $1`, id)
}

func (r *adminUserRepository) GetByEmail(ctx context.Context, email string) (models.AdminUser, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return models.AdminUser{}, internalerrors.New(internalerrors.ErrValidationFailed, "email is required", nil)
	}
	return r.getOne(ctx, `SELECT id, email, password_hash, created_at, updated_at FROM admin_users WHERE email = $1`, email)
}

func (r *adminUserRepository) getOne(ctx context.Context, query string, arg any) (models.AdminUser, error) {
	var out models.AdminUser
	row := r.pool.QueryRow(ctx, query, arg)
	if err := row.Scan(&out.ID, &out.Email, &out.PasswordHash, &out.CreatedAt, &out.UpdatedAt); err != nil {
		if err == pgx.ErrNoRows {
			return models.AdminUser{}, repository.ErrNotFound
		}
		return models.AdminUser{}, internalerrors.New(internalerrors.ErrDatabaseError, "select admin user", err)
	}
	return out, nil
}

func (r *adminUserRepository) Update(ctx context.Context, user models.AdminUser) error {
	if err := user.Validate(); err != nil {
		return internalerrors.New(internalerrors.ErrValidationFailed, "validate admin user", err)
	}
	var returnedID string
	row := r.pool.QueryRow(ctx,
		`UPDATE admin_users SET email = $2, password_hash = $3, updated_at = $4 WHERE id = $1 RETURNING id`,
		user.ID,
		user.Email,
		user.PasswordHash,
		user.UpdatedAt.UTC(),
	)
	if err := row.Scan(&returnedID); err != nil {
		if err == pgx.ErrNoRows {
			return repository.ErrNotFound
		}
		return internalerrors.New(internalerrors.ErrDatabaseError, "update admin user", err)
	}
	return nil
}

type adminSessionRepository struct {
	pool *pgxpool.Pool
}

func (r *adminSessionRepository) Create(ctx context.Context, session models.AdminSession) error {
	if err := session.Validate(); err != nil {
		return internalerrors.New(internalerrors.ErrValidationFailed, "validate admin session", err)
	}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO admin_sessions (id, admin_user_id, token_hash, created_at, expires_at) VALUES ($1, $2, $3, $4, $5)`,
		session.ID,
		session.AdminUserID,
		session.TokenHash,
		session.CreatedAt.UTC(),
		session.ExpiresAt.UTC(),
	)
	if err != nil {
		return internalerrors.New(internalerrors.ErrDatabaseError, "insert admin session", err)
	}
	return nil
}

func (r *adminSessionRepository) GetByTokenHash(ctx context.Context, tokenHash string) (models.AdminSession, error) {
	tokenHash = strings.TrimSpace(tokenHash)
	if tokenHash == "" {
		return models.AdminSession{}, internalerrors.New(internalerrors.ErrValidationFailed, "tokenHash is required", nil)
	}
	var out models.AdminSession
	row := r.pool.QueryRow(ctx,
		`SELECT id, admin_user_id, token_hash, created_at, expires_at FROM admin_sessions WHERE token_hash = $1`,
		tokenHash,
	)
	if err := row.Scan(&out.ID, &out.AdminUserID, &out.TokenHash, &out.CreatedAt, &out.ExpiresAt); err != nil {
		if err == pgx.ErrNoRows {
			return models.AdminSession{}, repository.ErrNotFound
		}
		return models.AdminSession{}, internalerrors.New(internalerrors.ErrDatabaseError, "select admin session", err)
	}
	return out, nil
}

func (r *adminSessionRepository) Delete(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return internalerrors.New(internalerrors.ErrValidationFailed, "id is required", nil)
	}
	var returnedID string
	row := r.pool.QueryRow(ctx, `DELETE FROM admin_sessions WHERE id = $1 RETURNING id`, id)
	if err := row.Scan(&returnedID); err != nil {
		if err == pgx.ErrNoRows {
			return repository.ErrNotFound
		}
		return internalerrors.New(internalerrors.ErrDatabaseError, "delete admin session", err)
	}
	return nil
}

func (r *adminSessionRepository) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		return 0, internalerrors.New(internalerrors.ErrValidationFailed, "now is required", nil)
	}
	tag, err := r.pool.Exec(ctx, `DELETE FROM admin_sessions WHERE expires_at <= $1`, now.UTC())
	if err != nil {
		return 0, internalerrors.New(internalerrors.ErrDatabaseError, "delete expired admin sessions", err)
	}
	return tag.RowsAffected(), nil
}

type passwordResetTokenRepository struct {
	pool *pgxpool.Pool
}

func (r *passwordResetTokenRepository) Create(ctx context.Context, token models.PasswordResetToken) error {
	if err := token.Validate(); err != nil {
		return internalerrors.New(internalerrors.ErrValidationFailed, "validate password reset token", err)
	}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO password_reset_tokens (id, admin_user_id, token_hash, created_at, expires_at, used_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		token.ID,
		token.AdminUserID,
		token.TokenHash,
		token.CreatedAt.UTC(),
		token.ExpiresAt.UTC(),
		token.UsedAt,
	)
	if err != nil {
		return internalerrors.New(internalerrors.ErrDatabaseError, "insert password reset token", err)
	}
	return nil
}

func (r *passwordResetTokenRepository) GetByTokenHash(ctx context.Context, tokenHash string) (models.PasswordResetToken, error) {
	tokenHash = strings.TrimSpace(tokenHash)
	if tokenHash == "" {
		return models.PasswordResetToken{}, internalerrors.New(internalerrors.ErrValidationFailed, "tokenHash is required", nil)
	}
	var out models.PasswordResetToken
	row := r.pool.QueryRow(ctx,
		`SELECT id, admin_user_id, token_hash, created_at, expires_at, used_at FROM password_reset_tokens WHERE token_hash = $1`,
		tokenHash,
	)
	if err := row.Scan(&out.ID, &out.AdminUserID, &out.TokenHash, &out.CreatedAt, &out.ExpiresAt, &out.UsedAt); err != nil {
		if err == pgx.ErrNoRows {
			return models.PasswordResetToken{}, repository.ErrNotFound
		}
		return models.PasswordResetToken{}, internalerrors.New(internalerrors.ErrDatabaseError, "select password reset token", err)
	}
	return out, nil
}

func (r *passwordResetTokenRepository) MarkUsed(ctx context.Context, id string, usedAt time.Time) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return internalerrors.New(internalerrors.ErrValidationFailed, "id is required", nil)
	}
	if usedAt.IsZero() {
		return internalerrors.New(internalerrors.ErrValidationFailed, "usedAt is required", nil)
	}

	var returnedID string
	row := r.pool.QueryRow(ctx,
		`UPDATE password_reset_tokens SET used_at = $2 WHERE id = $1 AND used_at IS NULL RETURNING id`,
		id,
		usedAt.UTC(),
	)
	if err := row.Scan(&returnedID); err != nil {
		if err == pgx.ErrNoRows {
			return repository.ErrNotFound
		}
		return internalerrors.New(internalerrors.ErrDatabaseError, "mark password reset token used", err)
	}
	return nil
}

func (r *passwordResetTokenRepository) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		return 0, internalerrors.New(internalerrors.ErrValidationFailed, "now is required", nil)
	}
	tag, err := r.pool.Exec(ctx, `DELETE FROM password_reset_tokens WHERE expires_at <= $1`, now.UTC())
	if err != nil {
		return 0, internalerrors.New(internalerrors.ErrDatabaseError, "delete expired password reset tokens", err)
	}
	return tag.RowsAffected(), nil
}

func jsonbOrNull(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if string(b) == "null" {
		return nil, nil
	}
	return json.RawMessage(b), nil
}

func decodeMap(b []byte) (map[string]any, error) {
	if len(b) == 0 {
		return nil, nil
	}
	if string(b) == "null" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("unmarshal json map: %w", err)
	}
	return m, nil
}

func decodeAny(b []byte) (any, error) {
	if len(b) == 0 {
		return nil, nil
	}
	if string(b) == "null" {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("unmarshal json: %w", err)
	}
	return v, nil
}
