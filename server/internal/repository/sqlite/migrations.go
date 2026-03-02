package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

var migrationStatements = []string{
	`CREATE TABLE IF NOT EXISTS api_keys (
		id TEXT PRIMARY KEY,
		access_key TEXT NOT NULL,
		secret_key_hash TEXT NOT NULL,
		secret_key_ciphertext TEXT NOT NULL,
		description TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		expires_at TEXT,
		revoked_at TEXT,
		rotation_of TEXT,
		status TEXT NOT NULL
	);`,
	`ALTER TABLE api_keys ADD COLUMN secret_key_ciphertext TEXT NOT NULL DEFAULT '';`,
	`ALTER TABLE api_keys ADD COLUMN multiplier INTEGER NOT NULL DEFAULT 10000;`,

	`CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_access_key ON api_keys(access_key);`,

	`CREATE TABLE IF NOT EXISTS pricing_presets (
		preset TEXT PRIMARY KEY,
		credit_per_second DECIMAL(10,4) NOT NULL,
		description TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);`,

	`CREATE TABLE IF NOT EXISTS key_budgets (
		id TEXT PRIMARY KEY,
		api_key_id TEXT UNIQUE REFERENCES api_keys(id),
		credits_remaining DECIMAL(20,4) NOT NULL DEFAULT 0,
		credits_reserved DECIMAL(20,4) NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);`,

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
		pricing_snapshot TEXT,
		created_at TEXT NOT NULL,
		settled_at TEXT,
		UNIQUE(request_id)
	);`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_billing_ledger_request_id ON billing_ledger(request_id);`,

	`CREATE TABLE IF NOT EXISTS admin_users (
		id TEXT PRIMARY KEY,
		email TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);`,
	`CREATE INDEX IF NOT EXISTS idx_admin_users_email ON admin_users(email);`,

	`CREATE TABLE IF NOT EXISTS admin_sessions (
		id TEXT PRIMARY KEY,
		admin_user_id TEXT NOT NULL REFERENCES admin_users(id),
		token_hash TEXT NOT NULL UNIQUE,
		created_at TEXT NOT NULL,
		expires_at TEXT NOT NULL
	);`,
	`CREATE INDEX IF NOT EXISTS idx_admin_sessions_expires_at ON admin_sessions(expires_at);`,

	`CREATE TABLE IF NOT EXISTS password_reset_tokens (
		id TEXT PRIMARY KEY,
		admin_user_id TEXT NOT NULL REFERENCES admin_users(id),
		token_hash TEXT NOT NULL UNIQUE,
		created_at TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		used_at TEXT
	);`,
	`CREATE INDEX IF NOT EXISTS idx_password_reset_tokens_expires_at ON password_reset_tokens(expires_at);`,

	`CREATE TABLE IF NOT EXISTS downstream_requests (
		id TEXT PRIMARY KEY,
		request_id TEXT NOT NULL,
		api_key_id TEXT NOT NULL,
		action TEXT NOT NULL,
		method TEXT NOT NULL,
		path TEXT NOT NULL,
		query_string TEXT,
		headers TEXT,
		body TEXT,
		client_ip TEXT,
		received_at TEXT NOT NULL
	);`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_downstream_requests_request_id ON downstream_requests(request_id);`,

	`CREATE TABLE IF NOT EXISTS upstream_attempts (
		id TEXT PRIMARY KEY,
		request_id TEXT NOT NULL,
		attempt_number INTEGER NOT NULL,
		upstream_action TEXT NOT NULL,
		request_headers TEXT,
		request_body TEXT,
		response_status INTEGER NOT NULL,
		response_headers TEXT,
		response_body TEXT,
		latency_ms INTEGER NOT NULL,
		error TEXT,
		sent_at TEXT NOT NULL
	);`,
	`CREATE INDEX IF NOT EXISTS idx_upstream_attempts_request_id ON upstream_attempts(request_id);`,

	`CREATE TABLE IF NOT EXISTS audit_events (
		id TEXT PRIMARY KEY,
		request_id TEXT NOT NULL,
		event_type TEXT NOT NULL,
		actor TEXT,
		action TEXT NOT NULL,
		resource TEXT NOT NULL,
		metadata TEXT,
		created_at TEXT NOT NULL
	);`,
	`CREATE INDEX IF NOT EXISTS idx_audit_events_request_id_created_at ON audit_events(request_id, created_at);`,

	`CREATE TABLE IF NOT EXISTS idempotency_records (
		id TEXT PRIMARY KEY,
		idempotency_key TEXT NOT NULL,
		request_hash TEXT NOT NULL,
		response_status INTEGER NOT NULL,
		response_body TEXT,
		created_at TEXT NOT NULL,
		expires_at TEXT NOT NULL
	);`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_idempotency_records_idempotency_key ON idempotency_records(idempotency_key);`,

	`CREATE TABLE IF NOT EXISTS admin_users (
		id TEXT PRIMARY KEY,
		email TEXT NOT NULL,
		password_hash TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_admin_users_email ON admin_users(email);`,

	`CREATE TABLE IF NOT EXISTS admin_sessions (
		id TEXT PRIMARY KEY,
		admin_user_id TEXT NOT NULL REFERENCES admin_users(id),
		token_hash TEXT NOT NULL,
		created_at TEXT NOT NULL,
		expires_at TEXT NOT NULL
	);`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_admin_sessions_token_hash ON admin_sessions(token_hash);`,
	`CREATE INDEX IF NOT EXISTS idx_admin_sessions_expires_at ON admin_sessions(expires_at);`,
	`CREATE INDEX IF NOT EXISTS idx_admin_sessions_admin_user_id ON admin_sessions(admin_user_id);`,

	`CREATE TABLE IF NOT EXISTS password_reset_tokens (
		id TEXT PRIMARY KEY,
		admin_user_id TEXT NOT NULL REFERENCES admin_users(id),
		token_hash TEXT NOT NULL,
		created_at TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		used_at TEXT
	);`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_password_reset_tokens_token_hash ON password_reset_tokens(token_hash);`,
	`CREATE INDEX IF NOT EXISTS idx_password_reset_tokens_expires_at ON password_reset_tokens(expires_at);`,
	`CREATE INDEX IF NOT EXISTS idx_password_reset_tokens_admin_user_id ON password_reset_tokens(admin_user_id);`,
}



func ApplyMigrations(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return
		}
	}()

	for i, stmt := range migrationStatements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			if isSQLiteDuplicateColumn(err) {
				continue
			}
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

func isSQLiteDuplicateColumn(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "duplicate column name")
}
