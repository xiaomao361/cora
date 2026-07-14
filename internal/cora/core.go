package cora

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
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
	ProductLine   string            `json:"product_line"`
	Service       string            `json:"service"`
	Environment   string            `json:"environment"`
	Release       string            `json:"release,omitempty"`
	Source        string            `json:"source,omitempty"`
	TraceID       string            `json:"trace_id,omitempty"`
	Thread        string            `json:"thread,omitempty"`
	Method        string            `json:"method,omitempty"`
	Line          string            `json:"line,omitempty"`
	Breadcrumbs   []Breadcrumb      `json:"breadcrumbs,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	Logger        string            `json:"logger"`
	ExceptionType string            `json:"exception_type"`
	Message       string            `json:"message"`
	Stacktrace    string            `json:"stacktrace"`
	OccurredAt    time.Time         `json:"occurred_at,omitempty"`
}

type Breadcrumb struct {
	OccurredAt time.Time `json:"occurred_at"`
	Level      string    `json:"level"`
	Logger     string    `json:"logger"`
	Thread     string    `json:"thread,omitempty"`
	Method     string    `json:"method,omitempty"`
	Line       string    `json:"line,omitempty"`
	Message    string    `json:"message"`
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
	Service     string    `json:"service"`
	Fingerprint string    `json:"fingerprint"`
	Count       int64     `json:"count"`
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`
}

type NodeOccurrence struct {
	ProductLine     string    `json:"product_line"`
	Service         string    `json:"service"`
	Fingerprint     string    `json:"fingerprint"`
	Node            string    `json:"node"`
	DeploymentGroup string    `json:"deployment_group,omitempty"`
	Environment     string    `json:"environment"`
	Count           int64     `json:"count"`
	FirstSeen       time.Time `json:"first_seen"`
	LastSeen        time.Time `json:"last_seen"`
}

type NodeTrendPoint struct {
	ProductLine     string    `json:"product_line"`
	Service         string    `json:"service"`
	Fingerprint     string    `json:"fingerprint"`
	Node            string    `json:"node"`
	DeploymentGroup string    `json:"deployment_group,omitempty"`
	Count           int64     `json:"count"`
	WindowStart     time.Time `json:"window_start"`
	WindowEnd       time.Time `json:"window_end"`
}

type nodeAggregate struct {
	Node            string
	DeploymentGroup string
	Count           int64
	First           Event
	Latest          Event
}

