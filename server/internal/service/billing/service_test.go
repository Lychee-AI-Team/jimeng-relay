package billing

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jimeng-relay/server/internal/config"
	"github.com/jimeng-relay/server/internal/repository/sqlite"
	"github.com/shopspring/decimal"
)

func TestServicePreAuthorizeConcurrentSameRequestIDSingleCharge(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	fx := newBillingFixture(t, now, "1", "10")

	const workers = 12
	requestID := "req-concurrent-1"

	var wg sync.WaitGroup
	start := make(chan struct{})
	errCh := make(chan error, workers)
	resCh := make(chan PreAuthResult, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			res, err := preAuthorizeWithRetry(fx.ctx, fx.svc, requestID, fx.apiKeyID, fx.body)
			if err != nil {
				errCh <- err
				return
			}
			resCh <- res
		}()
	}

	close(start)
	wg.Wait()
	close(errCh)
	close(resCh)

	for err := range errCh {
		t.Fatalf("PreAuthorize concurrent call failed: %v", err)
	}

	results := make([]PreAuthResult, 0, workers)
	for res := range resCh {
		results = append(results, res)
	}
	if len(results) != workers {
		t.Fatalf("expected %d successful results, got %d", workers, len(results))
	}

	first := results[0]
	if first.LedgerID == "" {
		t.Fatalf("expected non-empty ledger id")
	}
	if !first.EstimatedCost.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("expected estimated cost 1, got %s", first.EstimatedCost)
	}
	for _, got := range results[1:] {
		if got.LedgerID != first.LedgerID {
			t.Fatalf("expected all calls to return same ledger id, got %q and %q", first.LedgerID, got.LedgerID)
		}
		if !got.EstimatedCost.Equal(first.EstimatedCost) {
			t.Fatalf("expected same estimated cost, got %s and %s", first.EstimatedCost, got.EstimatedCost)
		}
	}

	remaining, reserved := queryBudget(t, fx.db, fx.apiKeyID)
	if !remaining.Equal(decimal.RequireFromString("10")) {
		t.Fatalf("expected remaining 10, got %s", remaining)
	}
	if !reserved.Equal(decimal.RequireFromString("1")) {
		t.Fatalf("expected reserved 1 after concurrent retries, got %s", reserved)
	}

	if got := countLedgerByRequest(t, fx.db, requestID); got != 1 {
		t.Fatalf("expected exactly one ledger row, got %d", got)
	}
}

