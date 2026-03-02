package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jimeng-relay/server/internal/config"
	relayhandler "github.com/jimeng-relay/server/internal/handler/relay"
	"github.com/jimeng-relay/server/internal/middleware/observability"
	"github.com/jimeng-relay/server/internal/middleware/sigv4"
	"github.com/jimeng-relay/server/internal/relay/upstream"
	"github.com/jimeng-relay/server/internal/repository/sqlite"
	"github.com/jimeng-relay/server/internal/secretcrypto"
	apikeyservice "github.com/jimeng-relay/server/internal/service/apikey"
	auditservice "github.com/jimeng-relay/server/internal/service/audit"
	idempotencyservice "github.com/jimeng-relay/server/internal/service/idempotency"
)

const (
	defaultLowConcurrency  = 8
	defaultHighConcurrency = 48
	defaultDuration        = 25 * time.Second
	defaultTimeout         = 3 * time.Second
	defaultMaxRetries      = 2
)

type scenario struct {
	Name        string
	Concurrency int
}

type result struct {
	Name           string        `json:"name"`
	Concurrency    int           `json:"concurrency"`
	Duration       time.Duration `json:"duration"`
	Total          int64         `json:"total"`
	Success        int64         `json:"success"`
	HTTPFailures   int64         `json:"http_failures"`
	TransportError int64         `json:"transport_errors"`
	ReqPerSec      float64       `json:"req_per_sec"`
	P95Ms          float64       `json:"p95_ms"`
	ErrorRate      float64       `json:"error_rate"`
}

type runReport struct {
	At        time.Time `json:"at"`
	GoVersion string    `json:"go_version"`
	GOOS      string    `json:"goos"`
	GOARCH    string    `json:"goarch"`
	CPU       int       `json:"cpu"`
	GOMAXPROC int       `json:"gomaxprocs"`
	Config    struct {
		Duration      time.Duration `json:"duration"`
		Timeout       time.Duration `json:"timeout"`
		MaxRetries    int           `json:"max_retries"`
		LowConc       int           `json:"low_concurrency"`
		HighConc      int           `json:"high_concurrency"`
		ClientConns   int           `json:"client_max_idle_per_host"`
		Database      string        `json:"database"`
		DatabaseURL   string        `json:"database_url"`
		UpstreamDelay time.Duration `json:"upstream_delay"`
	} `json:"config"`
	Scenarios []result `json:"scenarios"`
}

type benchEnv struct {
	serverURL   string
	accessKey   string
	secretKey   string
	region      string
	service     string
	httpClient  *http.Client
	cleanupFunc func()
	counter     atomic.Int64
}

