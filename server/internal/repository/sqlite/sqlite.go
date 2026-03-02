package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jimeng-relay/server/internal/models"
	"github.com/jimeng-relay/server/internal/repository"

	_ "modernc.org/sqlite"
)

type Repositories struct {
	DB *sql.DB

	APIKeys             *APIKeyRepo
	DownstreamRequests  *DownstreamRequestRepo
	UpstreamAttempts    *UpstreamAttemptRepo
	AuditEvents         *AuditEventRepo
	IdempotencyRecords  *IdempotencyRecordRepo
	AdminUsers          *AdminUserRepo
	AdminSessions       *AdminSessionRepo
	PasswordResetTokens *PasswordResetTokenRepo
}

func Open(ctx context.Context, dsn string) (*Repositories, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("dsn is required")
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}

	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}

	if err := ApplyMigrations(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return New(db), nil
}

func New(db *sql.DB) *Repositories {
	r := &Repositories{DB: db}
	r.APIKeys = &APIKeyRepo{db: db}
	r.DownstreamRequests = &DownstreamRequestRepo{db: db}
	r.UpstreamAttempts = &UpstreamAttemptRepo{db: db}
	r.AuditEvents = &AuditEventRepo{db: db}
	r.IdempotencyRecords = &IdempotencyRecordRepo{db: db}
	r.AdminUsers = &AdminUserRepo{db: db}
	r.AdminSessions = &AdminSessionRepo{db: db}
	r.PasswordResetTokens = &PasswordResetTokenRepo{db: db}
	return r
}

func (r *Repositories) Close() error {
	if r == nil || r.DB == nil {
		return nil
	}
	return r.DB.Close()
}

type APIKeyRepo struct{ db *sql.DB }

var _ repository.APIKeyRepository = (*APIKeyRepo)(nil)

func (r *APIKeyRepo) Create(ctx context.Context, key models.APIKey) error {
	if err := key.Validate(); err != nil {
		return err
	}

	_, err := r.db.ExecContext(ctx,
		`INSERT INTO api_keys (
			id, access_key, secret_key_hash, secret_key_ciphertext, description,
			created_at, updated_at, expires_at, revoked_at, rotation_of, status
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`,
		key.ID,
		key.AccessKey,
		key.SecretKeyHash,
		key.SecretKeyCiphertext,
		nullableString(key.Description),
		formatTime(key.CreatedAt),
		formatTime(key.UpdatedAt),
		nullableTime(key.ExpiresAt),
		nullableTime(key.RevokedAt),
		nullableStringPtr(key.RotationOf),
		string(key.Status),
	)
	if err != nil {
		return err
	}
	return nil
}

func (r *APIKeyRepo) GetByAccessKey(ctx context.Context, accessKey string) (models.APIKey, error) {
	return r.getOne(ctx,
		`SELECT id, access_key, secret_key_hash, secret_key_ciphertext, description, multiplier, created_at, updated_at, expires_at, revoked_at, rotation_of, status
		 FROM api_keys
		 WHERE access_key = ?
		 LIMIT 1;`,
		accessKey,
	)
}

func (r *APIKeyRepo) GetByID(ctx context.Context, id string) (models.APIKey, error) {
	return r.getOne(ctx,
		`SELECT id, access_key, secret_key_hash, secret_key_ciphertext, description, multiplier, created_at, updated_at, expires_at, revoked_at, rotation_of, status
		 FROM api_keys
		 WHERE id = ?
		 LIMIT 1;`,
		id,
	)
}

func (r *APIKeyRepo) getOne(ctx context.Context, query string, arg any) (models.APIKey, error) {
	var out models.APIKey

	var description sql.NullString
	var createdAt string
	var updatedAt string
	var expiresAt sql.NullString
	var revokedAt sql.NullString
	var rotationOf sql.NullString
	var status string

	err := r.db.QueryRowContext(ctx, query, arg).Scan(
		&out.ID,
		&out.AccessKey,
		&out.SecretKeyHash,
		&out.SecretKeyCiphertext,
		&description,
		&out.Multiplier,
		&createdAt,
		&updatedAt,
		&expiresAt,
		&revokedAt,
		&rotationOf,
		&status,
	)
	if err != nil {
		return models.APIKey{}, mapNotFound(err)
	}

	out.Description = description.String
	parsedCreatedAt, err := parseTime(createdAt)
	if err != nil {
		return models.APIKey{}, err
	}
	parsedUpdatedAt, err := parseTime(updatedAt)
	if err != nil {
		return models.APIKey{}, err
	}
	out.CreatedAt = parsedCreatedAt
	out.UpdatedAt = parsedUpdatedAt
	out.ExpiresAt = parseNullableTime(expiresAt)
	out.RevokedAt = parseNullableTime(revokedAt)
	out.RotationOf = parseNullableStringPtr(rotationOf)
	out.Status = models.APIKeyStatus(status)

	return out, nil
}

