package billing

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jimeng-relay/server/internal/config"
	internalerrors "github.com/jimeng-relay/server/internal/errors"
	"github.com/jimeng-relay/server/internal/repository"
	"github.com/jimeng-relay/server/internal/repository/postgres"
	"github.com/jimeng-relay/server/internal/repository/sqlite"
	"github.com/shopspring/decimal"
)

const (
	requestTypeImage = "image"
	requestTypeVideo = "video"

	defaultVideoFrames   = 121
	defaultVideoDuration = 5
)

var errInsufficientBudget = errors.New("insufficient budget")

const (
	ledgerStatusPreAuth  = "preauth"
	ledgerStatusSettled  = "settled"
	ledgerStatusReleased = "released"
)

type contextKey string

const preAuthContextKey contextKey = "billing_preauth"

type Config struct {
	Now func() time.Time
}

type Service struct {
	now func() time.Time
}

type PreAuthResult struct {
	LedgerID        string
	RequestID       string
	APIKeyID        string
	Preset          string
	RequestType     string
	EstimatedCost   decimal.Decimal
	DurationSeconds int
	Multiplier      int
}

func NewService(cfg Config) *Service {
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{now: now}
}

func WithPreAuthResult(ctx context.Context, result PreAuthResult) context.Context {
	return context.WithValue(ctx, preAuthContextKey, result)
}

func PreAuthResultFromContext(ctx context.Context) (PreAuthResult, bool) {
	v, ok := ctx.Value(preAuthContextKey).(PreAuthResult)
	return v, ok
}

func IsInsufficientBudget(err error) bool {
	return errors.Is(err, errInsufficientBudget)
}

func (s *Service) PreAuthorize(ctx context.Context, requestID, apiKeyID string, requestBody []byte) (PreAuthResult, error) {
	requestID = strings.TrimSpace(requestID)
	apiKeyID = strings.TrimSpace(apiKeyID)
	if requestID == "" {
		return PreAuthResult{}, internalerrors.New(internalerrors.ErrValidationFailed, "request_id is required", nil)
	}
	if apiKeyID == "" {
		return PreAuthResult{}, internalerrors.New(internalerrors.ErrValidationFailed, "api_key_id is required", nil)
	}

	req, err := parseRequestMeta(requestBody)
	if err != nil {
		return PreAuthResult{}, err
	}

	db, err := openBillingDB(ctx)
	if err != nil {
		return PreAuthResult{}, err
	}
	defer db.close()

	now := s.now()
	result := PreAuthResult{
		RequestID:       requestID,
		APIKeyID:        apiKeyID,
		Preset:          req.Preset,
		RequestType:     req.RequestType,
		DurationSeconds: req.DurationSeconds,
	}

	switch db.kind {
	case billingDBKindSQLite:
		return s.preAuthorizeSQLite(ctx, db.sqlite, now, req, result)
	case billingDBKindPostgres:
		return s.preAuthorizePostgres(ctx, db.pg, now, req, result)
	default:
		return PreAuthResult{}, internalerrors.New(internalerrors.ErrInternalError, "unsupported database kind", nil)
	}
}

func (s *Service) Settle(ctx context.Context, requestID, apiKeyID string) error {
	return s.transitionPreAuth(ctx, requestID, apiKeyID, ledgerStatusSettled)
}

func (s *Service) Release(ctx context.Context, requestID, apiKeyID string) error {
	return s.transitionPreAuth(ctx, requestID, apiKeyID, ledgerStatusReleased)
}

type settlementLedger struct {
	TotalCost decimal.Decimal
	Status    string
}

type existingPreAuthLedger struct {
	LedgerID        string
	APIKeyID        string
	RequestID       string
	Preset          string
	DurationSeconds int
	Multiplier      int
	EstimatedCost   decimal.Decimal
}

func (s *Service) transitionPreAuth(ctx context.Context, requestID, apiKeyID, targetStatus string) error {
	requestID = strings.TrimSpace(requestID)
	apiKeyID = strings.TrimSpace(apiKeyID)
	if requestID == "" {
		return internalerrors.New(internalerrors.ErrValidationFailed, "request_id is required", nil)
	}
	if apiKeyID == "" {
		return internalerrors.New(internalerrors.ErrValidationFailed, "api_key_id is required", nil)
	}
	if targetStatus != ledgerStatusSettled && targetStatus != ledgerStatusReleased {
		return internalerrors.New(internalerrors.ErrInternalError, "unsupported ledger target status", nil)
	}

	db, err := openBillingDB(ctx)
	if err != nil {
		return err
	}
	defer db.close()

	now := s.now()
	switch db.kind {
	case billingDBKindSQLite:
		return transitionPreAuthSQLite(ctx, db.sqlite, now, requestID, apiKeyID, targetStatus)
	case billingDBKindPostgres:
		return transitionPreAuthPostgres(ctx, db.pg, now, requestID, apiKeyID, targetStatus)
	default:
		return internalerrors.New(internalerrors.ErrInternalError, "unsupported database kind", nil)
	}
}