func main() {
	low := flag.Int("low", defaultLowConcurrency, "low concurrency scenario")
	high := flag.Int("high", defaultHighConcurrency, "high concurrency scenario")
	duration := flag.Duration("duration", defaultDuration, "duration per scenario")
	timeout := flag.Duration("timeout", defaultTimeout, "relay->upstream timeout and client timeout")
	maxRetries := flag.Int("max-retries", defaultMaxRetries, "upstream max retries")
	clientMaxIdle := flag.Int("client-max-idle-per-host", 256, "load client MaxIdleConnsPerHost")
	upstreamDelay := flag.Duration("upstream-delay", 20*time.Millisecond, "fake upstream latency per request")
	writeJSON := flag.String("out", "", "optional json output path")
	flag.Parse()

	if *low <= 0 || *high <= 0 {
		log.Fatalf("low/high must be positive")
	}
	if *duration <= 0 {
		log.Fatalf("duration must be positive")
	}
	if *timeout <= 0 {
		log.Fatalf("timeout must be positive")
	}
	if *maxRetries < 0 {
		log.Fatalf("max-retries must be >= 0")
	}

	env, err := setupBench(*timeout, *maxRetries, *clientMaxIdle, *upstreamDelay)
	if err != nil {
		log.Fatalf("setup bench env: %v", err)
	}
	defer env.cleanupFunc()

	scenarios := []scenario{
		{Name: "low", Concurrency: *low},
		{Name: "high", Concurrency: *high},
	}

	report := runReport{
		At:        time.Now().UTC(),
		GoVersion: runtime.Version(),
		GOOS:      runtime.GOOS,
		GOARCH:    runtime.GOARCH,
		CPU:       runtime.NumCPU(),
		GOMAXPROC: runtime.GOMAXPROCS(0),
	}
	report.Config.Duration = *duration
	report.Config.Timeout = *timeout
	report.Config.MaxRetries = *maxRetries
	report.Config.LowConc = *low
	report.Config.HighConc = *high
	report.Config.ClientConns = *clientMaxIdle
	report.Config.Database = "sqlite"
	report.Config.DatabaseURL = "temp-file"
	report.Config.UpstreamDelay = *upstreamDelay

	fmt.Printf("# Relay Submit Baseline\n")
	fmt.Printf("time=%s go=%s os=%s/%s cpu=%d gomaxprocs=%d\n", report.At.Format(time.RFC3339), report.GoVersion, report.GOOS, report.GOARCH, report.CPU, report.GOMAXPROC)
	fmt.Printf("duration=%s timeout=%s upstream_max_retries=%d upstream_delay=%s client_max_idle_per_host=%d\n\n", report.Config.Duration, report.Config.Timeout, report.Config.MaxRetries, report.Config.UpstreamDelay, report.Config.ClientConns)

	fmt.Printf("| scenario | concurrency | total | success | req/s | p95(ms) | error_rate |\n")
	fmt.Printf("|---|---:|---:|---:|---:|---:|---:|\n")

	for _, s := range scenarios {
		r := runScenario(env, s, *duration)
		report.Scenarios = append(report.Scenarios, r)
		fmt.Printf("| %s | %d | %d | %d | %.2f | %.2f | %.3f%% |\n", r.Name, r.Concurrency, r.Total, r.Success, r.ReqPerSec, r.P95Ms, r.ErrorRate*100)
	}

	if strings.TrimSpace(*writeJSON) != "" {
		if err := writeReportJSON(*writeJSON, report); err != nil {
			log.Fatalf("write json report: %v", err)
		}
		fmt.Printf("\njson_report=%s\n", *writeJSON)
	}
}

func setupBench(timeout time.Duration, maxRetries, clientMaxIdle int, upstreamDelay time.Duration) (*benchEnv, error) {
	ctx := context.Background()
	tmpDir, err := os.MkdirTemp("", "relay-perf-")
	if err != nil {
		return nil, err
	}
	dbPath := filepath.Join(tmpDir, "perf.db")

	repos, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if upstreamDelay > 0 {
			time.Sleep(upstreamDelay)
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		action := r.URL.Query().Get("Action")
		if action != "CVSync2AsyncSubmitTask" {
			w.WriteHeader(http.StatusBadRequest)
			if _, err := w.Write([]byte(`{"error":"bad action"}`)); err != nil {
				return
			}
			return
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`{"code":10000,"message":"ok","task_id":"task_perf"}`)); err != nil {
			return
		}
	}))

	secretCipher, err := secretcrypto.NewAESCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		upstreamSrv.Close()
		_ = repos.Close()
		_ = os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("new cipher: %w", err)
	}

	apikeySvc := apikeyservice.NewService(repos.APIKeys, apikeyservice.Config{SecretCipher: secretCipher})
	created, err := apikeySvc.Create(ctx, apikeyservice.CreateRequest{Description: "perf-baseline"})
	if err != nil {
		upstreamSrv.Close()
		_ = repos.Close()
		_ = os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("create api key: %w", err)
	}

	auditSvc := auditservice.NewService(repos.DownstreamRequests, repos.UpstreamAttempts, repos.AuditEvents, auditservice.Config{})
	idemSvc := idempotencyservice.NewService(repos.IdempotencyRecords, idempotencyservice.Config{})

	upstreamCfg := config.Config{
		Credentials: config.Credentials{AccessKey: "volc_perf_ak", SecretKey: "volc_perf_sk"},
		Region:      "cn-north-1",
		Host:        upstreamSrv.URL,
		Timeout:     timeout,
	}
	upstreamClient, err := upstream.NewClient(upstreamCfg, upstream.Options{MaxRetries: maxRetries})
	if err != nil {
		upstreamSrv.Close()
		_ = repos.Close()
		_ = os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("new upstream client: %w", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	authn := sigv4.New(repos.APIKeys, sigv4.Config{SecretCipher: secretCipher, ExpectedRegion: "cn-north-1", ExpectedService: "cv"})
	submitRoutes := relayhandler.NewSubmitHandler(upstreamClient, auditSvc, nil, idemSvc, repos.IdempotencyRecords, logger).Routes()

	app := http.NewServeMux()
	app.Handle("/v1/submit", submitRoutes)
	app.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("Action") == "CVSync2AsyncSubmitTask" {
			submitRoutes.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})

	relaySrv := httptest.NewServer(observability.Middleware(logger)(authn(app)))

	transport := &http.Transport{
		MaxIdleConns:        clientMaxIdle * 2,
		MaxIdleConnsPerHost: clientMaxIdle,
		MaxConnsPerHost:     0,
		IdleConnTimeout:     90 * time.Second,
	}

	client := &http.Client{Timeout: timeout, Transport: transport}

	cleanup := func() {
		relaySrv.Close()
		upstreamSrv.Close()
		_ = repos.Close()
		_ = os.RemoveAll(tmpDir)
		transport.CloseIdleConnections()
	}

	return &benchEnv{
		serverURL:   relaySrv.URL,
		accessKey:   created.AccessKey,
		secretKey:   created.SecretKey,
		region:      "cn-north-1",
		service:     "cv",
		httpClient:  client,
		cleanupFunc: cleanup,
	}, nil
}

