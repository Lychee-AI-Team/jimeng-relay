package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
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

const maxUpsertPricingBodyBytes int64 = 1 << 20

type pricingPresetMeta struct {
	ImageCost decimal.Decimal `json:"image_cost"`
}

type upsertPricingRequest struct {
	Preset             string          `json:"preset"`
	ImageCost          decimal.Decimal `json:"image_cost"`
	VideoCostPerSecond decimal.Decimal `json:"video_cost_per_second"`
}

type pricingResponse struct {
	Preset             string          `json:"preset"`
	ImageCost          decimal.Decimal `json:"image_cost"`
	VideoCostPerSecond decimal.Decimal `json:"video_cost_per_second"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

func UpsertPricing(w http.ResponseWriter, r *http.Request) {
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

	r.Body = http.MaxBytesReader(w, r.Body, maxUpsertPricingBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var req upsertPricingRequest
	if err := dec.Decode(&req); err != nil {
		writeError(w, internalerrors.New(internalerrors.ErrValidationFailed, "invalid json body", err), http.StatusBadRequest)
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, internalerrors.New(internalerrors.ErrValidationFailed, "invalid json body", nil), http.StatusBadRequest)
		return
	}

	req.Preset = strings.TrimSpace(req.Preset)
	if req.Preset == "" {
		writeError(w, internalerrors.New(internalerrors.ErrValidationFailed, "preset is required", nil), http.StatusBadRequest)
		return
	}
	if req.ImageCost.IsNegative() {
		writeError(w, internalerrors.New(internalerrors.ErrValidationFailed, "image_cost must be zero or positive", nil), http.StatusBadRequest)
		return
	}
	if req.VideoCostPerSecond.IsNegative() {
		writeError(w, internalerrors.New(internalerrors.ErrValidationFailed, "video_cost_per_second must be zero or positive", nil), http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	repo, cleanup, err := openPricingPresetRepository(ctx)
	if err != nil {
		writeError(w, err, statusFromError(err))
		return
	}
	defer cleanup()

	now := time.Now().UTC()
	metaJSON, err := json.Marshal(pricingPresetMeta{ImageCost: req.ImageCost})
	if err != nil {
		writeError(w, internalerrors.New(internalerrors.ErrInternalError, "encode pricing preset", err), http.StatusInternalServerError)
		return
	}

	preset := models.PricingPreset{
		Preset:         req.Preset,
		Description:    string(metaJSON),
		PricePerCredit: req.VideoCostPerSecond,
		Currency:       "CREDITS",
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := repo.Upsert(ctx, preset); err != nil {
		writeError(w, internalerrors.New(internalerrors.ErrDatabaseError, "upsert pricing preset", err), http.StatusInternalServerError)
		return
	}

	stored, err := repo.Get(ctx, req.Preset)
	if err != nil {
		writeError(w, internalerrors.New(internalerrors.ErrDatabaseError, "get pricing preset", err), http.StatusInternalServerError)
		return
	}

	imageCost := decimal.Zero
	if strings.TrimSpace(stored.Description) != "" {
		var meta pricingPresetMeta
		if err := json.Unmarshal([]byte(stored.Description), &meta); err == nil {
			imageCost = meta.ImageCost
		}
	}

	writeJSON(w, http.StatusOK, pricingResponse{
		Preset:             stored.Preset,
		ImageCost:          imageCost,
		VideoCostPerSecond: stored.PricePerCredit,
		CreatedAt:          stored.CreatedAt,
		UpdatedAt:          stored.UpdatedAt,
	})
}

func ListPricing(w http.ResponseWriter, r *http.Request) {
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

	ctx := r.Context()
	repo, cleanup, err := openPricingPresetRepository(ctx)
	if err != nil {
		writeError(w, err, statusFromError(err))
		return
	}
	defer cleanup()

	items, err := repo.List(ctx)
	if err != nil {
		writeError(w, internalerrors.New(internalerrors.ErrDatabaseError, "list pricing presets", err), http.StatusInternalServerError)
		return
	}

	out := make([]pricingResponse, 0, len(items))
	for _, it := range items {
		imageCost := decimal.Zero
		if strings.TrimSpace(it.Description) != "" {
			var meta pricingPresetMeta
			if err := json.Unmarshal([]byte(it.Description), &meta); err == nil {
				imageCost = meta.ImageCost
			}
		}
		out = append(out, pricingResponse{
			Preset:             it.Preset,
			ImageCost:          imageCost,
			VideoCostPerSecond: it.PricePerCredit,
			CreatedAt:          it.CreatedAt,
			UpdatedAt:          it.UpdatedAt,
		})
	}

	writeJSON(w, http.StatusOK, out)
}

func openPricingPresetRepository(ctx context.Context) (repository.PricingPresetRepository, func(), error) {
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
			return nil, func() {}, internalerrors.New(internalerrors.ErrDatabaseError, "open sqlite repository", err)
		}
		cleanup := func() { _ = repos.Close() }
		return &pricingPresetSQLiteRepo{db: repos.DB}, cleanup, nil
	case "postgres", "postgresql":
		cfg, err := pgxpool.ParseConfig(dbURL)
		if err != nil {
			return nil, func() {}, internalerrors.New(internalerrors.ErrDatabaseError, "parse DATABASE_URL", err)
		}
		cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeCacheStatement
		cfg.ConnConfig.StatementCacheCapacity = 512

		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			return nil, func() {}, internalerrors.New(internalerrors.ErrDatabaseError, "create postgres pool", err)
		}
		cleanup := func() { pool.Close() }
		if err := pool.Ping(ctx); err != nil {
			cleanup()
			return nil, func() {}, internalerrors.New(internalerrors.ErrDatabaseError, "ping postgres", err)
		}
		if err := postgres.Migrate(ctx, pool); err != nil {
			cleanup()
			return nil, func() {}, err
		}
		return &pricingPresetPostgresRepo{pool: pool}, cleanup, nil
	default:
		return nil, func() {}, internalerrors.New(internalerrors.ErrInternalError, "unsupported database type: "+dbType, nil)
	}
}

type pricingPresetSQLiteRepo struct {
	db *sql.DB
}

var _ repository.PricingPresetRepository = (*pricingPresetSQLiteRepo)(nil)

func (r *pricingPresetSQLiteRepo) Upsert(ctx context.Context, preset models.PricingPreset) error {
	if r == nil || r.db == nil {
		return internalerrors.New(internalerrors.ErrDatabaseError, "sqlite db is not configured", nil)
	}
	preset.Preset = strings.TrimSpace(preset.Preset)
	if preset.Preset == "" {
		return internalerrors.New(internalerrors.ErrValidationFailed, "preset is required", nil)
	}
	if preset.PricePerCredit.IsNegative() {
		return internalerrors.New(internalerrors.ErrValidationFailed, "video_cost_per_second must be zero or positive", nil)
	}
	if preset.CreatedAt.IsZero() {
		preset.CreatedAt = time.Now().UTC()
	}
	if preset.UpdatedAt.IsZero() {
		preset.UpdatedAt = preset.CreatedAt
	}

	_, err := r.db.ExecContext(ctx,
		`INSERT INTO pricing_presets (preset, credit_per_second, description, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(preset) DO UPDATE SET
			credit_per_second = excluded.credit_per_second,
			description = excluded.description,
			updated_at = excluded.updated_at;`,
		preset.Preset,
		preset.PricePerCredit.String(),
		nullableString(preset.Description),
		formatTime(preset.CreatedAt),
		formatTime(preset.UpdatedAt),
	)
	if err != nil {
		return err
	}
	return nil
}

func (r *pricingPresetSQLiteRepo) Get(ctx context.Context, preset string) (models.PricingPreset, error) {
	if r == nil || r.db == nil {
		return models.PricingPreset{}, internalerrors.New(internalerrors.ErrDatabaseError, "sqlite db is not configured", nil)
	}
	preset = strings.TrimSpace(preset)
	if preset == "" {
		return models.PricingPreset{}, internalerrors.New(internalerrors.ErrValidationFailed, "preset is required", nil)
	}

	var out models.PricingPreset
	var creditPerSecond string
	var description sql.NullString
	var createdAt string
	var updatedAt string

	if err := r.db.QueryRowContext(ctx,
		`SELECT preset, credit_per_second, description, created_at, updated_at
		 FROM pricing_presets
		 WHERE preset = ?
		 LIMIT 1;`,
		preset,
	).Scan(&out.Preset, &creditPerSecond, &description, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return models.PricingPreset{}, repository.ErrNotFound
		}
		return models.PricingPreset{}, err
	}

	v, err := decimal.NewFromString(strings.TrimSpace(creditPerSecond))
	if err != nil {
		return models.PricingPreset{}, err
	}
	out.PricePerCredit = v
	out.Description = description.String
	out.Currency = "CREDITS"

	parsedCreatedAt, err := parseTime(createdAt)
	if err != nil {
		return models.PricingPreset{}, err
	}
	parsedUpdatedAt, err := parseTime(updatedAt)
	if err != nil {
		return models.PricingPreset{}, err
	}
	out.CreatedAt = parsedCreatedAt
	out.UpdatedAt = parsedUpdatedAt

	return out, nil
}

func (r *pricingPresetSQLiteRepo) List(ctx context.Context) ([]models.PricingPreset, error) {
	if r == nil || r.db == nil {
		return nil, internalerrors.New(internalerrors.ErrDatabaseError, "sqlite db is not configured", nil)
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT preset, credit_per_second, description, created_at, updated_at
		 FROM pricing_presets
		 ORDER BY preset ASC;`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.PricingPreset, 0)
	for rows.Next() {
		var p models.PricingPreset
		var creditPerSecond string
		var description sql.NullString
		var createdAt string
		var updatedAt string
		if err := rows.Scan(&p.Preset, &creditPerSecond, &description, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		v, err := decimal.NewFromString(strings.TrimSpace(creditPerSecond))
		if err != nil {
			return nil, err
		}
		p.PricePerCredit = v
		p.Description = description.String
		p.Currency = "CREDITS"

		parsedCreatedAt, err := parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		parsedUpdatedAt, err := parseTime(updatedAt)
		if err != nil {
			return nil, err
		}
		p.CreatedAt = parsedCreatedAt
		p.UpdatedAt = parsedUpdatedAt
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

type pricingPresetPostgresRepo struct {
	pool *pgxpool.Pool
}

var _ repository.PricingPresetRepository = (*pricingPresetPostgresRepo)(nil)

func (r *pricingPresetPostgresRepo) Upsert(ctx context.Context, preset models.PricingPreset) error {
	if r == nil || r.pool == nil {
		return internalerrors.New(internalerrors.ErrDatabaseError, "postgres pool is not configured", nil)
	}
	preset.Preset = strings.TrimSpace(preset.Preset)
	if preset.Preset == "" {
		return internalerrors.New(internalerrors.ErrValidationFailed, "preset is required", nil)
	}
	if preset.PricePerCredit.IsNegative() {
		return internalerrors.New(internalerrors.ErrValidationFailed, "video_cost_per_second must be zero or positive", nil)
	}
	if preset.CreatedAt.IsZero() {
		preset.CreatedAt = time.Now().UTC()
	}
	if preset.UpdatedAt.IsZero() {
		preset.UpdatedAt = preset.CreatedAt
	}

	_, err := r.pool.Exec(ctx,
		`INSERT INTO pricing_presets (preset, credit_per_second, description, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5)
		 ON CONFLICT(preset) DO UPDATE SET
			credit_per_second = EXCLUDED.credit_per_second,
			description = EXCLUDED.description,
			updated_at = EXCLUDED.updated_at;`,
		preset.Preset,
		preset.PricePerCredit.String(),
		nullableString(preset.Description),
		preset.CreatedAt.UTC(),
		preset.UpdatedAt.UTC(),
	)
	if err != nil {
		return err
	}
	return nil
}

func (r *pricingPresetPostgresRepo) Get(ctx context.Context, preset string) (models.PricingPreset, error) {
	if r == nil || r.pool == nil {
		return models.PricingPreset{}, internalerrors.New(internalerrors.ErrDatabaseError, "postgres pool is not configured", nil)
	}
	preset = strings.TrimSpace(preset)
	if preset == "" {
		return models.PricingPreset{}, internalerrors.New(internalerrors.ErrValidationFailed, "preset is required", nil)
	}

	var out models.PricingPreset
	var creditPerSecond string
	var description *string
	if err := r.pool.QueryRow(ctx,
		`SELECT preset, credit_per_second, description, created_at, updated_at
		 FROM pricing_presets
		 WHERE preset = $1
		 LIMIT 1;`,
		preset,
	).Scan(&out.Preset, &creditPerSecond, &description, &out.CreatedAt, &out.UpdatedAt); err != nil {
		if err == pgx.ErrNoRows {
			return models.PricingPreset{}, repository.ErrNotFound
		}
		return models.PricingPreset{}, err
	}

	v, err := decimal.NewFromString(strings.TrimSpace(creditPerSecond))
	if err != nil {
		return models.PricingPreset{}, err
	}
	out.PricePerCredit = v
	out.Currency = "CREDITS"
	if description != nil {
		out.Description = *description
	}

	return out, nil
}

func (r *pricingPresetPostgresRepo) List(ctx context.Context) ([]models.PricingPreset, error) {
	if r == nil || r.pool == nil {
		return nil, internalerrors.New(internalerrors.ErrDatabaseError, "postgres pool is not configured", nil)
	}

	rows, err := r.pool.Query(ctx,
		`SELECT preset, credit_per_second, description, created_at, updated_at
		 FROM pricing_presets
		 ORDER BY preset ASC;`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.PricingPreset, 0)
	for rows.Next() {
		var p models.PricingPreset
		var creditPerSecond string
		var description *string
		if err := rows.Scan(&p.Preset, &creditPerSecond, &description, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		v, err := decimal.NewFromString(strings.TrimSpace(creditPerSecond))
		if err != nil {
			return nil, err
		}
		p.PricePerCredit = v
		p.Currency = "CREDITS"
		if description != nil {
			p.Description = *description
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func nullableString(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}