func transitionPreAuthSQLite(ctx context.Context, db *sql.DB, now time.Time, requestID, apiKeyID, targetStatus string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return internalerrors.New(internalerrors.ErrDatabaseError, "begin transaction", err)
	}
	defer func() { _ = tx.Rollback() }()

	ledger, found, err := getLedgerForTransitionSQLite(ctx, tx, requestID, apiKeyID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	currentStatus := strings.ToLower(strings.TrimSpace(ledger.Status))
	if currentStatus == targetStatus || currentStatus != ledgerStatusPreAuth {
		return nil
	}

	totalCost := ledger.TotalCost.String()
	if targetStatus == ledgerStatusSettled {
		if _, err := tx.ExecContext(ctx,
			`UPDATE key_budgets
			 SET credits_reserved = CASE WHEN credits_reserved >= ? THEN credits_reserved - ? ELSE 0 END,
			     credits_remaining = credits_remaining - ?,
			     updated_at = ?
			 WHERE api_key_id = ?;`,
			totalCost, totalCost, totalCost, now.Format(time.RFC3339Nano), apiKeyID,
		); err != nil {
			return internalerrors.New(internalerrors.ErrDatabaseError, "settle reserved credits", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE billing_ledger
			 SET status = ?, settled_at = ?
			 WHERE request_id = ? AND api_key_id = ? AND status = ?;`,
			ledgerStatusSettled,
			now.Format(time.RFC3339Nano),
			requestID,
			apiKeyID,
			ledgerStatusPreAuth,
		); err != nil {
			return internalerrors.New(internalerrors.ErrDatabaseError, "mark billing ledger settled", err)
		}
	} else {
		if _, err := tx.ExecContext(ctx,
			`UPDATE key_budgets
			 SET credits_reserved = CASE WHEN credits_reserved >= ? THEN credits_reserved - ? ELSE 0 END,
			     updated_at = ?
			 WHERE api_key_id = ?;`,
			totalCost, totalCost, now.Format(time.RFC3339Nano), apiKeyID,
		); err != nil {
			return internalerrors.New(internalerrors.ErrDatabaseError, "release reserved credits", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE billing_ledger
			 SET status = ?, settled_at = NULL
			 WHERE request_id = ? AND api_key_id = ? AND status = ?;`,
			ledgerStatusReleased,
			requestID,
			apiKeyID,
			ledgerStatusPreAuth,
		); err != nil {
			return internalerrors.New(internalerrors.ErrDatabaseError, "mark billing ledger released", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return internalerrors.New(internalerrors.ErrDatabaseError, "commit transaction", err)
	}
	return nil
}

func getLedgerForTransitionSQLite(ctx context.Context, tx *sql.Tx, requestID, apiKeyID string) (settlementLedger, bool, error) {
	var totalCost string
	var status string
	err := tx.QueryRowContext(ctx,
		`SELECT CAST(total_cost AS TEXT), status
		 FROM billing_ledger
		 WHERE request_id = ? AND api_key_id = ?
		 LIMIT 1;`,
		requestID,
		apiKeyID,
	).Scan(&totalCost, &status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return settlementLedger{}, false, nil
		}
		return settlementLedger{}, false, internalerrors.New(internalerrors.ErrDatabaseError, "select billing ledger", err)
	}
	amount, err := decimal.NewFromString(strings.TrimSpace(totalCost))
	if err != nil {
		return settlementLedger{}, false, internalerrors.New(internalerrors.ErrDatabaseError, "parse billing total_cost", err)
	}
	return settlementLedger{TotalCost: amount, Status: status}, true, nil
}

func transitionPreAuthPostgres(ctx context.Context, pool *pgxpool.Pool, now time.Time, requestID, apiKeyID, targetStatus string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return internalerrors.New(internalerrors.ErrDatabaseError, "begin transaction", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	ledger, found, err := getLedgerForTransitionPostgres(ctx, tx, requestID, apiKeyID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	currentStatus := strings.ToLower(strings.TrimSpace(ledger.Status))
	if currentStatus == targetStatus || currentStatus != ledgerStatusPreAuth {
		return nil
	}

	totalCost := ledger.TotalCost.String()
	if targetStatus == ledgerStatusSettled {
		if _, err := tx.Exec(ctx,
			`UPDATE key_budgets
			 SET credits_reserved = CASE WHEN credits_reserved >= $2::numeric THEN credits_reserved - $2::numeric ELSE 0 END,
			     credits_remaining = credits_remaining - $2::numeric,
			     updated_at = $3
			 WHERE api_key_id = $1;`,
			apiKeyID,
			totalCost,
			now,
		); err != nil {
			return internalerrors.New(internalerrors.ErrDatabaseError, "settle reserved credits", err)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE billing_ledger
			 SET status = $3, settled_at = $4
			 WHERE request_id = $1 AND api_key_id = $2 AND status = $5;`,
			requestID,
			apiKeyID,
			ledgerStatusSettled,
			now,
			ledgerStatusPreAuth,
		); err != nil {
			return internalerrors.New(internalerrors.ErrDatabaseError, "mark billing ledger settled", err)
		}
	} else {
		if _, err := tx.Exec(ctx,
			`UPDATE key_budgets
			 SET credits_reserved = CASE WHEN credits_reserved >= $2::numeric THEN credits_reserved - $2::numeric ELSE 0 END,
			     updated_at = $3
			 WHERE api_key_id = $1;`,
			apiKeyID,
			totalCost,
			now,
		); err != nil {
			return internalerrors.New(internalerrors.ErrDatabaseError, "release reserved credits", err)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE billing_ledger
			 SET status = $3, settled_at = NULL
			 WHERE request_id = $1 AND api_key_id = $2 AND status = $4;`,
			requestID,
			apiKeyID,
			ledgerStatusReleased,
			ledgerStatusPreAuth,
		); err != nil {
			return internalerrors.New(internalerrors.ErrDatabaseError, "mark billing ledger released", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return internalerrors.New(internalerrors.ErrDatabaseError, "commit transaction", err)
	}
	return nil
}

func getLedgerForTransitionPostgres(ctx context.Context, tx pgx.Tx, requestID, apiKeyID string) (settlementLedger, bool, error) {
	var totalCost string
	var status string
	err := tx.QueryRow(ctx,
		`SELECT total_cost::text, status
		 FROM billing_ledger
		 WHERE request_id = $1 AND api_key_id = $2
		 LIMIT 1
		 FOR UPDATE;`,
		requestID,
		apiKeyID,
	).Scan(&totalCost, &status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return settlementLedger{}, false, nil
		}
		return settlementLedger{}, false, internalerrors.New(internalerrors.ErrDatabaseError, "select billing ledger", err)
	}
	amount, err := decimal.NewFromString(strings.TrimSpace(totalCost))
	if err != nil {
		return settlementLedger{}, false, internalerrors.New(internalerrors.ErrDatabaseError, "parse billing total_cost", err)
	}
	return settlementLedger{TotalCost: amount, Status: status}, true, nil
}

type requestMeta struct {
	Preset          string
	RequestType     string
	DurationSeconds int
}

func parseRequestMeta(body []byte) (requestMeta, error) {
	if len(body) == 0 {
		return requestMeta{}, internalerrors.New(internalerrors.ErrValidationFailed, "request body is required", nil)
	}
	var payload map[string]any
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		return requestMeta{}, internalerrors.New(internalerrors.ErrValidationFailed, "invalid request body", err)
	}
	preset, _ := payload["req_key"].(string)
	preset = strings.TrimSpace(preset)
	if preset == "" {
		return requestMeta{}, internalerrors.New(internalerrors.ErrValidationFailed, "req_key is required", nil)
	}

	meta := requestMeta{Preset: preset, DurationSeconds: defaultVideoDuration, RequestType: requestTypeImage}
	if strings.Contains(preset, "_v30") {
		meta.RequestType = requestTypeVideo
		duration, err := durationSecondsFromPayload(payload)
		if err != nil {
			return requestMeta{}, err
		}
		meta.DurationSeconds = duration
	}
	return meta, nil
}

func durationSecondsFromPayload(payload map[string]any) (int, error) {
	v, ok := payload["frames"]
	if !ok || v == nil {
		return defaultVideoDuration, nil
	}
	frames, err := asInt(v)
	if err != nil {
		return 0, internalerrors.New(internalerrors.ErrValidationFailed, "frames must be an integer", err)
	}
	if frames < 121 || frames > 241 {
		return 0, internalerrors.New(internalerrors.ErrValidationFailed, "frames must be in [121,241]", nil)
	}
	if (frames-1)%24 != 0 {
		return 0, internalerrors.New(internalerrors.ErrValidationFailed, "frames must satisfy 24*n+1", nil)
	}
	return (frames - 1) / 24, nil
}

func asInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int32:
		return int(n), nil
	case int64:
		return int(n), nil
	case float64:
		if n != float64(int(n)) {
			return 0, fmt.Errorf("not an integer")
		}
		return int(n), nil
	case json.Number:
		i, err := strconv.ParseInt(strings.TrimSpace(n.String()), 10, 64)
		if err != nil {
			return 0, err
		}
		return int(i), nil
	default:
		return 0, fmt.Errorf("unsupported type %T", v)
	}
}

type pricingMeta struct {
	ImageCost decimal.Decimal `json:"image_cost"`
}

type pricingRow struct {
	VideoCostPerSecond decimal.Decimal
	ImageCost          decimal.Decimal
}

func parsePricing(imageMeta string, videoCostPerSecond decimal.Decimal) (pricingRow, error) {
	row := pricingRow{VideoCostPerSecond: videoCostPerSecond, ImageCost: decimal.Zero}
	if strings.TrimSpace(imageMeta) == "" {
		return row, nil
	}
	var meta pricingMeta
	if err := json.Unmarshal([]byte(imageMeta), &meta); err != nil {
		return pricingRow{}, err
	}
	row.ImageCost = meta.ImageCost
	return row, nil
}

func estimatedCost(row pricingRow, req requestMeta, multiplier int) decimal.Decimal {
	base := row.ImageCost
	if req.RequestType == requestTypeVideo {
		base = row.VideoCostPerSecond.Mul(decimal.NewFromInt(int64(req.DurationSeconds)))
	}
	m := decimal.NewFromInt(int64(multiplier))
	return base.Mul(m).Div(decimal.NewFromInt(10000))
}

func buildPricingSnapshot(req requestMeta, row pricingRow, multiplier int, estimate decimal.Decimal) (string, error) {
	snapshot := map[string]any{
		"preset":                req.Preset,
		"request_type":          req.RequestType,
		"multiplier":            multiplier,
		"duration_seconds":      req.DurationSeconds,
		"image_cost":            row.ImageCost.String(),
		"video_cost_per_second": row.VideoCostPerSecond.String(),
		"estimated_cost":        estimate.String(),
	}
	b, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (s *Service) preAuthorizeSQLite(ctx context.Context, db *sql.DB, now time.Time, req requestMeta, result PreAuthResult) (PreAuthResult, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return PreAuthResult{}, internalerrors.New(internalerrors.ErrDatabaseError, "begin transaction", err)
	}
	defer func() { _ = tx.Rollback() }()

	existing, found, err := getExistingPreAuthSQLite(ctx, tx, result.RequestID)
	if err != nil {
		return PreAuthResult{}, err
	}
	if found {
		if existing.APIKeyID != result.APIKeyID {
			return PreAuthResult{}, internalerrors.New(internalerrors.ErrValidationFailed, "request_id already exists for another api_key_id", nil)
		}
		return buildPreAuthResultFromExisting(existing), nil
	}

	multiplier, err := getAPIKeyMultiplierSQLite(ctx, tx, result.APIKeyID)
	if err != nil {
		return PreAuthResult{}, err
	}
	pricing, err := getPricingSQLite(ctx, tx, req.Preset)
	if err != nil {
		return PreAuthResult{}, err
	}
	estimate := estimatedCost(pricing, req, multiplier)
	snapshot, err := buildPricingSnapshot(req, pricing, multiplier, estimate)
	if err != nil {
		return PreAuthResult{}, internalerrors.New(internalerrors.ErrInternalError, "encode pricing snapshot", err)
	}

	remaining, reserved, err := getOrCreateBudgetSQLite(ctx, tx, result.APIKeyID, now)
	if err != nil {
		return PreAuthResult{}, err
	}
	if remaining.Sub(reserved).LessThan(estimate) {
		return PreAuthResult{}, internalerrors.New(internalerrors.ErrValidationFailed, "insufficient budget", errInsufficientBudget)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE key_budgets SET credits_reserved = credits_reserved + ?, updated_at = ? WHERE api_key_id = ?;`,
		estimate.String(), now.Format(time.RFC3339Nano), result.APIKeyID,
	); err != nil {
		return PreAuthResult{}, internalerrors.New(internalerrors.ErrDatabaseError, "reserve credits", err)
	}

	baseCost := pricing.ImageCost
	if req.RequestType == requestTypeVideo {
		baseCost = pricing.VideoCostPerSecond.Mul(decimal.NewFromInt(int64(req.DurationSeconds)))
	}
	ledgerID := "bled_" + randomHex(8)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO billing_ledger (id, api_key_id, request_id, idempotency_key, preset, base_cost, multiplier, duration_seconds, total_cost, status, pricing_snapshot, created_at, settled_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL);`,
		ledgerID,
		result.APIKeyID,
		result.RequestID,
		result.RequestID,
		req.Preset,
		baseCost.String(),
		multiplier,
		nullableInt(req.DurationSeconds, req.RequestType == requestTypeVideo),
		estimate.String(),
		"preauth",
		snapshot,
		now.Format(time.RFC3339Nano),
	); err != nil {
		if isSQLiteUniqueViolation(err) {
			existing, found, lookupErr := getExistingPreAuthSQLiteFromDB(ctx, db, result.RequestID)
			if lookupErr != nil {
				return PreAuthResult{}, lookupErr
			}
			if found {
				if existing.APIKeyID != result.APIKeyID {
					return PreAuthResult{}, internalerrors.New(internalerrors.ErrValidationFailed, "request_id already exists for another api_key_id", nil)
				}
				return buildPreAuthResultFromExisting(existing), nil
			}
		}
		return PreAuthResult{}, internalerrors.New(internalerrors.ErrDatabaseError, "create billing preauth ledger", err)
	}

	if err := tx.Commit(); err != nil {
		return PreAuthResult{}, internalerrors.New(internalerrors.ErrDatabaseError, "commit transaction", err)
	}

	result.LedgerID = ledgerID
	result.EstimatedCost = estimate
	result.Multiplier = multiplier
	return result, nil
}

func getAPIKeyMultiplierSQLite(ctx context.Context, tx *sql.Tx, apiKeyID string) (int, error) {
	var multiplier int
	if err := tx.QueryRowContext(ctx, `SELECT multiplier FROM api_keys WHERE id = ? LIMIT 1;`, apiKeyID).Scan(&multiplier); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, internalerrors.New(internalerrors.ErrAuthFailed, "api key not found", repository.ErrNotFound)
		}
		return 0, internalerrors.New(internalerrors.ErrDatabaseError, "select api key multiplier", err)
	}
	return multiplier, nil
}