func runScenario(env *benchEnv, s scenario, d time.Duration) result {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()

	latencies := make([]time.Duration, 0, s.Concurrency*256)
	latMu := sync.Mutex{}

	var total int64
	var success int64
	var httpFailures int64
	var transportErrors int64

	startWall := time.Now()
	wg := sync.WaitGroup{}
	for i := 0; i < s.Concurrency; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for {
				if ctx.Err() != nil {
					return
				}
				idx := env.counter.Add(1)
				idempotencyKey := "perf-" + s.Name + "-" + strconv.FormatInt(int64(workerID), 10) + "-" + strconv.FormatInt(idx, 10)
				payload := []byte(fmt.Sprintf(`{"req_key":"req_%s_%d","prompt":"A cat","seed":1}`, s.Name, idx))

				req, err := buildSignedRequest(http.MethodPost, env.serverURL+"/v1/submit", payload, env.accessKey, env.secretKey, env.region, env.service, time.Now().UTC())
				if err != nil {
					atomic.AddInt64(&transportErrors, 1)
					atomic.AddInt64(&total, 1)
					continue
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "application/json")
				req.Header.Set("Idempotency-Key", idempotencyKey)

				begin := time.Now()
				resp, err := env.httpClient.Do(req)
				elapsed := time.Since(begin)
				atomic.AddInt64(&total, 1)

				latMu.Lock()
				latencies = append(latencies, elapsed)
				latMu.Unlock()

				if err != nil {
					atomic.AddInt64(&transportErrors, 1)
					continue
				}
				if _, err := io.Copy(io.Discard, resp.Body); err != nil {
					atomic.AddInt64(&transportErrors, 1)
					_ = resp.Body.Close()
					continue
				}
				_ = resp.Body.Close()

				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					atomic.AddInt64(&success, 1)
				} else {
					atomic.AddInt64(&httpFailures, 1)
				}
			}
		}(i)
	}
	wg.Wait()
	elapsedWall := time.Since(startWall)

	all := atomic.LoadInt64(&total)
	errCount := atomic.LoadInt64(&httpFailures) + atomic.LoadInt64(&transportErrors)
	rps := 0.0
	if elapsedWall > 0 {
		rps = float64(all) / elapsedWall.Seconds()
	}

	return result{
		Name:           s.Name,
		Concurrency:    s.Concurrency,
		Duration:       elapsedWall,
		Total:          all,
		Success:        atomic.LoadInt64(&success),
		HTTPFailures:   atomic.LoadInt64(&httpFailures),
		TransportError: atomic.LoadInt64(&transportErrors),
		ReqPerSec:      rps,
		P95Ms:          p95Milliseconds(latencies),
		ErrorRate:      safeRate(errCount, all),
	}
}

