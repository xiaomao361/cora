package clarion

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Event struct {
	ProductLine   string    `json:"product_line"`
	Service       string    `json:"service"`
	Environment   string    `json:"environment"`
	Release       string    `json:"release,omitempty"`
	Logger        string    `json:"logger"`
	ExceptionType string    `json:"exception_type"`
	Message       string    `json:"message"`
	Stacktrace    string    `json:"stacktrace"`
	OccurredAt    time.Time `json:"occurred_at,omitempty"`
}

type Problem struct {
	ID            int64     `json:"id"`
	ProductLine   string    `json:"product_line"`
	Fingerprint   string    `json:"fingerprint"`
	Service       string    `json:"service"`
	Environment   string    `json:"environment"`
	ExceptionType string    `json:"exception_type"`
	Logger        string    `json:"logger"`
	Count         int64     `json:"count"`
	FirstSeen     time.Time `json:"first_seen"`
	LastSeen      time.Time `json:"last_seen"`
	FirstSample   string    `json:"first_sample"`
	LatestSample  string    `json:"latest_sample"`
}

type TrendPoint struct {
	ProductLine string    `json:"product_line"`
	Fingerprint string    `json:"fingerprint"`
	Count       int64     `json:"count"`
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`
}

type aggregate struct {
	Fingerprint string
	Count       int64
	First       Event
	Latest      Event
}

var (
	numberPattern = regexp.MustCompile(`\b\d+\b`)
	uuidPattern   = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\b`)
	stackLine     = regexp.MustCompile(`^\s*at\s+([^\s(]+)`)
)

func Fingerprint(event Event) string {
	parts := []string{event.ExceptionType, event.Logger}
	frames := applicationFrames(event.Stacktrace, 5)
	if len(frames) == 0 {
		message := numberPattern.ReplaceAllString(uuidPattern.ReplaceAllString(event.Message, "<uuid>"), "<n>")
		parts = append(parts, message)
	} else {
		parts = append(parts, frames...)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:16])
}

func applicationFrames(stacktrace string, limit int) []string {
	frames := make([]string, 0, limit)
	for _, line := range strings.Split(stacktrace, "\n") {
		match := stackLine.FindStringSubmatch(line)
		if len(match) != 2 || isFrameworkFrame(match[1]) {
			continue
		}
		frames = append(frames, match[1])
		if len(frames) == limit {
			break
		}
	}
	return frames
}

func isFrameworkFrame(frame string) bool {
	for _, prefix := range []string{"java.", "javax.", "jakarta.", "sun.", "jdk.", "org.springframework.", "org.apache.", "io.netty."} {
		if strings.HasPrefix(frame, prefix) {
			return true
		}
	}
	return false
}

