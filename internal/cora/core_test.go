package cora

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/claracore/cora/internal/buildinfo"
)

func TestSchemaMigrationCreatesAndReopensCurrentDatabase(t *testing.T) {
	path := t.TempDir() + "/cora.db"
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	assertSchemaVersion(t, path, 5)

	store, err = OpenStore(path)
	if err != nil {
		t.Fatalf("reopen current database: %v", err)
	}
	store.Close()
	assertSchemaVersion(t, path, 5)
}

func TestSchemaMigrationUpgradesUnversionedDatabaseWithoutLosingData(t *testing.T) {
	path := t.TempDir() + "/cora.db"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE problems (
		id INTEGER PRIMARY KEY, product_line TEXT NOT NULL, fingerprint TEXT NOT NULL,
		service TEXT NOT NULL, environment TEXT NOT NULL, exception_type TEXT NOT NULL,
		logger TEXT NOT NULL, count INTEGER NOT NULL, first_seen TEXT NOT NULL,
		last_seen TEXT NOT NULL, first_sample TEXT NOT NULL, latest_sample TEXT NOT NULL,
		UNIQUE(product_line, fingerprint));
		INSERT INTO problems VALUES (1, 'legacy', 'fingerprint', 'api', 'prod', 'Timeout',
		'logger', 7, '2026-07-13T00:00:00Z', '2026-07-13T00:00:00Z', '{}', '{}')`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	problems, err := store.Problems(context.Background(), "legacy")
	if err != nil || len(problems) != 1 || problems[0].Count != 7 {
		t.Fatalf("legacy data not preserved: problems=%v err=%v", problems, err)
	}
	assertSchemaVersion(t, path, 5)
}

func TestSchemaMigrationUpgradesV3IdentityWithoutLosingFacts(t *testing.T) {
	path := t.TempDir() + "/cora.db"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(db, schemaMigrations[:3]); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO problems
		(product_line, fingerprint, service, environment, exception_type, logger, count,
		 first_seen, last_seen, first_sample, latest_sample)
		VALUES ('line', 'fp', 'orders', 'prod', 'Timeout', 'logger', 7,
		 '2026-07-13T00:00:00Z', '2026-07-13T00:01:00Z', '{}', '{}');
		INSERT INTO trend_points
		(product_line, fingerprint, count, window_start, window_end)
		VALUES ('line', 'fp', 7, '2026-07-13T00:00:00Z', '2026-07-13T00:01:00Z');
		INSERT INTO cora_decisions
		(product_line, fingerprint, decision, category, rule_id, reason, source,
		 experience_version, decided_at)
		VALUES ('line', 'fp', 'observe', 'test', 'rule', 'reason', 'test', '',
		 '2026-07-13T00:01:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	problems, err := store.Problems(context.Background(), "line")
	if err != nil || len(problems) != 1 || problems[0].Service != "orders" || problems[0].Count != 7 {
		t.Fatalf("migrated problems=%v err=%v", problems, err)
	}
	points, err := store.TrendPoints(context.Background(), "line", "orders", "fp")
	if err != nil || len(points) != 1 || points[0].Count != 7 {
		t.Fatalf("migrated trends=%v err=%v", points, err)
	}
	items, err := store.Attention(context.Background(), "line")
	if err != nil || len(items) != 1 || items[0].Service != "orders" {
		t.Fatalf("migrated decisions=%v err=%v", items, err)
	}
	assertSchemaVersion(t, path, 5)
}

func TestSchemaMigrationRejectsNewerDatabase(t *testing.T) {
	path := t.TempDir() + "/cora.db"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	store, err := OpenStore(path)
	if store != nil {
		store.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("got error %v, want newer schema rejection", err)
	}
}

func TestStoreReadinessTracksFailedAndRecoveredSQLiteWrites(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/cora.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ready, reasons := store.Ready(context.Background())
	if !ready || len(reasons) != 0 {
		t.Fatalf("initial readiness ready=%v reasons=%v", ready, reasons)
	}
	event := Event{ProductLine: "line", Service: "api", Environment: "prod", ExceptionType: "Timeout"}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Record(cancelled, event); err == nil {
		t.Fatal("cancelled write unexpectedly succeeded")
	}
	health := store.Health()
	if health.WriteFailures != 1 || health.LastWriteError == "" {
		t.Fatalf("failed write health=%+v", health)
	}
	ready, reasons = store.Ready(context.Background())
	if ready || len(reasons) == 0 {
		t.Fatalf("readiness did not expose latest failure: ready=%v reasons=%v", ready, reasons)
	}
	if err := store.Record(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	health = store.Health()
	if health.SuccessfulWrites != 1 || health.LastSuccessfulWriteAt == nil || health.LastSuccessfulWriteAt.IsZero() {
		t.Fatalf("recovered write health=%+v", health)
	}
	ready, reasons = store.Ready(context.Background())
	if !ready || len(reasons) != 0 {
		t.Fatalf("recovered readiness ready=%v reasons=%v", ready, reasons)
	}
}

func TestVerifiedSQLiteBackupCanBeRestored(t *testing.T) {
	directory := t.TempDir()
	store, err := OpenStore(directory + "/cora.db")
	if err != nil {
		t.Fatal(err)
	}
	event := Event{ProductLine: "line", Service: "api", Environment: "prod", ExceptionType: "Timeout"}
	if err := store.Record(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	backupPath := directory + "/backups/cora.db"
	if err := store.Backup(context.Background(), backupPath); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{directory + "/cora.db", backupPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("database mode for %s is %o, want 600", path, info.Mode().Perm())
		}
	}
	store.Close()
	restored, err := OpenStore(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err := restored.IntegrityCheck(context.Background()); err != nil {
		t.Fatal(err)
	}
	problems, err := restored.Problems(context.Background(), "line")
	if err != nil || len(problems) != 1 {
		t.Fatalf("restored problems=%v err=%v", problems, err)
	}
}

func TestSchemaMigrationFailureRollsBackVersionAndDDL(t *testing.T) {
	path := t.TempDir() + "/cora.db"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = migrate(db, []migration{{version: 1, statements: []string{
		`CREATE TABLE should_rollback (id INTEGER PRIMARY KEY)`,
		`THIS IS NOT SQL`,
	}}})
	if err == nil {
		t.Fatal("migration unexpectedly succeeded")
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 0 {
		t.Fatalf("schema version=%d, want 0", version)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='should_rollback'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed migration left DDL behind")
	}
}

func assertSchemaVersion(t *testing.T, path string, want int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var got int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("schema version=%d, want %d", got, want)
	}
}

func TestRepeatedErrorsCollapseIntoOneProblem(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/cora.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	first := Event{
		ProductLine: "payments", Service: "checkout", Environment: "prod",
		Logger: "com.example.Checkout", ExceptionType: "java.lang.OutOfMemoryError",
		Message: "request 123 failed", Stacktrace: "at java.util.List.get(List.java:1)\nat com.example.Checkout.run(Checkout.java:41)",
	}
	second := first
	second.Message = "request 999 failed"
	second.Stacktrace = "at java.util.List.get(List.java:1)\nat com.example.Checkout.run(Checkout.java:99)"

	for _, event := range []Event{first, second} {
		if err := store.Record(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	problems, err := store.Problems(context.Background(), "payments")
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 {
		t.Fatalf("got %d problems, want 1", len(problems))
	}
	if problems[0].Count != 2 {
		t.Fatalf("got count %d, want 2", problems[0].Count)
	}
	if problems[0].FirstSample == problems[0].LatestSample {
		t.Fatal("first and latest representative samples should differ")
	}
}

func TestBreadcrumbContextDoesNotAffectFingerprint(t *testing.T) {
	event := Event{Logger: "com.example.Order", ExceptionType: "Timeout", Message: "failed"}
	withContext := event
	withContext.Thread = "worker-2"
	withContext.Method = "submit"
	withContext.Line = "42"
	withContext.Breadcrumbs = []Breadcrumb{{Level: "INFO", Message: "request started"}}
	if Fingerprint(event) != Fingerprint(withContext) {
		t.Fatal("thread, method, line, and breadcrumbs must not affect fingerprint")
	}
}

func TestOutOfOrderErrorsKeepChronologicalBoundsAndSamples(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/cora.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	aggregator := NewAggregator(store, 10)

	base := Event{
		ProductLine: "payments", Service: "checkout", Environment: "prod",
		Logger: "com.example.Checkout", ExceptionType: "java.lang.IllegalStateException",
		Stacktrace: "java.lang.IllegalStateException\n\tat com.example.Checkout.run(Checkout.java:41)",
	}
	newest := base
	newest.Message = "newest"
	newest.OccurredAt = time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC)
	middle := base
	middle.Message = "middle"
	middle.OccurredAt = time.Date(2026, 7, 13, 7, 30, 0, 0, time.UTC)
	oldest := base
	oldest.Message = "oldest"
	oldest.OccurredAt = time.Date(2026, 7, 13, 15, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))

	// Loki backfills can arrive newest-first. The first flush verifies ordering
	// inside one aggregation window; the second verifies the SQLite upsert.
	for _, event := range []Event{newest, middle} {
		if err := aggregator.Add(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := aggregator.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := aggregator.Add(oldest); err != nil {
		t.Fatal(err)
	}
	if err := aggregator.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	problems, err := store.Problems(context.Background(), "payments")
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 {
		t.Fatalf("got %d problems, want 1", len(problems))
	}
	problem := problems[0]
	if problem.Count != 3 {
		t.Fatalf("count=%d, want 3", problem.Count)
	}
	if !problem.FirstSeen.Equal(oldest.OccurredAt) {
		t.Fatalf("first_seen=%s, want %s", problem.FirstSeen, oldest.OccurredAt)
	}
	if !problem.LastSeen.Equal(newest.OccurredAt) {
		t.Fatalf("last_seen=%s, want %s", problem.LastSeen, newest.OccurredAt)
	}
	var firstSample, latestSample Event
	if err := json.Unmarshal([]byte(problem.FirstSample), &firstSample); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(problem.LatestSample), &latestSample); err != nil {
		t.Fatal(err)
	}
	if firstSample.Message != "oldest" || latestSample.Message != "newest" {
		t.Fatalf("samples=%q..%q, want oldest..newest", firstSample.Message, latestSample.Message)
	}
}

func TestAggregatorFlushesOneProblemAndTrendPointPerFingerprint(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/cora.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	aggregator := NewAggregator(store, 10)
	event := Event{ProductLine: "payments", Service: "checkout", Environment: "prod", Logger: "checkout", ExceptionType: "OOM", Message: "first"}
	for i := 0; i < 1000; i++ {
		event.Message = "request failed"
		if err := aggregator.Add(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := aggregator.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	stats := aggregator.Stats()
	if stats.PendingFingerprints != 0 || stats.Flushes != 1 || stats.FlushedEvents != 1000 || stats.FlushFailures != 0 {
		t.Fatalf("unexpected aggregator stats: %+v", stats)
	}
	problems, err := store.Problems(context.Background(), "payments")
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || problems[0].Count != 1000 {
		t.Fatalf("problems=%v, want one problem with count 1000", problems)
	}
	points, err := store.TrendPoints(context.Background(), "payments", "checkout", problems[0].Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].Count != 1000 {
		t.Fatalf("trend points=%v, want one point with count 1000", points)
	}
}

func TestAggregatorIsBoundedAndCountsDroppedEvents(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/cora.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	aggregator := NewAggregator(store, 1)
	first := Event{ProductLine: "line", Service: "api", ExceptionType: "First"}
	second := Event{ProductLine: "line", Service: "api", ExceptionType: "Second"}
	if err := aggregator.Add(first); err != nil {
		t.Fatal(err)
	}
	if err := aggregator.Add(first); err != nil {
		t.Fatal(err)
	}
	if err := aggregator.Add(second); err != nil {
		t.Fatal(err)
	}
	if aggregator.Dropped() != 1 {
		t.Fatalf("dropped=%d, want 1", aggregator.Dropped())
	}
	if err := aggregator.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	problems, err := store.Problems(context.Background(), "line")
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || problems[0].Count != 2 {
		t.Fatalf("problems=%v", problems)
	}
}

func TestConcurrentAggregation(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/cora.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	aggregator := NewAggregator(store, 10)
	event := Event{ProductLine: "line", Service: "api", ExceptionType: "Timeout"}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if err := aggregator.Add(event); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	if err := aggregator.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	problems, err := store.Problems(context.Background(), "line")
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || problems[0].Count != 2000 {
		t.Fatalf("problems=%v", problems)
	}
}

func TestProductLineExperienceBoundary(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/cora.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	event := Event{ProductLine: "line-a", Service: "api", ExceptionType: "Timeout", Message: "timeout 10"}
	if err := store.Record(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	event.ProductLine = "line-b"
	if err := store.Record(context.Background(), event); err != nil {
		t.Fatal(err)
	}

	for _, line := range []string{"line-a", "line-b"} {
		problems, err := store.Problems(context.Background(), line)
		if err != nil || len(problems) != 1 || problems[0].Count != 1 {
			t.Fatalf("boundary failed for %s: problems=%v err=%v", line, problems, err)
		}
	}
}

func TestProblemIdentitySeparatesServicesAndTracksDualNodes(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/cora.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	aggregator := NewAggregator(store, 10)
	base := Event{
		ProductLine: "gbjk-zhifu", Service: "gb-order", Environment: "prod",
		Logger: "com.guanbai.Order", ExceptionType: "TimeoutException", Message: "same failure",
		OccurredAt: time.Date(2026, 7, 13, 1, 0, 0, 0, time.UTC),
	}
	node1 := base
	node1.Labels = map[string]string{"server": "service01", "group": "service"}
	node2 := base
	node2.Labels = map[string]string{"node": "service02", "deployment_group": "service"}
	node2.OccurredAt = node2.OccurredAt.Add(time.Minute)
	otherService := base
	otherService.Service = "gb-payment"
	otherService.Labels = map[string]string{"node": "service01", "deployment_group": "service"}

	for _, event := range []Event{node1, node1, node2, otherService} {
		if err := aggregator.Add(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := aggregator.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	problems, err := store.Problems(context.Background(), "gbjk-zhifu")
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 2 {
		t.Fatalf("problems=%v, want same fingerprint split into two services", problems)
	}
	var order Problem
	for _, problem := range problems {
		if problem.Service == "gb-order" {
			order = problem
		}
	}
	if order.Count != 3 {
		t.Fatalf("gb-order problem=%+v, want count 3", order)
	}
	attention, err := store.Attention(context.Background(), "gbjk-zhifu")
	if err != nil || len(attention) != 2 {
		t.Fatalf("service-scoped decisions=%+v err=%v", attention, err)
	}
	nodes, err := store.NodeOccurrences(context.Background(), "gbjk-zhifu", "gb-order", order.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 || nodes[0].Node != "service01" || nodes[0].Count != 2 ||
		nodes[1].Node != "service02" || nodes[1].Count != 1 {
		t.Fatalf("node occurrences=%+v", nodes)
	}
	points, err := store.NodeTrendPoints(context.Background(), "gbjk-zhifu", "gb-order", order.Fingerprint, "")
	if err != nil || len(points) != 2 {
		t.Fatalf("node trends=%+v err=%v", points, err)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/node-occurrences?product_line=gbjk-zhifu&service=gb-order&fingerprint="+order.Fingerprint, nil)
	response := httptest.NewRecorder()
	Handler(store).ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"node":"service02"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestTrendEndpointRequiresServiceIdentity(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/cora.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	request := httptest.NewRequest(http.MethodGet, "/v1/trends?product_line=line&fingerprint=fp", nil)
	response := httptest.NewRecorder()
	Handler(store).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestBearerAuthenticationProtectsV1ButLeavesHealthAvailable(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/cora.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := HandlerWithOptions(store, HandlerOptions{BearerToken: "test-token"})

	for _, test := range []struct {
		name   string
		path   string
		token  string
		status int
	}{
		{name: "health is public", path: "/healthz", status: http.StatusOK},
		{name: "missing token", path: "/v1/problems?product_line=line", status: http.StatusUnauthorized},
		{name: "wrong token", path: "/v1/problems?product_line=line", token: "wrong", status: http.StatusUnauthorized},
		{name: "valid token", path: "/v1/problems?product_line=line", token: "test-token", status: http.StatusOK},
		{name: "readiness is protected", path: "/readyz", status: http.StatusUnauthorized},
		{name: "valid readiness", path: "/readyz", token: "test-token", status: http.StatusOK},
		{name: "mcp is protected", path: "/mcp", status: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			if test.token != "" {
				request.Header.Set("Authorization", "Bearer "+test.token)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestServerHealthExposesBuildSchemaAndWriteState(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/cora.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Record(context.Background(), Event{
		ProductLine: "line", Service: "api", Environment: "prod", ExceptionType: "Timeout",
	}); err != nil {
		t.Fatal(err)
	}
	handler := HandlerWithOptions(store, HandlerOptions{BuildInfo: buildinfo.Info{
		Version: "v-test", Commit: "abc123", BuildTime: "2026-07-14T00:00:00Z", GoVersion: "go-test",
	}})
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var body struct {
		Build   buildinfo.Info `json:"build"`
		Storage StoreHealth    `json:"storage"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || body.Build.Version != "v-test" ||
		body.Storage.SchemaVersion != 5 || body.Storage.SuccessfulWrites != 1 ||
		body.Storage.LastSuccessfulWriteAt == nil {
		t.Fatalf("status=%d health=%+v", response.Code, body)
	}
}