func safeRate(numerator, denominator int64) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func p95Milliseconds(latencies []time.Duration) float64 {
	if len(latencies) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(latencies))
	copy(sorted, latencies)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i] < sorted[j]
	})
	idx := int(math.Ceil(float64(len(sorted))*0.95)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return float64(sorted[idx].Microseconds()) / 1000.0
}

func writeReportJSON(path string, report runReport) error {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil
	}
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(trimmed), 0o755); err != nil {
		return err
	}
	return os.WriteFile(trimmed, b, 0o644)
}

func buildSignedRequest(method, target string, body []byte, accessKey, secret, region, service string, ts time.Time) (*http.Request, error) {
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(method, parsed.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Host = parsed.Host

	date := ts.UTC().Format("20060102T150405Z")
	payloadHash := sha256Hex(body)
	req.Header.Set("X-Date", date)
	req.Header.Set("X-Content-Sha256", payloadHash)

	signedHeaders := []string{"host", "x-content-sha256", "x-date"}
	canonicalRequest, err := buildCanonicalRequest(req, signedHeaders, payloadHash)
	if err != nil {
		return nil, err
	}
	dateScope := ts.UTC().Format("20060102")
	scope := dateScope + "/" + region + "/" + service + "/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		date,
		scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	signingKey := deriveSigningKey(secret, dateScope, region, service)
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKey+"/"+scope+", SignedHeaders="+strings.Join(signedHeaders, ";")+", Signature="+signature)
	return req, nil
}

func buildCanonicalRequest(r *http.Request, signedHeaders []string, payloadHash string) (string, error) {
	headers := append([]string(nil), signedHeaders...)
	sort.Strings(headers)
	canonHeaders := strings.Builder{}
	for _, h := range headers {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			return "", fmt.Errorf("empty signed header")
		}
		v := canonicalHeaderValue(r, h)
		if v == "" {
			return "", fmt.Errorf("missing signed header: %s", h)
		}
		canonHeaders.WriteString(h)
		canonHeaders.WriteByte(':')
		canonHeaders.WriteString(v)
		canonHeaders.WriteByte('\n')
	}

	canon := strings.Join([]string{
		r.Method,
		canonicalURI(r.URL.Path),
		canonicalQueryString(r.URL.Query()),
		canonHeaders.String(),
		strings.Join(headers, ";"),
		payloadHash,
	}, "\n")
	return canon, nil
}

func canonicalHeaderValue(r *http.Request, name string) string {
	if name == "host" {
		return strings.TrimSpace(strings.ToLower(r.Host))
	}
	vals := r.Header.Values(name)
	if len(vals) == 0 {
		return ""
	}
	for i := range vals {
		vals[i] = strings.Join(strings.Fields(vals[i]), " ")
	}
	return strings.TrimSpace(strings.Join(vals, ","))
}

func canonicalURI(path string) string {
	if path == "" {
		return "/"
	}
	parts := strings.Split(path, "/")
	for i := range parts {
		parts[i] = awsEscape(parts[i])
	}
	uri := strings.Join(parts, "/")
	if !strings.HasPrefix(uri, "/") {
		uri = "/" + uri
	}
	return uri
}

func canonicalQueryString(values url.Values) string {
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
		ek := awsEscape(k)
		for _, v := range vals {
			pairs = append(pairs, ek+"="+awsEscape(v))
		}
	}
	return strings.Join(pairs, "&")
}

func awsEscape(s string) string {
	e := url.QueryEscape(s)
	e = strings.ReplaceAll(e, "+", "%20")
	e = strings.ReplaceAll(e, "*", "%2A")
	e = strings.ReplaceAll(e, "%7E", "~")
	return e
}

func deriveSigningKey(secret, date, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), date)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}

func hmacSHA256(key []byte, message string) []byte {
	h := hmac.New(sha256.New, key)
	if _, err := h.Write([]byte(message)); err != nil {
		panic(err)
	}
	return h.Sum(nil)
}

func sha256Hex(v []byte) string {
	s := sha256.Sum256(v)
	return hex.EncodeToString(s[:])
}
