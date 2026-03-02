package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	internalerrors "github.com/jimeng-relay/server/internal/errors"
)

type migration struct {
	version    int
	name       string
	statements []string
}

var migrations = []migration{
	{
		version: 1,
		name:    "init",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS schema_migrations (
				version INTEGER PRIMARY KEY,
				name TEXT NOT NULL,
				applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
			)`,

			`CREATE TABLE IF NOT EXISTS api_keys (
				id TEXT PRIMARY KEY,
				access_key TEXT NOT NULL UNIQUE,
				secret_key_hash TEXT NOT NULL,
				secret_key_ciphertext TEXT NOT NULL DEFAULT '',
				description TEXT NOT NULL DEFAULT '',
				created_at TIMESTAMPTZ NOT NULL,
				updated_at TIMESTAMPTZ NOT NULL,
				expires_at TIMESTAMPTZ,
				revoked_at TIMESTAMPTZ,
				rotation_of TEXT,
				status TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_api_keys_status ON api_keys(status)`,
			`CREATE INDEX IF NOT EXISTS idx_api_keys_expires_at ON api_keys(expires_at)`,

			`CREATE TABLE IF NOT EXISTS downstream_requests (
				id TEXT PRIMARY KEY,
				request_id TEXT NOT NULL UNIQUE,
				api_key_id TEXT NOT NULL REFERENCES api_keys(id),
				action TEXT NOT NULL,
				method TEXT NOT NULL,
				path TEXT NOT NULL,
				query_string TEXT NOT NULL DEFAULT '',
				headers JSONB,
				body JSONB,
				client_ip TEXT NOT NULL DEFAULT '',
				received_at TIMESTAMPTZ NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_downstream_requests_request_id ON downstream_requests(request_id)`,
			`CREATE INDEX IF NOT EXISTS idx_downstream_requests_api_key_id ON downstream_requests(api_key_id)`,
			`CREATE INDEX IF NOT EXISTS idx_downstream_requests_received_at ON downstream_requests(received_at)`,

			`CREATE TABLE IF NOT EXISTS upstream_attempts (
				id TEXT PRIMARY KEY,
				request_id TEXT NOT NULL,
				attempt_number INTEGER NOT NULL,
				upstream_action TEXT NOT NULL,
				request_headers JSONB,
				request_body JSONB,
				response_status INTEGER NOT NULL,
				response_headers JSONB,
				response_body JSONB,
				latency_ms BIGINT NOT NULL,
				error TEXT,
				sent_at TIMESTAMPTZ NOT NULL,
				UNIQUE(request_id, attempt_number)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_upstream_attempts_request_id ON upstream_attempts(request_id)`,
			`CREATE INDEX IF NOT EXISTS idx_upstream_attempts_sent_at ON upstream_attempts(sent_at)`,

			`CREATE TABLE IF NOT EXISTS audit_events (
				id TEXT PRIMARY KEY,
				request_id TEXT NOT NULL,
				event_type TEXT NOT NULL,
				actor TEXT NOT NULL,
				action TEXT NOT NULL,
				resource TEXT NOT NULL,
				metadata JSONB,
				created_at TIMESTAMPTZ NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_audit_events_request_id ON audit_events(request_id)`,
			`CREATE INDEX IF NOT EXISTS idx_audit_events_created_at ON audit_events(created_at)`,

			`CREATE TABLE IF NOT EXISTS idempotency_records (
				id TEXT PRIMARY KEY,
				idempotency_key TEXT NOT NULL UNIQUE,
				request_hash TEXT NOT NULL,
				response_status INTEGER NOT NULL,
				response_body JSONB,
				created_at TIMESTAMPTZ NOT NULL,
				expires_at TIMESTAMPTZ NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_idempotency_records_expires_at ON idempotency_records(expires_at)`,
		},
	},
	{
		version: 2,
		name:    "api_key_secret_ciphertext",
		statements: []string{
			`ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS secret_key_ciphertext TEXT NOT NULL DEFAULT ''`,
		},
	},
	{
		version: 3,
		name:    "billing_schema",
		statements: []string{
			`ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS multiplier INTEGER NOT NULL DEFAULT 10000`,


			`CREATE TABLE IF NOT EXISTS pricing_presets (
				preset TEXT PRIMARY KEY,
				credit_per_second DECIMAL(10,4) NOT NULL,
				description TEXT,
				created_at TIMESTAMPTZ NOT NULL,
				updated_at TIMESTAMPTZ NOT NULL
			)`,

			`CREATE TABLE IF NOT EXISTS key_budgets (
				id TEXT PRIMARY KEY,
				api_key_id TEXT UNIQUE REFERENCES api_keys(id),
				credits_remaining DECIMAL(20,4) NOT NULL DEFAULT 0,
				credits_reserved DECIMAL(20,4) NOT NULL DEFAULT 0,
				created_at TIMESTAMPTZ NOT NULL,
				updated_at TIMESTAMPTZ NOT NULL
			)`,

			`CREATE TABLE IF NOT EXISTS billing_ledger (
				id TEXT PRIMARY KEY,
				api_key_id TEXT NOT NULL REFERENCES api_keys(id),
				request_id TEXT NOT NULL,
				idempotency_key TEXT,
				preset TEXT NOT NULL,
				base_cost DECIMAL(10,4) NOT NULL,
				multiplier INTEGER NOT NULL,
				duration_seconds INTEGER,
				total_cost DECIMAL(10,4) NOT NULL,
				status TEXT NOT NULL,
				pricing_snapshot JSONB,
				created_at TIMESTAMPTZ NOT NULL,
				settled_at TIMESTAMPTZ,
				UNIQUE(request_id)
			)`,
		},
	},
	{
		version: 4,
		name:    "admin_auth_schema",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS admin_users (
				id TEXT PRIMARY KEY,
				email TEXT NOT NULL UNIQUE,
				password_hash TEXT NOT NULL,
				created_at TIMESTAMPTZ NOT NULL,
				updated_at TIMESTAMPTZ NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_admin_users_email ON admin_users(email)`,

			`CREATE TABLE IF NOT EXISTS admin_sessions (
				id TEXT PRIMARY KEY,
				admin_user_id TEXT NOT NULL REFERENCES admin_users(id),
				token_hash TEXT NOT NULL UNIQUE,
				created_at TIMESTAMPTZ NOT NULL,
				expires_at TIMESTAMPTZ NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_admin_sessions_admin_user_id ON admin_sessions(admin_user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_admin_sessions_token_hash ON admin_sessions(token_hash)`,
			`CREATE INDEX IF NOT EXISTS idx_admin_sessions_expires_at ON admin_sessions(expires_at)`,

			`CREATE TABLE IF NOT EXISTS password_reset_tokens (
				id TEXT PRIMARY KEY,
				admin_user_id TEXT NOT NULL REFERENCES admin_users(id),
				token_hash TEXT NOT NULL UNIQUE,
				created_at TIMESTAMPTZ NOT NULL,
				expires_at TIMESTAMPTZ NOT NULL,
				used_at TIMESTAMPTZ
			)`,
			`CREATE INDEX IF NOT EXISTS idx_password_reset_tokens_admin_user_id ON password_reset_tokens(admin_user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_password_reset_tokens_token_hash ON password_reset_tokens(token_hash)`,
			`CREATE INDEX IF NOT EXISTS idx_password_reset_tokens_expires_at ON password_reset_tokens(expires_at)`,
		},
	},
	{
		version: 5,
		name:    "billing_ledger_request_id_unique",
		statements: []string{
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_billing_ledger_request_id ON billing_ledger(request_id)`,
		},
	},
	{
		version: 6,
		name:    "default_multiplier_10000",
		statements: []string{
			`ALTER TABLE api_keys ALTER COLUMN multiplier SET DEFAULT 10000`,
			`UPDATE api_keys SET multiplier = 10000 WHERE multiplier = 1`,
		},
	},
}


func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return internalerrors.New(internalerrors.ErrDatabaseError, "postgres pool is nil", nil)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return internalerrors.New(internalerrors.ErrDatabaseError, "begin migration transaction", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
			return
		}
	}()

	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`); err != nil {
		return internalerrors.New(internalerrors.ErrDatabaseError, "create schema_migrations", err)
	}

	applied := map[int]bool{}
	rows, err := tx.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return internalerrors.New(internalerrors.ErrDatabaseError, "list applied migrations", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return internalerrors.New(internalerrors.ErrDatabaseError, "scan applied migrations", err)
		}
		applied[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return internalerrors.New(internalerrors.ErrDatabaseError, "iterate applied migrations", err)
	}

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		for i, stmt := range m.statements {
			if stmt == "" {
				continue
			}
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return internalerrors.New(internalerrors.ErrDatabaseError, fmt.Sprintf("apply migration %d (%s) statement %d", m.version, m.name, i+1), err)
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, m.version, m.name); err != nil {
			return internalerrors.New(internalerrors.ErrDatabaseError, fmt.Sprintf("record migration %d (%s)", m.version, m.name), err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return internalerrors.New(internalerrors.ErrDatabaseError, "commit migrations", err)
	}
	return nil
}