func preAuthorizeWithRetry(ctx context.Context, svc *Service, requestID, apiKeyID string, body []byte) (PreAuthResult, error) {
	const maxRetries = 8
	for i := 0; i <= maxRetries; i++ {
		res, err := svc.PreAuthorize(ctx, requestID, apiKeyID, body)
		if err == nil {
			return res, nil
		}
		if i == maxRetries || !strings.Contains(strings.ToLower(err.Error()), "database is locked") {
			return PreAuthResult{}, err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return PreAuthResult{}, nil
}

func TestServicePreAuthorizeInsufficientBudgetRejectsWithoutMutation(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	fx := newBillingFixture(t, now, "1", "0.5")

	_, err := fx.svc.PreAuthorize(fx.ctx, "req-insufficient-1", fx.apiKeyID, fx.body)
	if err == nil {
		t.Fatalf("expected insufficient budget error")
	}
	if !IsInsufficientBudget(err) {
		t.Fatalf("expected IsInsufficientBudget=true, got err=%v", err)
	}

	if got := countLedgerByRequest(t, fx.db, "req-insufficient-1"); got != 0 {
		t.Fatalf("expected no ledger row for rejected request, got %d", got)
	}

	remaining, reserved := queryBudget(t, fx.db, fx.apiKeyID)
	if !remaining.Equal(decimal.RequireFromString("0.5")) {
		t.Fatalf("expected remaining unchanged at 0.5, got %s", remaining)
	}
	if !reserved.Equal(decimal.Zero) {
		t.Fatalf("expected reserved unchanged at 0, got %s", reserved)
	}
}

func TestServiceTransitionPreAuthTableDriven(t *testing.T) {
	tests := []struct {
		name              string
		transition        func(*Service, context.Context, string, string) error
		expectedStatus    string
		expectedRemaining decimal.Decimal
		expectedReserved  decimal.Decimal
		settledAtValid    bool
	}{
		{
			name:              "preauth then settle deducts and double-settle prevented",
			transition:        (*Service).Settle,
			expectedStatus:    ledgerStatusSettled,
			expectedRemaining: decimal.RequireFromString("3.5"),
			expectedReserved:  decimal.Zero,
			settledAtValid:    true,
		},
		{
			name:              "preauth then release refunds reserve and double-release prevented",
			transition:        (*Service).Release,
			expectedStatus:    ledgerStatusReleased,
			expectedRemaining: decimal.RequireFromString("5"),
			expectedReserved:  decimal.Zero,
			settledAtValid:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
			fx := newBillingFixture(t, now, "1.5", "5")
			requestID := "req-transition-" + tc.expectedStatus

			pre, err := fx.svc.PreAuthorize(fx.ctx, requestID, fx.apiKeyID, fx.body)
			if err != nil {
				t.Fatalf("PreAuthorize: %v", err)
			}
			if !pre.EstimatedCost.Equal(decimal.RequireFromString("1.5")) {
				t.Fatalf("expected preauth estimate 1.5, got %s", pre.EstimatedCost)
			}

			remainingAfterPreAuth, reservedAfterPreAuth := queryBudget(t, fx.db, fx.apiKeyID)
			if !remainingAfterPreAuth.Equal(decimal.RequireFromString("5")) {
				t.Fatalf("expected remaining 5 after preauth, got %s", remainingAfterPreAuth)
			}
			if !reservedAfterPreAuth.Equal(decimal.RequireFromString("1.5")) {
				t.Fatalf("expected reserved 1.5 after preauth, got %s", reservedAfterPreAuth)
			}

			if err := tc.transition(fx.svc, fx.ctx, requestID, fx.apiKeyID); err != nil {
				t.Fatalf("first transition: %v", err)
			}

			remainingAfterFirst, reservedAfterFirst := queryBudget(t, fx.db, fx.apiKeyID)
			if !remainingAfterFirst.Equal(tc.expectedRemaining) {
				t.Fatalf("expected remaining %s after first transition, got %s", tc.expectedRemaining, remainingAfterFirst)
			}
			if !reservedAfterFirst.Equal(tc.expectedReserved) {
				t.Fatalf("expected reserved %s after first transition, got %s", tc.expectedReserved, reservedAfterFirst)
			}

			ledgerAfterFirst := getLedgerByRequest(t, fx.db, requestID)
			if ledgerAfterFirst.Status != tc.expectedStatus {
				t.Fatalf("expected status %q, got %q", tc.expectedStatus, ledgerAfterFirst.Status)
			}
			if ledgerAfterFirst.SettledAt.Valid != tc.settledAtValid {
				t.Fatalf("expected settled_at valid=%v, got %v", tc.settledAtValid, ledgerAfterFirst.SettledAt.Valid)
			}

			if err := tc.transition(fx.svc, fx.ctx, requestID, fx.apiKeyID); err != nil {
				t.Fatalf("second transition should be idempotent, got err=%v", err)
			}

			remainingAfterSecond, reservedAfterSecond := queryBudget(t, fx.db, fx.apiKeyID)
			if !remainingAfterSecond.Equal(remainingAfterFirst) {
				t.Fatalf("expected remaining unchanged on second transition, got %s then %s", remainingAfterFirst, remainingAfterSecond)
			}
			if !reservedAfterSecond.Equal(reservedAfterFirst) {
				t.Fatalf("expected reserved unchanged on second transition, got %s then %s", reservedAfterFirst, reservedAfterSecond)
			}

			ledgerAfterSecond := getLedgerByRequest(t, fx.db, requestID)
			if ledgerAfterSecond.Status != ledgerAfterFirst.Status {
				t.Fatalf("expected status unchanged on second transition, got %q then %q", ledgerAfterFirst.Status, ledgerAfterSecond.Status)
			}
			if ledgerAfterSecond.SettledAt.Valid != ledgerAfterFirst.SettledAt.Valid {
				t.Fatalf("expected settled_at validity unchanged on second transition")
			}
		})
	}
}

type billingFixture struct {
	ctx      context.Context
	db       *sql.DB
	svc      *Service
	apiKeyID string
	body     []byte
}

func newBillingFixture(t *testing.T, now time.Time, imageCost, creditsRemaining string) billingFixture {
	t.Helper()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "billing-service-test.db")

	t.Setenv(config.EnvDatabaseType, "sqlite")
	t.Setenv(config.EnvDatabaseURL, dbPath)

	repos, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() {
		_ = repos.Close()
	})

	apiKeyID := "key_test"
	preset := "jimeng_t2i_v40"
	nowStr := now.Format(time.RFC3339Nano)

	if _, err := repos.DB.ExecContext(ctx,
		`INSERT INTO api_keys (id, access_key, secret_key_hash, secret_key_ciphertext, description, multiplier, created_at, updated_at, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);`,
		apiKeyID,
		"AK_TEST",
		"hash",
		"cipher",
		"billing test",
		10000,
		nowStr,
		nowStr,
		"active",
	); err != nil {
		t.Fatalf("seed api_keys: %v", err)
	}

	if _, err := repos.DB.ExecContext(ctx,
		`INSERT INTO pricing_presets (preset, credit_per_second, description, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?);`,
		preset,
		"1",
		fmt.Sprintf(`{"image_cost":"%s"}`, imageCost),
		nowStr,
		nowStr,
	); err != nil {
		t.Fatalf("seed pricing_presets: %v", err)
	}

	if _, err := repos.DB.ExecContext(ctx,
		`INSERT INTO key_budgets (id, api_key_id, credits_remaining, credits_reserved, created_at, updated_at)
		 VALUES (?, ?, ?, 0, ?, ?);`,
		"kbud_test",
		apiKeyID,
		creditsRemaining,
		nowStr,
		nowStr,
	); err != nil {
		t.Fatalf("seed key_budgets: %v", err)
	}

	return billingFixture{
		ctx:      ctx,
		db:       repos.DB,
		svc:      NewService(Config{Now: func() time.Time { return now }}),
		apiKeyID: apiKeyID,
		body:     []byte(fmt.Sprintf(`{"prompt":"cat","req_key":"%s"}`, preset)),
	}
}

