package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jimeng-relay/server/internal/config"
	internalerrors "github.com/jimeng-relay/server/internal/errors"
	adminmiddleware "github.com/jimeng-relay/server/internal/middleware/admin"
	"github.com/jimeng-relay/server/internal/models"
	"github.com/jimeng-relay/server/internal/repository"
	"github.com/jimeng-relay/server/internal/repository/postgres"
	"github.com/jimeng-relay/server/internal/repository/sqlite"
	"github.com/shopspring/decimal"
)

const (
	maxSetBudgetBodyBytes     int64 = 1 << 20
	maxSetMultiplierBodyBytes int64 = 1 << 20
)

func SetBudget(w http.ResponseWriter, r *http.Request) {
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

	r.Body = http.MaxBytesReader(w, r.Body, maxSetBudgetBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var payload struct {
		APIKeyID string          `json:"api_key_id"`
		Credits  decimal.Decimal `json:"credits"`
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

	apiKeyID := strings.TrimSpace(payload.APIKeyID)
	if apiKeyID == "" {
		writeError(w, internalerrors.New(internalerrors.ErrValidationFailed, "api_key_id is required", nil), http.StatusBadRequest)
		return
	}
	if payload.Credits.IsNegative() {
		writeError(w, internalerrors.New(internalerrors.ErrValidationFailed, "credits must be zero or positive", nil), http.StatusBadRequest)
		return
	}

	budget, err := setBudgetCredits(r.Context(), apiKeyID, payload.Credits)
	if err != nil {
		status := statusFromError(err)
		if errors.Is(err, repository.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, err, status)
		return
	}

	writeJSON(w, http.StatusOK, budget)
}

func SetMultiplier(w http.ResponseWriter, r *http.Request) {
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

	r.Body = http.MaxBytesReader(w, r.Body, maxSetMultiplierBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	dec.UseNumber()

	var payload struct {
		APIKeyID   string      `json:"api_key_id"`
		Multiplier json.Number `json:"multiplier"`
	}
	if err := dec.Decode(&payload); err != nil {
		writeError(w, internalerrors.New(internalerrors.ErrValidationFailed, "invalid json body", err), http.StatusBadRequest)
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, internalerrors.New(internalerrors.ErrValidationFailed, "invalid json body", nil), http.StatusBadRequest)
		return
	}

	apiKeyID := strings.TrimSpace(payload.APIKeyID)
	if apiKeyID == "" {
		writeError(w, internalerrors.New(internalerrors.ErrValidationFailed, "api_key_id is required", nil), http.StatusBadRequest)
		return
	}

	mStr := strings.TrimSpace(payload.Multiplier.String())
	if mStr == "" {
		writeError(w, internalerrors.New(internalerrors.ErrValidationFailed, "multiplier is required", nil), http.StatusBadRequest)
		return
	}
	m64, err := strconv.ParseInt(mStr, 10, 32)
	if err != nil {
		writeError(w, internalerrors.New(internalerrors.ErrValidationFailed, "multiplier must be an integer", err), http.StatusBadRequest)
		return
	}
	multiplier := int(m64)
	if multiplier < 0 || multiplier > 10000 {
		writeError(w, internalerrors.New(internalerrors.ErrValidationFailed, "multiplier must be between 0 and 10000", nil), http.StatusBadRequest)
		return
	}

	if err := setAPIKeyMultiplier(r.Context(), apiKeyID, multiplier); err != nil {
		status := statusFromError(err)
		if errors.Is(err, repository.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, err, status)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"api_key_id": apiKeyID, "multiplier": multiplier})
}

func GetBudget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, internalerrors.New(internalerrors.ErrValidationFailed, "method not allowed", nil), http.StatusMethodNotAllowed)
		return
	}

	adminUser, ok := adminmiddleware.AdminUserFromContext(r.Context())
	if !ok {
		writeError(w, internalerrors.New(internalerrors.ErrAuthFailed, "admin authentication required", nil), http.StatusUnauthorized)
		return
	}
	_ = adminUser

	apiKeyID := strings.TrimSpace(r.URL.Query().Get("api_key_id"))
	if apiKeyID == "" {
		writeError(w, internalerrors.New(internalerrors.ErrValidationFailed, "api_key_id is required", nil), http.StatusBadRequest)
		return
	}

	budget, err := getBudget(r.Context(), apiKeyID)
	if err != nil {
		status := statusFromError(err)
		if errors.Is(err, repository.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, err, status)
		return
	}

	writeJSON(w, http.StatusOK, budget)
}