type Store struct{ db *sql.DB }

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	for _, statement := range []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA busy_timeout = 5000`,
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			return nil, fmt.Errorf("configure store: %w", err)
		}
	}
	if err := migrate(db, schemaMigrations); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

type migration struct {
	version    int
	statements []string
}

var schemaMigrations = []migration{
	{version: 1, statements: []string{`CREATE TABLE IF NOT EXISTS problems (
		id INTEGER PRIMARY KEY,
		product_line TEXT NOT NULL,
		fingerprint TEXT NOT NULL,
		service TEXT NOT NULL,
		environment TEXT NOT NULL,
		exception_type TEXT NOT NULL,
		logger TEXT NOT NULL,
		count INTEGER NOT NULL,
		first_seen TEXT NOT NULL,
		last_seen TEXT NOT NULL,
		first_sample TEXT NOT NULL,
		latest_sample TEXT NOT NULL,
		UNIQUE(product_line, fingerprint)
	)`}},
	{version: 2, statements: []string{
		`CREATE TABLE IF NOT EXISTS trend_points (
			id INTEGER PRIMARY KEY,
			product_line TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			count INTEGER NOT NULL,
			window_start TEXT NOT NULL,
			window_end TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS trend_points_line_fingerprint_time
		 ON trend_points(product_line, fingerprint, window_end)`,
	}},
}

func migrate(db *sql.DB, migrations []migration) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	latest := 0
	if len(migrations) > 0 {
		latest = migrations[len(migrations)-1].version
	}
	if version > latest {
		return fmt.Errorf("database schema version %d is newer than supported version %d", version, latest)
	}
	for _, change := range migrations {
		if change.version <= version {
			continue
		}
		if change.version != version+1 {
			return fmt.Errorf("schema migration gap: have version %d, next migration is %d", version, change.version)
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin schema migration %d: %w", change.version, err)
		}
		for _, statement := range change.statements {
			if _, err := tx.Exec(statement); err != nil {
				tx.Rollback()
				return fmt.Errorf("apply schema migration %d: %w", change.version, err)
			}
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, change.version)); err != nil {
			tx.Rollback()
			return fmt.Errorf("record schema migration %d: %w", change.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit schema migration %d: %w", change.version, err)
		}
		version = change.version
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Record(ctx context.Context, event Event) error {
	event, err := prepareEvent(event, time.Now().UTC())
	if err != nil {
		return err
	}
	fingerprint := Fingerprint(event)
	return s.Flush(ctx, time.Now().UTC(), map[string]aggregate{
		event.ProductLine + "\x00" + fingerprint: {Fingerprint: fingerprint, Count: 1, First: event, Latest: event},
	})
}

func prepareEvent(event Event, now time.Time) (Event, error) {
	if event.ProductLine == "" || event.Service == "" || event.ExceptionType == "" {
		return event, errors.New("product_line, service, and exception_type are required")
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = now
	}
	return event, nil
}

func (s *Store) Flush(ctx context.Context, windowEnd time.Time, aggregates map[string]aggregate) error {
	if len(aggregates) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range aggregates {
		firstSample, err := json.Marshal(item.First)
		if err != nil {
			return err
		}
		latestSample, err := json.Marshal(item.Latest)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
		INSERT INTO problems (
			product_line, fingerprint, service, environment, exception_type, logger,
			count, first_seen, last_seen, first_sample, latest_sample
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(product_line, fingerprint) DO UPDATE SET
			count = count + excluded.count,
			last_seen = excluded.last_seen,
			latest_sample = excluded.latest_sample`,
			item.First.ProductLine, item.Fingerprint, item.First.Service, item.First.Environment,
			item.First.ExceptionType, item.First.Logger, item.Count,
			item.First.OccurredAt.Format(time.RFC3339Nano), item.Latest.OccurredAt.Format(time.RFC3339Nano),
			string(firstSample), string(latestSample))
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO trend_points
			(product_line, fingerprint, count, window_start, window_end) VALUES (?, ?, ?, ?, ?)`,
			item.First.ProductLine, item.Fingerprint, item.Count,
			item.First.OccurredAt.Format(time.RFC3339Nano), windowEnd.Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Problems(ctx context.Context, productLine string) ([]Problem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, product_line, fingerprint, service, environment, exception_type,
		       logger, count, first_seen, last_seen, first_sample, latest_sample
		FROM problems WHERE product_line = ? ORDER BY last_seen DESC`, productLine)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	problems := []Problem{}
	for rows.Next() {
		var problem Problem
		var firstSeen, lastSeen string
		if err := rows.Scan(&problem.ID, &problem.ProductLine, &problem.Fingerprint,
			&problem.Service, &problem.Environment, &problem.ExceptionType, &problem.Logger,
			&problem.Count, &firstSeen, &lastSeen, &problem.FirstSample, &problem.LatestSample); err != nil {
			return nil, err
		}
		problem.FirstSeen, _ = time.Parse(time.RFC3339Nano, firstSeen)
		problem.LastSeen, _ = time.Parse(time.RFC3339Nano, lastSeen)
		problems = append(problems, problem)
	}
	return problems, rows.Err()
}