func getPricingSQLite(ctx context.Context, tx *sql.Tx, preset string) (pricingRow, error) {
	var videoCost string
	var description sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT CAST(credit_per_second AS TEXT), description FROM pricing_presets WHERE preset = ? LIMIT 1;`,
		preset,
	).Scan(&videoCost, &description); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return pricingRow{}, internalerrors.New(internalerrors.ErrValidationFailed, "pricing preset not found", repository.ErrNotFound)
		}
		return pricingRow{}, internalerrors.New(internalerrors.ErrDatabaseError, "select pricing preset", err)
	}
	v, err := decimal.NewFromString(strings.TrimSpace(videoCost))
	if err != nil {
		return pricingRow{}, internalerrors.New(internalerrors.ErrDatabaseError, "parse credit_per_second", err)
	}
	row, err := parsePricing(description.String, v)
	if err != nil {
		return pricingRow{}, internalerrors.New(internalerrors.ErrDatabaseError, "parse pricing description", err)
	}
	return row, nil
}

func getOrCreateBudgetSQLite(ctx context.Context, tx *sql.Tx, apiKeyID string, now time.Time) (decimal.Decimal, decimal.Decimal, error) {
	var remainingStr string
	var reservedStr string
	err := tx.QueryRowContext(ctx,
		`SELECT CAST(credits_remaining AS TEXT), CAST(credits_reserved AS TEXT) FROM key_budgets WHERE api_key_id = ? LIMIT 1;`,
		apiKeyID,
	).Scan(&remainingStr, &reservedStr)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return decimal.Zero, decimal.Zero, internalerrors.New(internalerrors.ErrDatabaseError, "select key budget", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO key_budgets (id, api_key_id, credits_remaining, credits_reserved, created_at, updated_at)
			 VALUES (?, ?, 0, 0, ?, ?);`,
			"kbud_"+randomHex(8),
			apiKeyID,
			now.Format(time.RFC3339Nano),
			now.Format(time.RFC3339Nano),
		); err != nil {
			return decimal.Zero, decimal.Zero, internalerrors.New(internalerrors.ErrDatabaseError, "insert key budget", err)
		}
		return decimal.Zero, decimal.Zero, nil
	}

	remaining, err := decimal.NewFromString(strings.TrimSpace(remainingStr))
	if err != nil {
		return decimal.Zero, decimal.Zero, internalerrors.New(internalerrors.ErrDatabaseError, "parse credits_remaining", err)
	}
	reserved, err := decimal.NewFromString(strings.TrimSpace(reservedStr))
	if err != nil {
		return decimal.Zero, decimal.Zero, internalerrors.New(internalerrors.ErrDatabaseError, "parse credits_reserved", err)
	}
	return remaining, reserved, nil
}