type adminDBKind string

const (
	adminDBKindSQLite   adminDBKind = "sqlite"
	adminDBKindPostgres adminDBKind = "postgres"
)

type adminDB struct {
	kind   adminDBKind
	sqlite *sql.DB
	pg     *pgxpool.Pool
	close  func()
}

func openAdminDB(ctx context.Context) (*adminDB, error) {
	_ = config.LoadEnvFile(".env")

	dbType := strings.ToLower(strings.TrimSpace(os.Getenv(config.EnvDatabaseType)))
	if dbType == "" {
		dbType = config.DefaultDatabaseType
	}
	dbURL := strings.TrimSpace(os.Getenv(config.EnvDatabaseURL))
	if dbURL == "" {
		dbURL = config.DefaultDatabaseURL
	}

	switch dbType {
	case "", "sqlite":
		repos, err := sqlite.Open(ctx, dbURL)
		if err != nil {
			return nil, internalerrors.New(internalerrors.ErrDatabaseError, "open sqlite repository", err)
		}
		return &adminDB{kind: adminDBKindSQLite, sqlite: repos.DB, close: func() { _ = repos.Close() }}, nil
	case "postgres", "postgresql":
		cfg, err := pgxpool.ParseConfig(dbURL)
		if err != nil {
			return nil, internalerrors.New(internalerrors.ErrDatabaseError, "parse DATABASE_URL", err)
		}
		cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeCacheStatement
		cfg.ConnConfig.StatementCacheCapacity = 512

		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			return nil, internalerrors.New(internalerrors.ErrDatabaseError, "create postgres pool", err)
		}
		cleanup := func() { pool.Close() }
		if err := pool.Ping(ctx); err != nil {
			cleanup()
			return nil, internalerrors.New(internalerrors.ErrDatabaseError, "ping postgres", err)
		}
		if err := postgres.Migrate(ctx, pool); err != nil {
			cleanup()
			return nil, err
		}
		return &adminDB{kind: adminDBKindPostgres, pg: pool, close: cleanup}, nil
	default:
		return nil, internalerrors.New(internalerrors.ErrInternalError, "unsupported database type: "+dbType, nil)
	}
}