func (r *APIKeyRepo) List(ctx context.Context) ([]models.APIKey, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, access_key, secret_key_hash, secret_key_ciphertext, description, multiplier, created_at, updated_at, expires_at, revoked_at, rotation_of, status
		 FROM api_keys
		 ORDER BY created_at DESC;`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.APIKey
	for rows.Next() {
		var k models.APIKey
		var description sql.NullString
		var createdAt string
		var updatedAt string
		var expiresAt sql.NullString
		var revokedAt sql.NullString
		var rotationOf sql.NullString
		var status string

		if err := rows.Scan(
			&k.ID,
			&k.AccessKey,
			&k.SecretKeyHash,
			&k.SecretKeyCiphertext,
			&description,
			&k.Multiplier,
			&createdAt,
			&updatedAt,
			&expiresAt,
			&revokedAt,
			&rotationOf,
			&status,
		); err != nil {
			return nil, err
		}

		k.Description = description.String
		parsedCreatedAt, err := parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		parsedUpdatedAt, err := parseTime(updatedAt)
		if err != nil {
			return nil, err
		}
		k.CreatedAt = parsedCreatedAt
		k.UpdatedAt = parsedUpdatedAt
		k.ExpiresAt = parseNullableTime(expiresAt)
		k.RevokedAt = parseNullableTime(revokedAt)
		k.RotationOf = parseNullableStringPtr(rotationOf)
		k.Status = models.APIKeyStatus(status)
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *APIKeyRepo) Revoke(ctx context.Context, id string, revokedAt time.Time) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("id is required")
	}
	if revokedAt.IsZero() {
		return fmt.Errorf("revokedAt is required")
	}

	res, err := r.db.ExecContext(ctx,
		`UPDATE api_keys SET revoked_at = ?, status = ?, updated_at = ? WHERE id = ?;`,
		formatTime(revokedAt),
		string(models.APIKeyStatusRevoked),
		formatTime(time.Now().UTC()),
		id,
	)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (r *APIKeyRepo) SetExpired(ctx context.Context, id string, expiredAt time.Time) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("id is required")
	}
	if expiredAt.IsZero() {
		return fmt.Errorf("expiredAt is required")
	}

	res, err := r.db.ExecContext(ctx,
		`UPDATE api_keys
		 SET expires_at = ?,
		     status = CASE WHEN status = ? THEN status ELSE ? END,
		     updated_at = ?
		 WHERE id = ?;`,
		formatTime(expiredAt),
		string(models.APIKeyStatusRevoked),
		string(models.APIKeyStatusExpired),
		formatTime(time.Now().UTC()),
		id,
	)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (r *APIKeyRepo) SetExpiresAt(ctx context.Context, id string, expiresAt time.Time) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("id is required")
	}
	if expiresAt.IsZero() {
		return fmt.Errorf("expiresAt is required")
	}

	res, err := r.db.ExecContext(ctx,
		`UPDATE api_keys
		 SET expires_at = ?,
		     updated_at = ?
		 WHERE id = ?;`,
		formatTime(expiresAt),
		formatTime(time.Now().UTC()),
		id,
	)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return repository.ErrNotFound
	}
	return nil
}

type DownstreamRequestRepo struct{ db *sql.DB }

var _ repository.DownstreamRequestRepository = (*DownstreamRequestRepo)(nil)

func (r *DownstreamRequestRepo) Create(ctx context.Context, request models.DownstreamRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}

	headersJSON, err := marshalJSONNullable(request.Headers)
	if err != nil {
		return err
	}
	bodyJSON, err := marshalJSONNullable(request.Body)
	if err != nil {
		return err
	}

	_, err = r.db.ExecContext(ctx,
		`INSERT INTO downstream_requests (
			id, request_id, api_key_id, action,
			method, path, query_string, headers, body, client_ip, received_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`,
		request.ID,
		request.RequestID,
		request.APIKeyID,
		string(request.Action),
		request.Method,
		request.Path,
		nullableString(request.QueryString),
		headersJSON,
		bodyJSON,
		nullableString(request.ClientIP),
		formatTime(request.ReceivedAt),
	)
	if err != nil {
		return err
	}
	return nil
}

func (r *DownstreamRequestRepo) GetByID(ctx context.Context, id string) (models.DownstreamRequest, error) {
	return r.getOne(ctx, `SELECT id, request_id, api_key_id, action, method, path, query_string, headers, body, client_ip, received_at FROM downstream_requests WHERE id = ? LIMIT 1;`, id)
}

func (r *DownstreamRequestRepo) GetByRequestID(ctx context.Context, requestID string) (models.DownstreamRequest, error) {
	return r.getOne(ctx, `SELECT id, request_id, api_key_id, action, method, path, query_string, headers, body, client_ip, received_at FROM downstream_requests WHERE request_id = ? LIMIT 1;`, requestID)
}

func (r *DownstreamRequestRepo) getOne(ctx context.Context, query string, arg any) (models.DownstreamRequest, error) {
	var out models.DownstreamRequest
	var action string
	var queryString sql.NullString
	var headers sql.NullString
	var body sql.NullString
	var clientIP sql.NullString
	var receivedAt string

	if err := r.db.QueryRowContext(ctx, query, arg).Scan(
		&out.ID,
		&out.RequestID,
		&out.APIKeyID,
		&action,
		&out.Method,
		&out.Path,
		&queryString,
		&headers,
		&body,
		&clientIP,
		&receivedAt,
	); err != nil {
		return models.DownstreamRequest{}, mapNotFound(err)
	}

	out.Action = models.DownstreamAction(action)
	out.QueryString = queryString.String
	out.ClientIP = clientIP.String
	parsedReceivedAt, err := parseTime(receivedAt)
	if err != nil {
		return models.DownstreamRequest{}, err
	}
	out.ReceivedAt = parsedReceivedAt

	if err := unmarshalJSONNullable(headers, &out.Headers); err != nil {
		return models.DownstreamRequest{}, err
	}
	if err := unmarshalJSONNullable(body, &out.Body); err != nil {
		return models.DownstreamRequest{}, err
	}

	return out, nil
}

type UpstreamAttemptRepo struct{ db *sql.DB }

var _ repository.UpstreamAttemptRepository = (*UpstreamAttemptRepo)(nil)

func (r *UpstreamAttemptRepo) Create(ctx context.Context, attempt models.UpstreamAttempt) error {
	if err := attempt.Validate(); err != nil {
		return err
	}

	reqHeadersJSON, err := marshalJSONNullable(attempt.RequestHeaders)
	if err != nil {
		return err
	}
	reqBodyJSON, err := marshalJSONNullable(attempt.RequestBody)
	if err != nil {
		return err
	}
	respHeadersJSON, err := marshalJSONNullable(attempt.ResponseHeaders)
	if err != nil {
		return err
	}
	respBodyJSON, err := marshalJSONNullable(attempt.ResponseBody)
	if err != nil {
		return err
	}

	_, err = r.db.ExecContext(ctx,
		`INSERT INTO upstream_attempts (
			id, request_id, attempt_number, upstream_action,
			request_headers, request_body,
			response_status, response_headers, response_body,
			latency_ms, error, sent_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`,
		attempt.ID,
		attempt.RequestID,
		attempt.AttemptNumber,
		attempt.UpstreamAction,
		reqHeadersJSON,
		reqBodyJSON,
		attempt.ResponseStatus,
		respHeadersJSON,
		respBodyJSON,
		attempt.LatencyMs,
		nullableStringPtr(attempt.Error),
		formatTime(attempt.SentAt),
	)
	if err != nil {
		return err
	}
	return nil
}

func (r *UpstreamAttemptRepo) ListByRequestID(ctx context.Context, requestID string) ([]models.UpstreamAttempt, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, request_id, attempt_number, upstream_action, request_headers, request_body, response_status, response_headers, response_body, latency_ms, error, sent_at
		 FROM upstream_attempts
		 WHERE request_id = ?
		 ORDER BY attempt_number ASC;`,
		requestID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.UpstreamAttempt
	for rows.Next() {
		var a models.UpstreamAttempt
		var reqHeaders sql.NullString
		var reqBody sql.NullString
		var respHeaders sql.NullString
		var respBody sql.NullString
		var errStr sql.NullString
		var sentAt string

		if err := rows.Scan(
			&a.ID,
			&a.RequestID,
			&a.AttemptNumber,
			&a.UpstreamAction,
			&reqHeaders,
			&reqBody,
			&a.ResponseStatus,
			&respHeaders,
			&respBody,
			&a.LatencyMs,
			&errStr,
			&sentAt,
		); err != nil {
			return nil, err
		}

		if err := unmarshalJSONNullable(reqHeaders, &a.RequestHeaders); err != nil {
			return nil, err
		}
		if err := unmarshalJSONNullable(reqBody, &a.RequestBody); err != nil {
			return nil, err
		}
		if err := unmarshalJSONNullable(respHeaders, &a.ResponseHeaders); err != nil {
			return nil, err
		}
		if err := unmarshalJSONNullable(respBody, &a.ResponseBody); err != nil {
			return nil, err
		}
		if errStr.Valid {
			v := errStr.String
			a.Error = &v
		}
		parsedSentAt, err := parseTime(sentAt)
		if err != nil {
			return nil, err
		}
		a.SentAt = parsedSentAt
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

type AuditEventRepo struct{ db *sql.DB }

var _ repository.AuditEventRepository = (*AuditEventRepo)(nil)

func (r *AuditEventRepo) Create(ctx context.Context, event models.AuditEvent) error {
	if err := event.Validate(); err != nil {
		return err
	}

	metadataJSON, err := marshalJSONNullable(event.Metadata)
	if err != nil {
		return err
	}

	_, err = r.db.ExecContext(ctx,
		`INSERT INTO audit_events (
			id, request_id, event_type, actor, action, resource, metadata, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?);`,
		event.ID,
		event.RequestID,
		string(event.EventType),
		nullableString(event.Actor),
		event.Action,
		event.Resource,
		metadataJSON,
		formatTime(event.CreatedAt),
	)
	if err != nil {
		return err
	}
	return nil
}

func (r *AuditEventRepo) ListByRequestID(ctx context.Context, requestID string) ([]models.AuditEvent, error) {
	return r.list(ctx,
		`SELECT id, request_id, event_type, actor, action, resource, metadata, created_at
		 FROM audit_events
		 WHERE request_id = ?
		 ORDER BY created_at ASC;`,
		requestID,
	)
}

func (r *AuditEventRepo) ListByTimeRange(ctx context.Context, start, end time.Time) ([]models.AuditEvent, error) {
	return r.list(ctx,
		`SELECT id, request_id, event_type, actor, action, resource, metadata, created_at
		 FROM audit_events
		 WHERE created_at >= ? AND created_at <= ?
		 ORDER BY created_at ASC;`,
		formatTime(start),
		formatTime(end),
	)
}

func (r *AuditEventRepo) list(ctx context.Context, query string, args ...any) ([]models.AuditEvent, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.AuditEvent
	for rows.Next() {
		var e models.AuditEvent
		var eventType string
		var actor sql.NullString
		var metadata sql.NullString
		var createdAt string
		if err := rows.Scan(
			&e.ID,
			&e.RequestID,
			&eventType,
			&actor,
			&e.Action,
			&e.Resource,
			&metadata,
			&createdAt,
		); err != nil {
			return nil, err
		}
		e.EventType = models.EventType(eventType)
		e.Actor = actor.String
		if err := unmarshalJSONNullable(metadata, &e.Metadata); err != nil {
			return nil, err
		}
		parsedCreatedAt, err := parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		e.CreatedAt = parsedCreatedAt
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

type IdempotencyRecordRepo struct{ db *sql.DB }

var _ repository.IdempotencyRecordRepository = (*IdempotencyRecordRepo)(nil)

func (r *IdempotencyRecordRepo) GetByKey(ctx context.Context, idempotencyKey string) (models.IdempotencyRecord, error) {
	var out models.IdempotencyRecord
	var respBody sql.NullString
	var createdAt string
	var expiresAt string

	err := r.db.QueryRowContext(ctx,
		`SELECT id, idempotency_key, request_hash, response_status, response_body, created_at, expires_at
		 FROM idempotency_records
		 WHERE idempotency_key = ?
		 LIMIT 1;`,
		idempotencyKey,
	).Scan(
		&out.ID,
		&out.IdempotencyKey,
		&out.RequestHash,
		&out.ResponseStatus,
		&respBody,
		&createdAt,
		&expiresAt,
	)
	if err != nil {
		return models.IdempotencyRecord{}, mapNotFound(err)
	}

	if err := unmarshalJSONNullable(respBody, &out.ResponseBody); err != nil {
		return models.IdempotencyRecord{}, err
	}
	parsedCreatedAt, err := parseTime(createdAt)
	if err != nil {
		return models.IdempotencyRecord{}, err
	}
	parsedExpiresAt, err := parseTime(expiresAt)
	if err != nil {
		return models.IdempotencyRecord{}, err
	}
	out.CreatedAt = parsedCreatedAt
	out.ExpiresAt = parsedExpiresAt
	return out, nil
}

func (r *IdempotencyRecordRepo) Create(ctx context.Context, record models.IdempotencyRecord) error {
	if err := record.Validate(); err != nil {
		return err
	}

	respBodyJSON, err := marshalJSONNullable(record.ResponseBody)
	if err != nil {
		return err
	}

	_, err = r.db.ExecContext(ctx,
		`INSERT INTO idempotency_records (
			id, idempotency_key, request_hash, response_status, response_body, created_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?);`,
		record.ID,
		record.IdempotencyKey,
		record.RequestHash,
		record.ResponseStatus,
		respBodyJSON,
		formatTime(record.CreatedAt),
		formatTime(record.ExpiresAt),
	)
	if err != nil {
		return err
	}
	return nil
}

func (r *IdempotencyRecordRepo) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM idempotency_records WHERE expires_at <= ?;`, formatTime(now))
	if err != nil {
		return 0, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return rows, nil
}

type AdminUserRepo struct{ db *sql.DB }

var _ repository.AdminUserRepository = (*AdminUserRepo)(nil)

func (r *AdminUserRepo) Create(ctx context.Context, user models.AdminUser) error {
	if err := user.Validate(); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO admin_users (id, email, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?, ?);`,
		user.ID,
		user.Email,
		user.PasswordHash,
		formatTime(user.CreatedAt),
		formatTime(user.UpdatedAt),
	)
	return err
}

func (r *AdminUserRepo) GetByID(ctx context.Context, id string) (models.AdminUser, error) {
	return r.getOne(ctx, `SELECT id, email, password_hash, created_at, updated_at FROM admin_users WHERE id = ? LIMIT 1;`, id)
}

func (r *AdminUserRepo) GetByEmail(ctx context.Context, email string) (models.AdminUser, error) {
	return r.getOne(ctx, `SELECT id, email, password_hash, created_at, updated_at FROM admin_users WHERE email = ? LIMIT 1;`, email)
}

func (r *AdminUserRepo) Update(ctx context.Context, user models.AdminUser) error {
	if err := user.Validate(); err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE admin_users SET email = ?, password_hash = ?, updated_at = ? WHERE id = ?;`,
		user.Email,
		user.PasswordHash,
		formatTime(user.UpdatedAt),
		user.ID,
	)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (r *AdminUserRepo) getOne(ctx context.Context, query string, arg any) (models.AdminUser, error) {
	var out models.AdminUser
	var createdAt string
	var updatedAt string
	if err := r.db.QueryRowContext(ctx, query, arg).Scan(&out.ID, &out.Email, &out.PasswordHash, &createdAt, &updatedAt); err != nil {
		return models.AdminUser{}, mapNotFound(err)
	}
	parsedCreatedAt, err := parseTime(createdAt)
	if err != nil {
		return models.AdminUser{}, err
	}
	parsedUpdatedAt, err := parseTime(updatedAt)
	if err != nil {
		return models.AdminUser{}, err
	}
	out.CreatedAt = parsedCreatedAt
	out.UpdatedAt = parsedUpdatedAt
	return out, nil
}

type AdminSessionRepo struct{ db *sql.DB }

var _ repository.AdminSessionRepository = (*AdminSessionRepo)(nil)

func (r *AdminSessionRepo) Create(ctx context.Context, session models.AdminSession) error {
	if err := session.Validate(); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO admin_sessions (id, admin_user_id, token_hash, created_at, expires_at) VALUES (?, ?, ?, ?, ?);`,
		session.ID,
		session.AdminUserID,
		session.TokenHash,
		formatTime(session.CreatedAt),
		formatTime(session.ExpiresAt),
	)
	return err
}

func (r *AdminSessionRepo) GetByTokenHash(ctx context.Context, tokenHash string) (models.AdminSession, error) {
	var out models.AdminSession
	var createdAt string
	var expiresAt string
	if err := r.db.QueryRowContext(ctx, `SELECT id, admin_user_id, token_hash, created_at, expires_at FROM admin_sessions WHERE token_hash = ? LIMIT 1;`, tokenHash).
		Scan(&out.ID, &out.AdminUserID, &out.TokenHash, &createdAt, &expiresAt); err != nil {
		return models.AdminSession{}, mapNotFound(err)
	}
	parsedCreatedAt, err := parseTime(createdAt)
	if err != nil {
		return models.AdminSession{}, err
	}
	parsedExpiresAt, err := parseTime(expiresAt)
	if err != nil {
		return models.AdminSession{}, err
	}
	out.CreatedAt = parsedCreatedAt
	out.ExpiresAt = parsedExpiresAt
	return out, nil
}

func (r *AdminSessionRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE id = ?;`, id)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (r *AdminSessionRepo) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE expires_at <= ?;`, formatTime(now))
	if err != nil {
		return 0, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return rows, nil
}

type PasswordResetTokenRepo struct{ db *sql.DB }

var _ repository.PasswordResetTokenRepository = (*PasswordResetTokenRepo)(nil)

func (r *PasswordResetTokenRepo) Create(ctx context.Context, token models.PasswordResetToken) error {
	if err := token.Validate(); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO password_reset_tokens (id, admin_user_id, token_hash, created_at, expires_at, used_at) VALUES (?, ?, ?, ?, ?, ?);`,
		token.ID,
		token.AdminUserID,
		token.TokenHash,
		formatTime(token.CreatedAt),
		formatTime(token.ExpiresAt),
		nullableTime(token.UsedAt),
	)
	return err
}

func (r *PasswordResetTokenRepo) GetByTokenHash(ctx context.Context, tokenHash string) (models.PasswordResetToken, error) {
	var out models.PasswordResetToken
	var createdAt string
	var expiresAt string
	var usedAt sql.NullString

	if err := r.db.QueryRowContext(ctx,
		`SELECT id, admin_user_id, token_hash, created_at, expires_at, used_at FROM password_reset_tokens WHERE token_hash = ? LIMIT 1;`,
		tokenHash,
	).Scan(&out.ID, &out.AdminUserID, &out.TokenHash, &createdAt, &expiresAt, &usedAt); err != nil {
		return models.PasswordResetToken{}, mapNotFound(err)
	}

	parsedCreatedAt, err := parseTime(createdAt)
	if err != nil {
		return models.PasswordResetToken{}, err
	}
	parsedExpiresAt, err := parseTime(expiresAt)
	if err != nil {
		return models.PasswordResetToken{}, err
	}
	out.CreatedAt = parsedCreatedAt
	out.ExpiresAt = parsedExpiresAt
	out.UsedAt = parseNullableTime(usedAt)
	return out, nil
}

func (r *PasswordResetTokenRepo) MarkUsed(ctx context.Context, id string, usedAt time.Time) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE password_reset_tokens SET used_at = ? WHERE id = ? AND used_at IS NULL;`,
		formatTime(usedAt),
		id,
	)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (r *PasswordResetTokenRepo) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM password_reset_tokens WHERE expires_at <= ?;`, formatTime(now))
	if err != nil {
		return 0, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return rows, nil
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(v string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, v)
	if err == nil {
		return t, nil
	}
	t, err2 := time.Parse(time.RFC3339, v)
	if err2 == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("parse time %q: %w", v, err)
}

func nullableTime(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return formatTime(*t)
}

func parseNullableTime(v sql.NullString) *time.Time {
	if !v.Valid || strings.TrimSpace(v.String) == "" {
		return nil
	}
	t, err := parseTime(v.String)
	if err != nil {
		return nil
	}
	return &t
}

func nullableString(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

func nullableStringPtr(v *string) any {
	if v == nil || strings.TrimSpace(*v) == "" {
		return nil
	}
	return *v
}

func parseNullableStringPtr(v sql.NullString) *string {
	if !v.Valid || strings.TrimSpace(v.String) == "" {
		return nil
	}
	s := v.String
	return &s
}

func marshalJSONNullable(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func unmarshalJSONNullable[T any](v sql.NullString, dst *T) error {
	if dst == nil {
		return fmt.Errorf("dst is nil")
	}
	if !v.Valid || strings.TrimSpace(v.String) == "" {
		var zero T
		*dst = zero
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(v.String))
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}

func mapNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return repository.ErrNotFound
	}
	return err
}