func queryBudget(t *testing.T, db *sql.DB, apiKeyID string) (decimal.Decimal, decimal.Decimal) {
	t.Helper()

	var remainingStr string
	var reservedStr string
	if err := db.QueryRow(
		`SELECT CAST(credits_remaining AS TEXT), CAST(credits_reserved AS TEXT) FROM key_budgets WHERE api_key_id = ? LIMIT 1;`,
		apiKeyID,
	).Scan(&remainingStr, &reservedStr); err != nil {
		t.Fatalf("query budget: %v", err)
	}

	remaining := decimal.RequireFromString(remainingStr)
	reserved := decimal.RequireFromString(reservedStr)
	return remaining, reserved
}

func countLedgerByRequest(t *testing.T, db *sql.DB, requestID string) int {
	t.Helper()

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM billing_ledger WHERE request_id = ?;`, requestID).Scan(&count); err != nil {
		t.Fatalf("count billing_ledger: %v", err)
	}
	return count
}

type ledgerRow struct {
	Status    string
	TotalCost decimal.Decimal
	SettledAt sql.NullString
}

func getLedgerByRequest(t *testing.T, db *sql.DB, requestID string) ledgerRow {
	t.Helper()

	var totalCostStr string
	out := ledgerRow{}
	if err := db.QueryRow(
		`SELECT status, CAST(total_cost AS TEXT), settled_at FROM billing_ledger WHERE request_id = ? LIMIT 1;`,
		requestID,
	).Scan(&out.Status, &totalCostStr, &out.SettledAt); err != nil {
		t.Fatalf("get billing_ledger row: %v", err)
	}
	out.TotalCost = decimal.RequireFromString(totalCostStr)
	return out
}