func setAPIKeyMultiplier(ctx context.Context, apiKeyID string, multiplier int) error {
	db, err := openAdminDB(ctx)
	if err != nil {
		return err
	}
	defer db.close()

	now := time.Now().UTC()

	switch db.kind {
	case adminDBKindSQLite:
		res, err := db.sqlite.ExecContext(ctx, `UPDATE api_keys SET multiplier = ?, updated_at = ? WHERE id = ?;`, multiplier, now.Format(time.RFC3339Nano), apiKeyID)
		if err != nil {
			return internalerrors.New(internalerrors.ErrDatabaseError, "update api key multiplier", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return internalerrors.New(internalerrors.ErrDatabaseError, "update api key multiplier", err)
		}
		if n == 0 {
			return repository.ErrNotFound
		}
		return nil
	case adminDBKindPostgres:
		tag, err := db.pg.Exec(ctx, `UPDATE api_keys SET multiplier = $2, updated_at = $3 WHERE id = $1;`, apiKeyID, multiplier, now)
		if err != nil {
			return internalerrors.New(internalerrors.ErrDatabaseError, "update api key multiplier", err)
		}
		if tag.RowsAffected() == 0 {
			return repository.ErrNotFound
		}
		return nil
	default:
		return internalerrors.New(internalerrors.ErrInternalError, "unsupported database kind", nil)
	}
}

func setBudgetCredits(ctx context.Context, apiKeyID string, credits decimal.Decimal) (models.KeyBudget, error) {
	db, err := openAdminDB(ctx)
	if err != nil {
		return models.KeyBudget{}, err
	}
	defer db.close()

	now := time.Now().UTC()

	switch db.kind {
	case adminDBKindSQLite:
		return setBudgetCreditsSQLite(ctx, db.sqlite, apiKeyID, credits, now)
	case adminDBKindPostgres:
		return setBudgetCreditsPostgres(ctx, db.pg, apiKeyID, credits, now)
	default:
		return models.KeyBudget{}, internalerrors.New(internalerrors.ErrInternalError, "unsupported database kind", nil)
	}
}

func setBudgetCreditsSQLite(ctx context.Context, db *sql.DB, apiKeyID string, credits decimal.Decimal, now time.Time) (models.KeyBudget, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return models.KeyBudget{}, internalerrors.New(internalerrors.ErrDatabaseError, "begin transaction", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := tx.QueryRowContext(ctx, `SELECT id FROM api_keys WHERE id = ? LIMIT 1;`, apiKeyID).Scan(new(string)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return models.KeyBudget{}, repository.ErrNotFound
		}
		return models.KeyBudget{}, internalerrors.New(internalerrors.ErrDatabaseError, "select api key", err)
	}

	var reservedStr string
	err = tx.QueryRowContext(ctx, `SELECT CAST(credits_reserved AS TEXT) FROM key_budgets WHERE api_key_id = ? LIMIT 1;`, apiKeyID).Scan(&reservedStr)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			_, err = tx.ExecContext(ctx,
				`INSERT INTO key_budgets (id, api_key_id, credits_remaining, credits_reserved, created_at, updated_at) VALUES (?, ?, ?, 0, ?, ?);`,
				"kbud_"+randomHexFallback(8),
				apiKeyID,
				credits.String(),
				now.Format(time.RFC3339Nano),
				now.Format(time.RFC3339Nano),
			)
			if err != nil {
				return models.KeyBudget{}, internalerrors.New(internalerrors.ErrDatabaseError, "insert key budget", err)
			}
		} else {
			return models.KeyBudget{}, internalerrors.New(internalerrors.ErrDatabaseError, "select reserved credits", err)
		}
	} else {
		reserved, err := decimal.NewFromString(strings.TrimSpace(reservedStr))
		if err != nil {
			return models.KeyBudget{}, internalerrors.New(internalerrors.ErrDatabaseError, "parse reserved credits", err)
		}
		if reserved.GreaterThan(credits) {
			return models.KeyBudget{}, internalerrors.New(internalerrors.ErrValidationFailed, "credits must be >= credits_reserved", nil)
		}
		_, err = tx.ExecContext(ctx, `UPDATE key_budgets SET credits_remaining = ?, updated_at = ? WHERE api_key_id = ?;`, credits.String(), now.Format(time.RFC3339Nano), apiKeyID)
		if err != nil {
			return models.KeyBudget{}, internalerrors.New(internalerrors.ErrDatabaseError, "update key budget credits", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return models.KeyBudget{}, internalerrors.New(internalerrors.ErrDatabaseError, "commit transaction", err)
	}

	return getBudgetSQLite(ctx, db, apiKeyID)
}

func setBudgetCreditsPostgres(ctx context.Context, pool *pgxpool.Pool, apiKeyID string, credits decimal.Decimal, now time.Time) (models.KeyBudget, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return models.KeyBudget{}, internalerrors.New(internalerrors.ErrDatabaseError, "begin transaction", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tmp string
	if err := tx.QueryRow(ctx, `SELECT id FROM api_keys WHERE id = $1 LIMIT 1;`, apiKeyID).Scan(&tmp); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.KeyBudget{}, repository.ErrNotFound
		}
		return models.KeyBudget{}, internalerrors.New(internalerrors.ErrDatabaseError, "select api key", err)
	}

	var reservedStr string
	err = tx.QueryRow(ctx, `SELECT credits_reserved::text FROM key_budgets WHERE api_key_id = $1 LIMIT 1 FOR UPDATE;`, apiKeyID).Scan(&reservedStr)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_, err = tx.Exec(ctx,
				`INSERT INTO key_budgets (id, api_key_id, credits_remaining, credits_reserved, created_at, updated_at)
				 VALUES ($1, $2, $3, 0, $4, $4);`,
				"kbud_"+randomHexFallback(8),
				apiKeyID,
				credits.String(),
				now,
			)
			if err != nil {
				return models.KeyBudget{}, internalerrors.New(internalerrors.ErrDatabaseError, "insert key budget", err)
			}
		} else {
			return models.KeyBudget{}, internalerrors.New(internalerrors.ErrDatabaseError, "select reserved credits", err)
		}
	} else {
		reserved, err := decimal.NewFromString(strings.TrimSpace(reservedStr))
		if err != nil {
			return models.KeyBudget{}, internalerrors.New(internalerrors.ErrDatabaseError, "parse reserved credits", err)
		}
		if reserved.GreaterThan(credits) {
			return models.KeyBudget{}, internalerrors.New(internalerrors.ErrValidationFailed, "credits must be >= credits_reserved", nil)
		}
		_, err = tx.Exec(ctx, `UPDATE key_budgets SET credits_remaining = $2, updated_at = $3 WHERE api_key_id = $1;`, apiKeyID, credits.String(), now)
		if err != nil {
			return models.KeyBudget{}, internalerrors.New(internalerrors.ErrDatabaseError, "update key budget credits", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return models.KeyBudget{}, internalerrors.New(internalerrors.ErrDatabaseError, "commit transaction", err)
	}

	return getBudgetPostgres(ctx, pool, apiKeyID)
}

func getBudget(ctx context.Context, apiKeyID string) (models.KeyBudget, error) {
	db, err := openAdminDB(ctx)
	if err != nil {
		return models.KeyBudget{}, err
	}
	defer db.close()

	switch db.kind {
	case adminDBKindSQLite:
		return getBudgetSQLite(ctx, db.sqlite, apiKeyID)
	case adminDBKindPostgres:
		return getBudgetPostgres(ctx, db.pg, apiKeyID)
	default:
		return models.KeyBudget{}, internalerrors.New(internalerrors.ErrInternalError, "unsupported database kind", nil)
	}
}

func getBudgetSQLite(ctx context.Context, db *sql.DB, apiKeyID string) (models.KeyBudget, error) {
	var out models.KeyBudget
	out.APIKeyID = apiKeyID

	var remainingStr string
	var reservedStr string
	var createdAtStr string
	var updatedAtStr string

	err := db.QueryRowContext(ctx,
		`SELECT api_key_id, CAST(credits_remaining AS TEXT), CAST(credits_reserved AS TEXT), created_at, updated_at
		 FROM key_budgets
		 WHERE api_key_id = ?
		 LIMIT 1;`,
		apiKeyID,
	).Scan(&out.APIKeyID, &remainingStr, &reservedStr, &createdAtStr, &updatedAtStr)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return models.KeyBudget{}, repository.ErrNotFound
		}
		return models.KeyBudget{}, internalerrors.New(internalerrors.ErrDatabaseError, "select key budget", err)
	}

	rem, err := decimal.NewFromString(strings.TrimSpace(remainingStr))
	if err != nil {
		return models.KeyBudget{}, internalerrors.New(internalerrors.ErrDatabaseError, "parse credits_remaining", err)
	}
	res, err := decimal.NewFromString(strings.TrimSpace(reservedStr))
	if err != nil {
		return models.KeyBudget{}, internalerrors.New(internalerrors.ErrDatabaseError, "parse credits_reserved", err)
	}
	createdAt, err := parseTime(createdAtStr)
	if err != nil {
		return models.KeyBudget{}, internalerrors.New(internalerrors.ErrDatabaseError, "parse created_at", err)
	}
	updatedAt, err := parseTime(updatedAtStr)
	if err != nil {
		return models.KeyBudget{}, internalerrors.New(internalerrors.ErrDatabaseError, "parse updated_at", err)
	}

	out.CreditsAvailable = rem
	out.CreditsReserved = res
	out.CreatedAt = createdAt
	out.UpdatedAt = updatedAt

	return out, nil
}