type aggregate struct {
	Fingerprint string
	Count       int64
	First       Event
	Latest      Event
	Nodes       map[string]nodeAggregate
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

type Store struct {
	db   *sql.DB
	cora Cora
}

func OpenStore(path string) (*Store, error) {
	core, err := defaultCoraCore()
	if err != nil {
		return nil, err
	}
	return OpenStoreWithCora(path, core)
}

func OpenStoreWithCora(path string, core Cora) (*Store, error) {
	if core == nil {
		return nil, errors.New("Cora core is required")
	}
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
	return &Store{db: db, cora: core}, nil
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
	{version: 3, statements: []string{
		`CREATE TABLE IF NOT EXISTS cora_decisions (
			id INTEGER PRIMARY KEY,
			product_line TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			decision TEXT NOT NULL CHECK(decision IN ('attention', 'observe', 'ignore')),
			category TEXT NOT NULL,
			rule_id TEXT NOT NULL,
			reason TEXT NOT NULL,
			source TEXT NOT NULL,
			experience_version TEXT NOT NULL,
			decided_at TEXT NOT NULL,
			UNIQUE(product_line, fingerprint)
		)`,
		`CREATE INDEX IF NOT EXISTS cora_decisions_line_decision
		 ON cora_decisions(product_line, decision)`,
	}},
	{version: 4, statements: []string{
		`CREATE TABLE problems_v4 (
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
			UNIQUE(product_line, service, fingerprint)
		)`,
		`INSERT INTO problems_v4 SELECT * FROM problems`,
		`DROP TABLE problems`,
		`ALTER TABLE problems_v4 RENAME TO problems`,
		`CREATE TABLE trend_points_v4 (
			id INTEGER PRIMARY KEY,
			product_line TEXT NOT NULL,
			service TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			count INTEGER NOT NULL,
			window_start TEXT NOT NULL,
			window_end TEXT NOT NULL
		)`,
		`INSERT INTO trend_points_v4
			(id, product_line, service, fingerprint, count, window_start, window_end)
			SELECT t.id, t.product_line, p.service, t.fingerprint, t.count, t.window_start, t.window_end
			FROM trend_points t
			JOIN problems p ON p.product_line = t.product_line AND p.fingerprint = t.fingerprint`,
		`DROP TABLE trend_points`,
		`ALTER TABLE trend_points_v4 RENAME TO trend_points`,
		`CREATE INDEX trend_points_line_service_fingerprint_time
			ON trend_points(product_line, service, fingerprint, window_end)`,
		`CREATE TABLE cora_decisions_v4 (
			id INTEGER PRIMARY KEY,
			product_line TEXT NOT NULL,
			service TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			decision TEXT NOT NULL CHECK(decision IN ('attention', 'observe', 'ignore')),
			category TEXT NOT NULL,
			rule_id TEXT NOT NULL,
			reason TEXT NOT NULL,
			source TEXT NOT NULL,
			experience_version TEXT NOT NULL,
			decided_at TEXT NOT NULL,
			UNIQUE(product_line, service, fingerprint)
		)`,
		`INSERT INTO cora_decisions_v4
			(id, product_line, service, fingerprint, decision, category, rule_id, reason,
			 source, experience_version, decided_at)
			SELECT d.id, d.product_line, p.service, d.fingerprint, d.decision, d.category,
			       d.rule_id, d.reason, d.source, d.experience_version, d.decided_at
			FROM cora_decisions d
			JOIN problems p ON p.product_line = d.product_line AND p.fingerprint = d.fingerprint`,
		`DROP TABLE cora_decisions`,
		`ALTER TABLE cora_decisions_v4 RENAME TO cora_decisions`,
		`CREATE INDEX cora_decisions_line_decision
			ON cora_decisions(product_line, decision)`,
		`CREATE TABLE node_occurrences (
			id INTEGER PRIMARY KEY,
			product_line TEXT NOT NULL,
			service TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			node TEXT NOT NULL,
			deployment_group TEXT NOT NULL,
			environment TEXT NOT NULL,
			count INTEGER NOT NULL,
			first_seen TEXT NOT NULL,
			last_seen TEXT NOT NULL,
			UNIQUE(product_line, service, fingerprint, node)
		)`,
		`CREATE INDEX node_occurrences_problem
			ON node_occurrences(product_line, service, fingerprint, count DESC)`,
		`CREATE TABLE node_trend_points (
			id INTEGER PRIMARY KEY,
			product_line TEXT NOT NULL,
			service TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			node TEXT NOT NULL,
			deployment_group TEXT NOT NULL,
			count INTEGER NOT NULL,
			window_start TEXT NOT NULL,
			window_end TEXT NOT NULL
		)`,
		`CREATE INDEX node_trend_points_problem_time
			ON node_trend_points(product_line, service, fingerprint, node, window_end)`,
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
		aggregateKey(event, fingerprint): newAggregate(event, fingerprint),
	})
}

func aggregateKey(event Event, fingerprint string) string {
	return event.ProductLine + "\x00" + event.Service + "\x00" + fingerprint
}

func nodeIdentity(event Event) (string, string) {
	node := event.Labels["node"]
	if node == "" {
		node = event.Labels["server"]
	}
	if node == "" {
		node = "unknown"
	}
	group := event.Labels["deployment_group"]
	if group == "" {
		group = event.Labels["group"]
	}
	return node, group
}

func newAggregate(event Event, fingerprint string) aggregate {
	node, group := nodeIdentity(event)
	return aggregate{
		Fingerprint: fingerprint, Count: 1, First: event, Latest: event,
		Nodes: map[string]nodeAggregate{node: {
			Node: node, DeploymentGroup: group, Count: 1, First: event, Latest: event,
		}},
	}
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
		var previousCount int64
		var storedFirstSeen, storedLastSeen, storedFirstSample, storedLatestSample string
		firstOccurrence := false
		err := tx.QueryRowContext(ctx, `SELECT count, first_seen, last_seen, first_sample, latest_sample
			FROM problems WHERE product_line = ? AND service = ? AND fingerprint = ?`,
			item.First.ProductLine, item.First.Service, item.Fingerprint).
			Scan(&previousCount, &storedFirstSeen, &storedLastSeen, &storedFirstSample, &storedLatestSample)
		if errors.Is(err, sql.ErrNoRows) {
			firstOccurrence = true
			previousCount = 0
		} else if err != nil {
			return err
		}
		firstSeen := item.First.OccurredAt
		lastSeen := item.Latest.OccurredAt
		firstSample, err := json.Marshal(item.First)
		if err != nil {
			return err
		}
		latestSample, err := json.Marshal(item.Latest)
		if err != nil {
			return err
		}
		decisionEvent := item.Latest
		if !firstOccurrence {
			storedFirstTime, err := time.Parse(time.RFC3339Nano, storedFirstSeen)
			if err != nil {
				return fmt.Errorf("parse stored first_seen: %w", err)
			}
			storedLastTime, err := time.Parse(time.RFC3339Nano, storedLastSeen)
			if err != nil {
				return fmt.Errorf("parse stored last_seen: %w", err)
			}
			if !firstSeen.Before(storedFirstTime) {
				firstSeen = storedFirstTime
				firstSample = []byte(storedFirstSample)
			}
			if !lastSeen.After(storedLastTime) {
				lastSeen = storedLastTime
				latestSample = []byte(storedLatestSample)
				var storedLatest Event
				if json.Unmarshal([]byte(storedLatestSample), &storedLatest) == nil && storedLatest.ProductLine != "" {
					decisionEvent = storedLatest
				}
			}
		}
		decision, err := s.cora.Decide(ctx, DecisionRequest{
			Event: decisionEvent, Fingerprint: item.Fingerprint,
			OccurrenceCount: previousCount + item.Count, FirstOccurrence: firstOccurrence,
		})
		if err != nil {
			decision = CoraDecision{
				Decision: DecisionObserve, Category: "core-unavailable",
				RuleID: "cora.default.core-unavailable",
				Reason: "Cora could not decide; keep visible for review",
				Source: "framework_default", DecidedAt: windowEnd,
			}
		} else if !validDecision(decision.Decision) {
			decision = CoraDecision{
				Decision: DecisionObserve, Category: "invalid-core-decision",
				RuleID: "cora.default.invalid-core-decision",
				Reason: "Cora returned an invalid decision; keep visible for review",
				Source: "framework_default", DecidedAt: windowEnd,
			}
		}
		if decision.DecidedAt.IsZero() {
			decision.DecidedAt = windowEnd
		}
		_, err = tx.ExecContext(ctx, `
		INSERT INTO problems (
			product_line, fingerprint, service, environment, exception_type, logger,
			count, first_seen, last_seen, first_sample, latest_sample
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(product_line, service, fingerprint) DO UPDATE SET
			count = count + excluded.count,
			first_seen = excluded.first_seen,
			first_sample = excluded.first_sample,
			last_seen = excluded.last_seen,
			latest_sample = excluded.latest_sample`,
			item.First.ProductLine, item.Fingerprint, item.First.Service, item.First.Environment,
			item.First.ExceptionType, item.First.Logger, item.Count,
			firstSeen.Format(time.RFC3339Nano), lastSeen.Format(time.RFC3339Nano),
			string(firstSample), string(latestSample))
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO cora_decisions
			(product_line, service, fingerprint, decision, category, rule_id, reason, source,
			 experience_version, decided_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(product_line, service, fingerprint) DO UPDATE SET
				decision = excluded.decision,
				category = excluded.category,
				rule_id = excluded.rule_id,
				reason = excluded.reason,
				source = excluded.source,
				experience_version = excluded.experience_version,
				decided_at = excluded.decided_at`,
			item.First.ProductLine, item.First.Service, item.Fingerprint, decision.Decision, decision.Category,
			decision.RuleID, decision.Reason, decision.Source, decision.ExperienceVersion,
			decision.DecidedAt.Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO trend_points
			(product_line, service, fingerprint, count, window_start, window_end) VALUES (?, ?, ?, ?, ?, ?)`,
			item.First.ProductLine, item.First.Service, item.Fingerprint, item.Count,
			item.First.OccurredAt.Format(time.RFC3339Nano), windowEnd.Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
		for _, node := range item.Nodes {
			_, err = tx.ExecContext(ctx, `INSERT INTO node_occurrences
				(product_line, service, fingerprint, node, deployment_group, environment,
				 count, first_seen, last_seen) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(product_line, service, fingerprint, node) DO UPDATE SET
					count = count + excluded.count,
					first_seen = CASE WHEN excluded.first_seen < first_seen THEN excluded.first_seen ELSE first_seen END,
					last_seen = CASE WHEN excluded.last_seen > last_seen THEN excluded.last_seen ELSE last_seen END,
					deployment_group = CASE WHEN excluded.last_seen > last_seen THEN excluded.deployment_group ELSE deployment_group END,
					environment = CASE WHEN excluded.last_seen > last_seen THEN excluded.environment ELSE environment END`,
				item.First.ProductLine, item.First.Service, item.Fingerprint, node.Node,
				node.DeploymentGroup, node.Latest.Environment, node.Count,
				node.First.OccurredAt.UTC().Format(time.RFC3339Nano), node.Latest.OccurredAt.UTC().Format(time.RFC3339Nano))
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO node_trend_points
				(product_line, service, fingerprint, node, deployment_group, count,
				 window_start, window_end) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				item.First.ProductLine, item.First.Service, item.Fingerprint, node.Node,
				node.DeploymentGroup, node.Count, node.First.OccurredAt.UTC().Format(time.RFC3339Nano),
				windowEnd.Format(time.RFC3339Nano))
			if err != nil {
				return err
			}
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

func (s *Store) TrendPoints(ctx context.Context, productLine, service, fingerprint string) ([]TrendPoint, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT product_line, service, fingerprint, count, window_start, window_end
		FROM trend_points WHERE product_line = ? AND service = ? AND fingerprint = ? ORDER BY window_end`,
		productLine, service, fingerprint)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var points []TrendPoint
	for rows.Next() {
		var point TrendPoint
		var start, end string
		if err := rows.Scan(&point.ProductLine, &point.Service, &point.Fingerprint, &point.Count, &start, &end); err != nil {
			return nil, err
		}
		point.WindowStart, _ = time.Parse(time.RFC3339Nano, start)
		point.WindowEnd, _ = time.Parse(time.RFC3339Nano, end)
		points = append(points, point)
	}
	return points, rows.Err()
}

func (s *Store) NodeOccurrences(ctx context.Context, productLine, service, fingerprint string) ([]NodeOccurrence, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT product_line, service, fingerprint, node,
		deployment_group, environment, count, first_seen, last_seen
		FROM node_occurrences
		WHERE product_line = ? AND service = ? AND fingerprint = ?
		ORDER BY count DESC, node`, productLine, service, fingerprint)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []NodeOccurrence{}
	for rows.Next() {
		var item NodeOccurrence
		var firstSeen, lastSeen string
		if err := rows.Scan(&item.ProductLine, &item.Service, &item.Fingerprint, &item.Node,
			&item.DeploymentGroup, &item.Environment, &item.Count, &firstSeen, &lastSeen); err != nil {
			return nil, err
		}
		item.FirstSeen, _ = time.Parse(time.RFC3339Nano, firstSeen)
		item.LastSeen, _ = time.Parse(time.RFC3339Nano, lastSeen)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) NodeTrendPoints(ctx context.Context, productLine, service, fingerprint, node string) ([]NodeTrendPoint, error) {
	query := `SELECT product_line, service, fingerprint, node, deployment_group, count, window_start, window_end
		FROM node_trend_points WHERE product_line = ? AND service = ? AND fingerprint = ?`
	args := []any{productLine, service, fingerprint}
	if node != "" {
		query += ` AND node = ?`
		args = append(args, node)
	}
	query += ` ORDER BY window_end, node`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []NodeTrendPoint{}
	for rows.Next() {
		var item NodeTrendPoint
		var start, end string
		if err := rows.Scan(&item.ProductLine, &item.Service, &item.Fingerprint, &item.Node,
			&item.DeploymentGroup, &item.Count, &start, &end); err != nil {
			return nil, err
		}
		item.WindowStart, _ = time.Parse(time.RFC3339Nano, start)
		item.WindowEnd, _ = time.Parse(time.RFC3339Nano, end)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) Attention(ctx context.Context, productLine string) ([]AttentionItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, p.product_line, p.fingerprint, p.service, p.environment,
		       p.exception_type, p.logger, p.count, p.last_seen,
		       d.decision, d.category, d.rule_id, d.reason, d.source,
		       d.experience_version, d.decided_at
		FROM problems p
		JOIN cora_decisions d ON d.product_line = p.product_line AND d.service = p.service
		 AND d.fingerprint = p.fingerprint
		WHERE p.product_line = ? AND d.decision != 'ignore'
		ORDER BY CASE d.decision WHEN 'attention' THEN 0 ELSE 1 END, p.last_seen DESC`, productLine)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []AttentionItem{}
	for rows.Next() {
		var item AttentionItem
		var lastSeen, decidedAt string
		if err := rows.Scan(&item.ProblemID, &item.ProductLine, &item.Fingerprint,
			&item.Service, &item.Environment, &item.ExceptionType, &item.Logger,
			&item.Count, &lastSeen, &item.Decision, &item.Category, &item.RuleID,
			&item.Reason, &item.Source, &item.ExperienceVersion, &decidedAt); err != nil {
			return nil, err
		}
		item.LastSeen, _ = time.Parse(time.RFC3339Nano, lastSeen)
		item.DecidedAt, _ = time.Parse(time.RFC3339Nano, decidedAt)
		items = append(items, item)
	}
	return items, rows.Err()
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
	key := aggregateKey(event, fingerprint)
	a.mu.Lock()
	defer a.mu.Unlock()
	item, exists := a.pending[key]
	if !exists && len(a.pending) >= a.maxActive {
		a.dropped++
		return nil
	}
	if !exists {
		item = newAggregate(event, fingerprint)
	} else {
		if event.OccurredAt.Before(item.First.OccurredAt) {
			item.First = event
		}
		if event.OccurredAt.After(item.Latest.OccurredAt) {
			item.Latest = event
		}
		node, group := nodeIdentity(event)
		nodeItem, nodeExists := item.Nodes[node]
		if !nodeExists {
			nodeItem = nodeAggregate{Node: node, DeploymentGroup: group, First: event, Latest: event}
		} else {
			if event.OccurredAt.Before(nodeItem.First.OccurredAt) {
				nodeItem.First = event
			}
			if event.OccurredAt.After(nodeItem.Latest.OccurredAt) {
				nodeItem.Latest = event
				nodeItem.DeploymentGroup = group
			}
		}
		nodeItem.Count++
		item.Nodes[node] = nodeItem
	}
	if exists {
		item.Count++
	}
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
				if current.First.OccurredAt.Before(item.First.OccurredAt) {
					item.First = current.First
				}
				if current.Latest.OccurredAt.After(item.Latest.OccurredAt) {
					item.Latest = current.Latest
				}
				for node, currentNode := range current.Nodes {
					if pendingNode, exists := item.Nodes[node]; exists {
						pendingNode.Count += currentNode.Count
						if currentNode.First.OccurredAt.Before(pendingNode.First.OccurredAt) {
							pendingNode.First = currentNode.First
						}
						if currentNode.Latest.OccurredAt.After(pendingNode.Latest.OccurredAt) {
							pendingNode.Latest = currentNode.Latest
							pendingNode.DeploymentGroup = currentNode.DeploymentGroup
						}
						item.Nodes[node] = pendingNode
					} else {
						item.Nodes[node] = currentNode
					}
				}
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

type HandlerOptions struct {
	BearerToken string
}

func Handler(store *Store, aggregators ...*Aggregator) http.Handler {
	return HandlerWithOptions(store, HandlerOptions{}, aggregators...)
}

func HandlerWithOptions(store *Store, options HandlerOptions, aggregators ...*Aggregator) http.Handler {
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
	mux.HandleFunc("GET /v1/attention", func(w http.ResponseWriter, r *http.Request) {
		productLine := r.URL.Query().Get("product_line")
		if productLine == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "product_line is required"})
			return
		}
		items, err := store.Attention(r.Context(), productLine)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"attention": items})
	})
	mux.HandleFunc("GET /v1/trends", func(w http.ResponseWriter, r *http.Request) {
		productLine := r.URL.Query().Get("product_line")
		service := r.URL.Query().Get("service")
		fingerprint := r.URL.Query().Get("fingerprint")
		if productLine == "" || service == "" || fingerprint == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "product_line, service, and fingerprint are required"})
			return
		}
		points, err := store.TrendPoints(r.Context(), productLine, service, fingerprint)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"trend_points": points})
	})
	mux.HandleFunc("GET /v1/node-occurrences", func(w http.ResponseWriter, r *http.Request) {
		productLine := r.URL.Query().Get("product_line")
		service := r.URL.Query().Get("service")
		fingerprint := r.URL.Query().Get("fingerprint")
		if productLine == "" || service == "" || fingerprint == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "product_line, service, and fingerprint are required"})
			return
		}
		items, err := store.NodeOccurrences(r.Context(), productLine, service, fingerprint)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"node_occurrences": items})
	})
	mux.HandleFunc("GET /v1/node-trends", func(w http.ResponseWriter, r *http.Request) {
		productLine := r.URL.Query().Get("product_line")
		service := r.URL.Query().Get("service")
		fingerprint := r.URL.Query().Get("fingerprint")
		if productLine == "" || service == "" || fingerprint == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "product_line, service, and fingerprint are required"})
			return
		}
		items, err := store.NodeTrendPoints(r.Context(), productLine, service, fingerprint, r.URL.Query().Get("node"))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"node_trend_points": items})
	})
	if options.BearerToken == "" {
		return mux
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			expected := "Bearer " + options.BearerToken
			provided := r.Header.Get("Authorization")
			if len(provided) != len(expected) || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