func (s *Service) preAuthorizePostgres(ctx context.Context, pool *pgxpool.Pool, now time.Time, req requestMeta, result PreAuthResult) (PreAuthResult, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return PreAuthResult{}, internalerrors.New(internalerrors.ErrDatabaseError, "begin transaction", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	existing, found, err := getExistingPreAuthPostgres(ctx, tx, result.RequestID)
	if err != nil {
		return PreAuthResult{}, err
	}
	if found {
		if existing.APIKeyID != result.APIKeyID {
			return PreAuthResult{}, internalerrors.New(internalerrors.ErrValidationFailed, "request_id already exists for another api_key_id", nil)
		}
		return buildPreAuthResultFromExisting(existing), nil
	}

	multiplier, err := getAPIKeyMultiplierPostgres(ctx, tx, result.APIKeyID)
	if err != nil {
		return PreAuthResult{}, err
	}
	pricing, err := getPricingPostgres(ctx, tx, req.Preset)
	if err != nil {
		return PreAuthResult{}, err
	}
	estimate := estimatedCost(pricing, req, multiplier)
	snapshot, err := buildPricingSnapshot(req, pricing, multiplier, estimate)
	if err != nil {
		return PreAuthResult{}, internalerrors.New(internalerrors.ErrInternalError, "encode pricing snapshot", err)
	}

	remaining, reserved, err := getOrCreateBudgetPostgres(ctx, tx, result.APIKeyID, now)
	if err != nil {
		return PreAuthResult{}, err
	}
	if remaining.Sub(reserved).LessThan(estimate) {
		return PreAuthResult{}, internalerrors.New(internalerrors.ErrValidationFailed, "insufficient budget", errInsufficientBudget)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE key_budgets SET credits_reserved = credits_reserved + $2, updated_at = $3 WHERE api_key_id = $1;`,
		result.APIKeyID,
		estimate.String(),
		now,
	); err != nil {
		return PreAuthResult{}, internalerrors.New(internalerrors.ErrDatabaseError, "reserve credits", err)
	}

	baseCost := pricing.ImageCost
	if req.RequestType == requestTypeVideo {
		baseCost = pricing.VideoCostPerSecond.Mul(decimal.NewFromInt(int64(req.DurationSeconds)))
	}
	ledgerID := "bled_" + randomHex(8)
	if _, err := tx.Exec(ctx,
		`INSERT INTO billing_ledger (id, api_key_id, request_id, idempotency_key, preset, base_cost, multiplier, duration_seconds, total_cost, status, pricing_snapshot, created_at, settled_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, $12, NULL);`,
		ledgerID,
		result.APIKeyID,
		result.RequestID,
		result.RequestID,
		req.Preset,
		baseCost.String(),
		multiplier,
		nullableInt(req.DurationSeconds, req.RequestType == requestTypeVideo),
		estimate.String(),
		"preauth",
		snapshot,
		now,
	); err != nil {
		if isPostgresUniqueViolation(err) {
			existing, found, lookupErr := getExistingPreAuthPostgresFromPool(ctx, pool, result.RequestID)
			if lookupErr != nil {
				return PreAuthResult{}, lookupErr
			}
			if found {
				if existing.APIKeyID != result.APIKeyID {
					return PreAuthResult{}, internalerrors.New(internalerrors.ErrValidationFailed, "request_id already exists for another api_key_id", nil)
				}
				return buildPreAuthResultFromExisting(existing), nil
			}
		}
		return PreAuthResult{}, internalerrors.New(internalerrors.ErrDatabaseError, "create billing preauth ledger", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return PreAuthResult{}, internalerrors.New(internalerrors.ErrDatabaseError, "commit transaction", err)
	}

	result.LedgerID = ledgerID
	result.EstimatedCost = estimate
	result.Multiplier = multiplier
	return result, nil
}

func getAPIKeyMultiplierPostgres(ctx context.Context, tx pgx.Tx, apiKeyID string) (int, error) {
	var multiplier int
	if err := tx.QueryRow(ctx, `SELECT multiplier FROM api_keys WHERE id = $1 LIMIT 1;`, apiKeyID).Scan(&multiplier); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, internalerrors.New(internalerrors.ErrAuthFailed, "api key not found", repository.ErrNotFound)
		}
		return 0, internalerrors.New(internalerrors.ErrDatabaseError, "select api key multiplier", err)
	}
	return multiplier, nil
}

func getPricingPostgres(ctx context.Context, tx pgx.Tx, preset string) (pricingRow, error) {
	var videoCost string
	var description *string
	if err := tx.QueryRow(ctx,
		`SELECT credit_per_second::text, description FROM pricing_presets WHERE preset = $1 LIMIT 1;`,
		preset,
	).Scan(&videoCost, &description); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pricingRow{}, internalerrors.New(internalerrors.ErrValidationFailed, "pricing preset not found", repository.ErrNotFound)
		}
		return pricingRow{}, internalerrors.New(internalerrors.ErrDatabaseError, "select pricing preset", err)
	}
	v, err := decimal.NewFromString(strings.TrimSpace(videoCost))
	if err != nil {
		return pricingRow{}, internalerrors.New(internalerrors.ErrDatabaseError, "parse credit_per_second", err)
	}
	meta := ""
	if description != nil {
		meta = *description
	}
	row, err := parsePricing(meta, v)
	if err != nil {
		return pricingRow{}, internalerrors.New(internalerrors.ErrDatabaseError, "parse pricing description", err)
	}
	return row, nil
}

func getOrCreateBudgetPostgres(ctx context.Context, tx pgx.Tx, apiKeyID string, now time.Time) (decimal.Decimal, decimal.Decimal, error) {
	var remainingStr string
	var reservedStr string
	err := tx.QueryRow(ctx,
		`SELECT credits_remaining::text, credits_reserved::text FROM key_budgets WHERE api_key_id = $1 LIMIT 1 FOR UPDATE;`,
		apiKeyID,
	).Scan(&remainingStr, &reservedStr)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return decimal.Zero, decimal.Zero, internalerrors.New(internalerrors.ErrDatabaseError, "select key budget", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO key_budgets (id, api_key_id, credits_remaining, credits_reserved, created_at, updated_at)
			 VALUES ($1, $2, 0, 0, $3, $3)
			 ON CONFLICT (api_key_id) DO NOTHING;`,
			"kbud_"+randomHex(8), apiKeyID, now,
		); err != nil {
			return decimal.Zero, decimal.Zero, internalerrors.New(internalerrors.ErrDatabaseError, "insert key budget", err)
		}
		if err := tx.QueryRow(ctx,
			`SELECT credits_remaining::text, credits_reserved::text FROM key_budgets WHERE api_key_id = $1 LIMIT 1 FOR UPDATE;`,
			apiKeyID,
		).Scan(&remainingStr, &reservedStr); err != nil {
			return decimal.Zero, decimal.Zero, internalerrors.New(internalerrors.ErrDatabaseError, "reselect key budget", err)
		}
	}

	remaining, err := decimal.NewFromString(strings.TrimSpace(remainingStr))
	if err != nil {
		return decimal.Zero, decimal.Zero, internalerrors.New(internalerrors.ErrDatabaseError, "parse credits_remaining", err)
	}
	reserved, err := decimal.NewFromString(strings.TrimSpace(reservedStr))
	if err != nil {
		return decimal.Zero, decimal.Zero, internalerrors.New(internalerrors.ErrDatabaseError, "parse credits_reserved", err)
	}
	return remaining, reserved, nil
}