func getBudgetPostgres(ctx context.Context, pool *pgxpool.Pool, apiKeyID string) (models.KeyBudget, error) {
	var out models.KeyBudget
	var remainingStr string
	var reservedStr string

	err := pool.QueryRow(ctx,
		`SELECT api_key_id, credits_remaining::text, credits_reserved::text, created_at, updated_at
		 FROM key_budgets
		 WHERE api_key_id = $1
		 LIMIT 1;`,
		apiKeyID,
	).Scan(&out.APIKeyID, &remainingStr, &reservedStr, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.KeyBudget{}, repository.ErrNotFound
		}
		return models.KeyBudget{}, internalerrors.New(internalerrors.ErrDatabaseError, "select key budget", err)
	}

	rem, err := decimal.NewFromString(strings.TrimSpace(remainingStr))
	if err != nil {
		return models.KeyBudget{}, internalerrors.New(internalerrors.ErrDatabaseError, "parse credits_remaining", err)
	}
	res, err := decimal.NewFromString(strings.TrimSpace(reservedStr))
	if err != nil {
		return models.KeyBudget{}, internalerrors.New(internalerrors.ErrDatabaseError, "parse credits_reserved", err)
	}
	out.CreditsAvailable = rem
	out.CreditsReserved = res

	return out, nil
}

func parseTime(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, errors.New("time is empty")
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err == nil {
		return t, nil
	}
	t, err2 := time.Parse(time.RFC3339, v)
	if err2 == nil {
		return t, nil
	}
	return time.Time{}, err
}