func (s *Store) TrendPoints(ctx context.Context, productLine, fingerprint string) ([]TrendPoint, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT product_line, fingerprint, count, window_start, window_end
		FROM trend_points WHERE product_line = ? AND fingerprint = ? ORDER BY window_end`, productLine, fingerprint)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var points []TrendPoint
	for rows.Next() {
		var point TrendPoint
		var start, end string
		if err := rows.Scan(&point.ProductLine, &point.Fingerprint, &point.Count, &start, &end); err != nil {
			return nil, err
		}
		point.WindowStart, _ = time.Parse(time.RFC3339Nano, start)
		point.WindowEnd, _ = time.Parse(time.RFC3339Nano, end)
		points = append(points, point)
	}
	return points, rows.Err()
}

type Aggregator struct {
	store             *Store
	maxActive         int
	mu                sync.Mutex
	pending           map[string]aggregate
	dropped           uint64
	flushes           uint64
	flushFailures     uint64
	flushedEvents     uint64
	lastFlushDuration time.Duration
}

type AggregatorStats struct {
	PendingFingerprints int           `json:"pending_fingerprints"`
	DroppedEvents       uint64        `json:"dropped_events"`
	Flushes             uint64        `json:"flushes"`
	FlushFailures       uint64        `json:"flush_failures"`
	FlushedEvents       uint64        `json:"flushed_events"`
	LastFlushDuration   time.Duration `json:"-"`
}

func NewAggregator(store *Store, maxActive int) *Aggregator {
	if maxActive < 1 {
		maxActive = 1
	}
	return &Aggregator{store: store, maxActive: maxActive, pending: make(map[string]aggregate)}
}

func (a *Aggregator) Add(event Event) error {
	event, err := prepareEvent(event, time.Now().UTC())
	if err != nil {
		return err
	}
	fingerprint := Fingerprint(event)
	key := event.ProductLine + "\x00" + fingerprint
	a.mu.Lock()
	defer a.mu.Unlock()
	item, exists := a.pending[key]
	if !exists && len(a.pending) >= a.maxActive {
		a.dropped++
		return nil
	}
	if !exists {
		item = aggregate{Fingerprint: fingerprint, First: event}
	}
	item.Count++
	item.Latest = event
	a.pending[key] = item
	return nil
}

func (a *Aggregator) Flush(ctx context.Context) error {
	started := time.Now()
	a.mu.Lock()
	pending := a.pending
	a.pending = make(map[string]aggregate)
	a.mu.Unlock()
	if err := a.store.Flush(ctx, time.Now().UTC(), pending); err != nil {
		a.mu.Lock()
		a.flushFailures++
		a.lastFlushDuration = time.Since(started)
		for key, item := range pending {
			if current, ok := a.pending[key]; ok {
				item.Count += current.Count
				item.Latest = current.Latest
			}
			a.pending[key] = item
		}
		a.mu.Unlock()
		return err
	}
	var flushedEvents uint64
	for _, item := range pending {
		flushedEvents += uint64(item.Count)
	}
	a.mu.Lock()
	a.flushes++
	a.flushedEvents += flushedEvents
	a.lastFlushDuration = time.Since(started)
	a.mu.Unlock()
	return nil
}

func (a *Aggregator) Stats() AggregatorStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	return AggregatorStats{
		PendingFingerprints: len(a.pending),
		DroppedEvents:       a.dropped,
		Flushes:             a.flushes,
		FlushFailures:       a.flushFailures,
		FlushedEvents:       a.flushedEvents,
		LastFlushDuration:   a.lastFlushDuration,
	}
}

func (a *Aggregator) Dropped() uint64 { return a.Stats().DroppedEvents }

func (a *Aggregator) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = a.Flush(ctx)
		}
	}
}

func Handler(store *Store, aggregators ...*Aggregator) http.Handler {
	aggregator := NewAggregator(store, 10000)
	if len(aggregators) > 0 {
		aggregator = aggregators[0]
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		stats := aggregator.Stats()
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok",
			"aggregation": map[string]any{
				"pending_fingerprints":   stats.PendingFingerprints,
				"dropped_events":         stats.DroppedEvents,
				"flushes":                stats.Flushes,
				"flush_failures":         stats.FlushFailures,
				"flushed_events":         stats.FlushedEvents,
				"last_flush_duration_ms": float64(stats.LastFlushDuration.Microseconds()) / 1000,
			},
		})
	})
	mux.HandleFunc("POST /v1/events:batch", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Events []Event `json:"events"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		if len(request.Events) == 0 || len(request.Events) > 500 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "events must contain 1 to 500 items"})
			return
		}
		for index, event := range request.Events {
			if _, err := prepareEvent(event, time.Now().UTC()); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "event_index": index})
				return
			}
		}
		for index, event := range request.Events {
			if err := aggregator.Add(event); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "event_index": index})
				return
			}
		}
		writeJSON(w, http.StatusAccepted, map[string]int{"accepted": len(request.Events)})
	})
	mux.HandleFunc("GET /v1/problems", func(w http.ResponseWriter, r *http.Request) {
		productLine := r.URL.Query().Get("product_line")
		if productLine == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "product_line is required"})
			return
		}
		problems, err := store.Problems(r.Context(), productLine)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"problems": problems})
	})
	mux.HandleFunc("GET /v1/trends", func(w http.ResponseWriter, r *http.Request) {
		productLine := r.URL.Query().Get("product_line")
		fingerprint := r.URL.Query().Get("fingerprint")
		if productLine == "" || fingerprint == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "product_line and fingerprint are required"})
			return
		}
		points, err := store.TrendPoints(r.Context(), productLine, fingerprint)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"trend_points": points})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