func nullableInt(v int, valid bool) any {
	if !valid {
		return nil
	}
	return v
}

func buildPreAuthResultFromExisting(existing existingPreAuthLedger) PreAuthResult {
	requestType := requestTypeImage
	if existing.DurationSeconds > 0 {
		requestType = requestTypeVideo
	}
	return PreAuthResult{
		LedgerID:        existing.LedgerID,
		RequestID:       existing.RequestID,
		APIKeyID:        existing.APIKeyID,
		Preset:          existing.Preset,
		RequestType:     requestType,
		EstimatedCost:   existing.EstimatedCost,
		DurationSeconds: existing.DurationSeconds,
		Multiplier:      existing.Multiplier,
	}
}

func getExistingPreAuthSQLite(ctx context.Context, tx *sql.Tx, requestID string) (existingPreAuthLedger, bool, error) {
	return getExistingPreAuthSQLiteWithQueryRow(ctx, tx.QueryRowContext, requestID)
}

func getExistingPreAuthSQLiteFromDB(ctx context.Context, db *sql.DB, requestID string) (existingPreAuthLedger, bool, error) {
	return getExistingPreAuthSQLiteWithQueryRow(ctx, db.QueryRowContext, requestID)
}

func getExistingPreAuthSQLiteWithQueryRow(ctx context.Context, queryRow func(context.Context, string, ...any) *sql.Row, requestID string) (existingPreAuthLedger, bool, error) {
	var (
		entry         existingPreAuthLedger
		totalCost     string
		durationSecNS sql.NullInt64
	)
	err := queryRow(ctx,
		`SELECT id, api_key_id, request_id, preset, multiplier, duration_seconds, CAST(total_cost AS TEXT)
		 FROM billing_ledger
		 WHERE request_id = ?
		 LIMIT 1;`,
		requestID,
	).Scan(
		&entry.LedgerID,
		&entry.APIKeyID,
		&entry.RequestID,
		&entry.Preset,
		&entry.Multiplier,
		&durationSecNS,
		&totalCost,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return existingPreAuthLedger{}, false, nil
		}
		return existingPreAuthLedger{}, false, internalerrors.New(internalerrors.ErrDatabaseError, "select existing billing preauth", err)
	}
	if durationSecNS.Valid {
		entry.DurationSeconds = int(durationSecNS.Int64)
	}
	estimate, err := decimal.NewFromString(strings.TrimSpace(totalCost))
	if err != nil {
		return existingPreAuthLedger{}, false, internalerrors.New(internalerrors.ErrDatabaseError, "parse existing billing total_cost", err)
	}
	entry.EstimatedCost = estimate
	return entry, true, nil
}

