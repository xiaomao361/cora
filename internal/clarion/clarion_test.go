package clarion

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
)

func TestSchemaMigrationCreatesAndReopensCurrentDatabase(t *testing.T) {
	path := t.TempDir() + "/clarion.db"
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	assertSchemaVersion(t, path, 2)

	store, err = OpenStore(path)
	if err != nil {
		t.Fatalf("reopen current database: %v", err)
	}
	store.Close()
	assertSchemaVersion(t, path, 2)
}

func TestSchemaMigrationUpgradesUnversionedDatabaseWithoutLosingData(t *testing.T) {
	path := t.TempDir() + "/clarion.db"
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
	assertSchemaVersion(t, path, 2)
}

func TestSchemaMigrationRejectsNewerDatabase(t *testing.T) {
	path := t.TempDir() + "/clarion.db"
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

func TestSchemaMigrationFailureRollsBackVersionAndDDL(t *testing.T) {
	path := t.TempDir() + "/clarion.db"
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
	store, err := OpenStore(t.TempDir() + "/clarion.db")
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

func TestAggregatorFlushesOneProblemAndTrendPointPerFingerprint(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/clarion.db")
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
	points, err := store.TrendPoints(context.Background(), "payments", problems[0].Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].Count != 1000 {
		t.Fatalf("trend points=%v, want one point with count 1000", points)
	}
}

func TestAggregatorIsBoundedAndCountsDroppedEvents(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/clarion.db")
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
	store, err := OpenStore(t.TempDir() + "/clarion.db")
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
	store, err := OpenStore(t.TempDir() + "/clarion.db")
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