func getExistingPreAuthPostgres(ctx context.Context, tx pgx.Tx, requestID string) (existingPreAuthLedger, bool, error) {
	return getExistingPreAuthPostgresWithQueryRow(ctx, tx.QueryRow, requestID)
}

func getExistingPreAuthPostgresFromPool(ctx context.Context, pool *pgxpool.Pool, requestID string) (existingPreAuthLedger, bool, error) {
	return getExistingPreAuthPostgresWithQueryRow(ctx, pool.QueryRow, requestID)
}

func getExistingPreAuthPostgresWithQueryRow(ctx context.Context, queryRow func(context.Context, string, ...any) pgx.Row, requestID string) (existingPreAuthLedger, bool, error) {
	var (
		entry       existingPreAuthLedger
		totalCost   string
		durationSec *int
	)
	err := queryRow(ctx,
		`SELECT id, api_key_id, request_id, preset, multiplier, duration_seconds, total_cost::text
		 FROM billing_ledger
		 WHERE request_id = $1
		 LIMIT 1;`,
		requestID,
	).Scan(
		&entry.LedgerID,
		&entry.APIKeyID,
		&entry.RequestID,
		&entry.Preset,
		&entry.Multiplier,
		&durationSec,
		&totalCost,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return existingPreAuthLedger{}, false, nil
		}
		return existingPreAuthLedger{}, false, internalerrors.New(internalerrors.ErrDatabaseError, "select existing billing preauth", err)
	}
	if durationSec != nil {
		entry.DurationSeconds = *durationSec
	}
	estimate, err := decimal.NewFromString(strings.TrimSpace(totalCost))
	if err != nil {
		return existingPreAuthLedger{}, false, internalerrors.New(internalerrors.ErrDatabaseError, "parse existing billing total_cost", err)
	}
	entry.EstimatedCost = estimate
	return entry, true, nil
}

func isSQLiteUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "request_id") {
		return false
	}
	return strings.Contains(msg, "unique") || strings.Contains(msg, "constraint")
}

func isPostgresUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	if pgErr.Code != "23505" {
		return false
	}
	msg := strings.ToLower(pgErr.ConstraintName + " " + pgErr.Message)
	return strings.Contains(msg, "request_id")
}

func randomHex(size int) string {
	if size <= 0 {
		size = 8
	}
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b)
}

type billingDBKind string

const (
	billingDBKindSQLite   billingDBKind = "sqlite"
	billingDBKindPostgres billingDBKind = "postgres"
)

type billingDB struct {
	kind   billingDBKind
	sqlite *sql.DB
	pg     *pgxpool.Pool
	close  func()
}

func openBillingDB(ctx context.Context) (*billingDB, error) {
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
		return &billingDB{kind: billingDBKindSQLite, sqlite: repos.DB, close: func() { _ = repos.Close() }}, nil
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
		return &billingDB{kind: billingDBKindPostgres, pg: pool, close: cleanup}, nil
	default:
		return nil, internalerrors.New(internalerrors.ErrInternalError, "unsupported database type: "+dbType, nil)
	}
}
