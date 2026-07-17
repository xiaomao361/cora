package retention

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const logicalEstimateMethod = "UTF-8 bytes in eligible first/latest samples plus text fields and 8 bytes per integer in eligible trend rows; excludes SQLite record headers, indexes, B-tree overhead, and retained identity rows"
const physicalCaveat = "This is logical payload, not exact physical recovery. DELETE would only make pages reusable; file shrink requires a separate controlled checkpoint/VACUUM maintenance action."

type databaseSnapshot struct {
	digest string
	size   int64
	mtime  time.Time
}

type rawProblem struct {
	id, count, cases, handled                                                  int64
	productLine, service, fingerprint, rootCauseKey, state, decision, lastSeen string
}

func RunAudit(ctx context.Context, config Config) (Result, error) {
	config, err := normalizeConfig(config)
	if err != nil {
		return Result{}, err
	}
	before, err := snapshotDatabase(config.DatabasePath)
	if err != nil {
		return Result{}, fmt.Errorf("snapshot database before audit: %w", err)
	}

	db, err := openReadOnly(config.DatabasePath)
	if err != nil {
		return Result{}, err
	}
	defer db.Close()

	artifacts, _, err := buildArtifactIndex(config.IterationRoot, config.ClosureRoot)
	if err != nil {
		return Result{}, fmt.Errorf("index retention artifacts: %w", err)
	}
	receipts, diagnostics, err := loadReceipts(config.ClosureRoot, artifacts)
	if err != nil {
		return Result{}, fmt.Errorf("load closure receipts: %w", err)
	}

	audit, usedInputs, err := buildAudit(ctx, db, config, before, receipts, diagnostics, artifacts)
	if err != nil {
		return Result{}, err
	}
	if err := db.Close(); err != nil {
		return Result{}, err
	}
	after, err := snapshotDatabase(config.DatabasePath)
	if err != nil {
		return Result{}, fmt.Errorf("snapshot database after audit: %w", err)
	}
	if before != after {
		return Result{}, errors.New("read-only audit changed database sha256, size, or mtime")
	}

	result, err := writeAudit(config, audit, usedInputs)
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func normalizeConfig(config Config) (Config, error) {
	if strings.TrimSpace(config.DatabasePath) == "" || strings.TrimSpace(config.ProductLine) == "" || strings.TrimSpace(config.CoraBuildVersion) == "" || strings.TrimSpace(config.OutputRoot) == "" || strings.TrimSpace(config.RunID) == "" {
		return Config{}, errors.New("database path, product line, Cora build version, output root, and run ID are required")
	}
	if !sha256Pattern.MatchString(config.CoraSourceDigest) {
		return Config{}, errors.New("Cora source digest must be a lowercase SHA-256")
	}
	if !stableIDPattern.MatchString(config.RunID) {
		return Config{}, errors.New("run ID must be a stable identifier")
	}
	var err error
	config.DatabasePath, err = filepath.Abs(config.DatabasePath)
	if err != nil {
		return Config{}, err
	}
	config.IterationRoot = cleanOptional(config.IterationRoot)
	config.ClosureRoot = cleanOptional(config.ClosureRoot)
	config.OutputRoot, err = filepath.Abs(config.OutputRoot)
	if err != nil {
		return Config{}, err
	}
	return config, nil
}

func cleanOptional(path string) string {
	if path == "" {
		return ""
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return absolute
}

func snapshotDatabase(path string) (databaseSnapshot, error) {
	info, err := os.Stat(path)
	if err != nil {
		return databaseSnapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return databaseSnapshot{}, errors.New("database path is not a regular file")
	}
	digest, size, err := hashFile(path)
	if err != nil {
		return databaseSnapshot{}, err
	}
	return databaseSnapshot{digest: digest, size: size, mtime: info.ModTime().UTC()}, nil
}

func openReadOnly(path string) (*sql.DB, error) {
	// The audit accepts only a completed, consistent backup. immutable=1 prevents
	// SQLite from creating or touching WAL/SHM sidecars while inspecting it.
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&immutable=1"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open SQLite backup read-only: %w", err)
	}
	return db, nil
}

func buildAudit(ctx context.Context, db *sql.DB, config Config, database databaseSnapshot, receipts []loadedReceipt, diagnostics []ReceiptDiagnostic, artifacts artifactIndex) (Audit, []Artifact, error) {
	var schemaVersion int
	var storage StorageStats
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&schemaVersion); err != nil {
		return Audit{}, nil, err
	}
	if err := db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&storage.PageCount); err != nil {
		return Audit{}, nil, err
	}
	if err := db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&storage.PageSizeBytes); err != nil {
		return Audit{}, nil, err
	}
	if err := db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&storage.FreelistCount); err != nil {
		return Audit{}, nil, err
	}
	if err := db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&storage.QuickCheck); err != nil {
		return Audit{}, nil, err
	}
	if storage.QuickCheck != "ok" {
		return Audit{}, nil, fmt.Errorf("SQLite quick_check failed: %s", storage.QuickCheck)
	}
	storage.DatabaseBytesByPages = storage.PageCount * storage.PageSizeBytes
	storage.ReusableFreelistBytes = storage.FreelistCount * storage.PageSizeBytes
	storage.WALSizeBytes = fileSize(config.DatabasePath + "-wal")
	storage.SHMSizeBytes = fileSize(config.DatabasePath + "-shm")

	tables, err := readTableStats(ctx, db, config.ProductLine)
	if err != nil {
		return Audit{}, nil, err
	}
	problems, err := readProblems(ctx, db, config.ProductLine)
	if err != nil {
		return Audit{}, nil, err
	}
	receiptByProblem := map[string][]loadedReceipt{}
	invalidReceiptFiles := 0
	for _, receipt := range receipts {
		if receipt.receipt.ProductLine != config.ProductLine {
			continue
		}
		if len(receipt.reasons) > 0 {
			invalidReceiptFiles++
		}
		key := problemKey(receipt.receipt.Service, receipt.receipt.Fingerprint)
		receiptByProblem[key] = append(receiptByProblem[key], receipt)
	}

	audit := Audit{
		SchemaVersion: AuditSchemaVersion, AuditRunID: config.RunID, ProductLine: config.ProductLine,
		CapturedAt:        database.mtime,
		Database:          DatabaseIdentity{Path: filepath.Base(config.DatabasePath), SHA256: database.digest, SizeBytes: database.size, ModifiedAt: database.mtime, SchemaVersion: schemaVersion},
		CoraBuild:         CoraBuildIdentity{Version: config.CoraBuildVersion, SourceDigest: config.CoraSourceDigest},
		Storage:           storage,
		Tables:            tables,
		ProblemDecisions:  []ProblemDecision{},
		UnmatchedReceipts: []ReceiptDiagnostic{},
		LogicalRelease:    LogicalRelease{Method: logicalEstimateMethod, PhysicalCaveat: physicalCaveat},
	}
	validProblemKeys := map[string]bool{}
	usedDigests := map[string]bool{database.digest: true}
	for _, problem := range problems {
		key := problemKey(problem.service, problem.fingerprint)
		validProblemKeys[key] = true
		decision, digests, err := decideProblem(ctx, db, problem, receiptByProblem[key], artifacts, database.mtime)
		if err != nil {
			return Audit{}, nil, err
		}
		for _, digest := range digests {
			usedDigests[digest] = true
		}
		audit.ProblemDecisions = append(audit.ProblemDecisions, decision)
		if decision.RetentionEligible {
			audit.Summary.EligibleProblems++
			audit.LogicalRelease.EstimatedRows += decision.EstimatedReleasableRows
			audit.LogicalRelease.EstimatedBytes += decision.EstimatedReleasableBytes
		} else {
			audit.Summary.IneligibleProblems++
		}
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.ProductLine == "" || diagnostic.ProductLine == config.ProductLine {
			audit.UnmatchedReceipts = append(audit.UnmatchedReceipts, diagnostic)
			invalidReceiptFiles++
		}
	}
	for key, values := range receiptByProblem {
		if validProblemKeys[key] {
			continue
		}
		for _, receipt := range values {
			audit.UnmatchedReceipts = append(audit.UnmatchedReceipts, ReceiptDiagnostic{Path: receipt.path, ProductLine: receipt.receipt.ProductLine, Service: receipt.receipt.Service, Fingerprint: receipt.receipt.Fingerprint, Reasons: []string{"receipt_problem_not_found"}})
		}
	}
	sort.Slice(audit.UnmatchedReceipts, func(i, j int) bool { return audit.UnmatchedReceipts[i].Path < audit.UnmatchedReceipts[j].Path })
	audit.Summary.ProblemCount = len(problems)
	audit.Summary.InvalidReceiptFiles = invalidReceiptFiles
	audit.Breakdowns = buildBreakdowns(problems)
	inputs := []Artifact{{Path: filepath.Base(config.DatabasePath), SHA256: database.digest, Bytes: database.size}}
	for digest := range usedDigests {
		if digest == database.digest {
			continue
		}
		for _, path := range artifacts[digest] {
			inputs = append(inputs, Artifact{Path: path.display, SHA256: digest, Bytes: path.bytes})
			break
		}
	}
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].Path < inputs[j].Path })
	return audit, inputs, nil
}

func readTableStats(ctx context.Context, db *sql.DB, productLine string) ([]TableStats, error) {
	specs := []struct{ table, earliest, latest string }{
		{"problems", "first_seen", "last_seen"}, {"trend_points", "window_start", "window_end"},
		{"node_occurrences", "first_seen", "last_seen"}, {"node_trend_points", "window_start", "window_end"},
		{"cora_decisions", "decided_at", "decided_at"}, {"problem_cases", "recorded_at", "recorded_at"},
	}
	stats := make([]TableStats, 0, len(specs))
	for _, spec := range specs {
		query := fmt.Sprintf("SELECT COUNT(*), COALESCE(MIN(%s), ''), COALESCE(MAX(%s), '') FROM %s WHERE product_line = ?", spec.earliest, spec.latest, spec.table)
		item := TableStats{Table: spec.table}
		if err := db.QueryRowContext(ctx, query, productLine).Scan(&item.Rows, &item.EarliestAt, &item.LatestAt); err != nil {
			return nil, fmt.Errorf("read %s stats: %w", spec.table, err)
		}
		stats = append(stats, item)
	}
	return stats, nil
}

func readProblems(ctx context.Context, db *sql.DB, productLine string) ([]rawProblem, error) {
	rows, err := db.QueryContext(ctx, `SELECT p.id, p.product_line, p.service, p.fingerprint, p.root_cause_key, p.state,
		COALESCE(d.decision, 'none'), p.count, p.last_seen,
		COUNT(c.id), COALESCE(SUM(CASE WHEN c.handled = 1 THEN 1 ELSE 0 END), 0)
		FROM problems p
		LEFT JOIN cora_decisions d ON d.product_line=p.product_line AND d.service=p.service AND d.fingerprint=p.fingerprint
		 AND d.root_cause_key=p.root_cause_key
		LEFT JOIN problem_cases c ON c.problem_id=p.id AND c.product_line=p.product_line
		WHERE p.product_line=?
		GROUP BY p.id, p.product_line, p.service, p.fingerprint, p.root_cause_key, p.state, d.decision, p.count, p.last_seen
		ORDER BY p.service, p.fingerprint, p.id`, productLine)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []rawProblem
	for rows.Next() {
		var item rawProblem
		if err := rows.Scan(&item.id, &item.productLine, &item.service, &item.fingerprint, &item.rootCauseKey, &item.state, &item.decision, &item.count, &item.lastSeen, &item.cases, &item.handled); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func decideProblem(ctx context.Context, db *sql.DB, problem rawProblem, receipts []loadedReceipt, artifacts artifactIndex, capturedAt time.Time) (ProblemDecision, []string, error) {
	decision := ProblemDecision{ProblemID: problem.id, ProductLine: problem.productLine, Service: problem.service, Fingerprint: problem.fingerprint, RootCauseKey: problem.rootCauseKey, State: problem.state, Decision: problem.decision, OccurrenceCount: problem.count, LastSeen: problem.lastSeen, CaseCount: problem.cases, HandledCaseCount: problem.handled}
	baseReasons := []string{}
	if problem.state != "resolved" {
		baseReasons = append(baseReasons, "problem_active")
	}
	if problem.handled == 0 {
		baseReasons = append(baseReasons, "handled_case_missing")
	}
	if len(receipts) == 0 {
		baseReasons = append(baseReasons, "closure_receipt_missing")
	}
	var best *loadedReceipt
	var bestReasons []string
	for index := range receipts {
		receipt := &receipts[index]
		reasons := append([]string{}, receipt.reasons...)
		if receipt.receipt.ProductLine != problem.productLine || receipt.receipt.Service != problem.service || receipt.receipt.Fingerprint != problem.fingerprint {
			reasons = append(reasons, "receipt_problem_identity_mismatch")
		}
		caseReasons, err := validateReceiptCases(ctx, db, problem, receipt.receipt.CaseSnapshot.CaseIDs)
		if err != nil {
			return decision, nil, err
		}
		reasons = append(reasons, caseReasons...)
		lastSeen, err := time.Parse(time.RFC3339Nano, problem.lastSeen)
		if err != nil {
			reasons = append(reasons, "problem_last_seen_invalid")
		} else if !receipt.receipt.Observation.EndsAt.IsZero() && lastSeen.After(receipt.receipt.Observation.EndsAt) {
			reasons = append(reasons, "occurrence_after_observation")
		}
		if !receipt.receipt.Observation.EndsAt.IsZero() && capturedAt.Before(receipt.receipt.Observation.EndsAt) {
			reasons = append(reasons, "observation_window_not_elapsed_at_capture")
		}
		reasons = sortedUnique(append(baseReasons, reasons...))
		if best == nil || len(reasons) < len(bestReasons) || (len(reasons) == len(bestReasons) && receipt.receipt.ClosureReceiptID < best.receipt.ClosureReceiptID) {
			best, bestReasons = receipt, reasons
		}
	}
	if best == nil {
		decision.BlockingReasons = sortedUnique(baseReasons)
		return decision, nil, nil
	}
	decision.ClosureReceiptID = best.receipt.ClosureReceiptID
	decision.BlockingReasons = bestReasons
	if len(bestReasons) == 0 {
		decision.RetentionEligible = true
		rows, bytes, err := estimateLogicalRelease(ctx, db, problem)
		if err != nil {
			return decision, nil, err
		}
		decision.EstimatedReleasableRows, decision.EstimatedReleasableBytes = rows, bytes
	}
	digests := []string{best.digest, best.receipt.CaseSnapshot.ManifestSHA256, best.receipt.Rule.PackSHA256, best.receipt.Evaluation.ArtifactSHA256, best.receipt.Observation.EvidenceSHA256}
	if snapshotDigest := referencedCaseSnapshotDigest(best.receipt, artifacts); snapshotDigest != "" {
		digests = append(digests, snapshotDigest)
	}
	return decision, digests, nil
}

func validateReceiptCases(ctx context.Context, db *sql.DB, problem rawProblem, ids []int64) ([]string, error) {
	if len(ids) == 0 {
		return []string{"case_snapshot_case_ids_invalid"}, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, problem.id)
	for _, id := range ids {
		args = append(args, id)
	}
	query := fmt.Sprintf("SELECT COUNT(*), COALESCE(SUM(CASE WHEN handled=1 THEN 1 ELSE 0 END),0) FROM problem_cases WHERE problem_id=? AND id IN (%s)", placeholders)
	var count, handled int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&count, &handled); err != nil {
		return nil, err
	}
	var reasons []string
	if count != len(ids) {
		reasons = append(reasons, "receipt_cases_not_found")
	}
	if handled != len(ids) {
		reasons = append(reasons, "receipt_cases_not_handled")
	}
	return reasons, nil
}

func estimateLogicalRelease(ctx context.Context, db *sql.DB, problem rawProblem) (int64, int64, error) {
	var sampleBytes int64
	if err := db.QueryRowContext(ctx, `SELECT length(CAST(first_sample AS BLOB)) + length(CAST(latest_sample AS BLOB)) FROM problems WHERE id=?`, problem.id).Scan(&sampleBytes); err != nil {
		return 0, 0, err
	}
	var trendRows, trendBytes int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(length(product_line)+length(service)+length(fingerprint)+length(root_cause_key)+length(window_start)+length(window_end)+16),0) FROM trend_points WHERE product_line=? AND service=? AND fingerprint=? AND root_cause_key=?`, problem.productLine, problem.service, problem.fingerprint, problem.rootCauseKey).Scan(&trendRows, &trendBytes); err != nil {
		return 0, 0, err
	}
	var nodeRows, nodeBytes int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(length(product_line)+length(service)+length(fingerprint)+length(root_cause_key)+length(node)+length(deployment_group)+length(window_start)+length(window_end)+16),0) FROM node_trend_points WHERE product_line=? AND service=? AND fingerprint=? AND root_cause_key=?`, problem.productLine, problem.service, problem.fingerprint, problem.rootCauseKey).Scan(&nodeRows, &nodeBytes); err != nil {
		return 0, 0, err
	}
	return trendRows + nodeRows, sampleBytes + trendBytes + nodeBytes, nil
}

func buildBreakdowns(problems []rawProblem) Breakdowns {
	state, decision, handled, cases := map[string]*CountBucket{}, map[string]*CountBucket{}, map[string]*CountBucket{}, map[string]*CountBucket{}
	add := func(target map[string]*CountBucket, key string, item rawProblem) {
		bucket := target[key]
		if bucket == nil {
			bucket = &CountBucket{Key: key}
			target[key] = bucket
		}
		bucket.Problems++
		bucket.Occurrences += item.count
	}
	for _, item := range problems {
		add(state, item.state, item)
		add(decision, item.decision, item)
		if item.handled > 0 {
			add(handled, "handled", item)
		} else {
			add(handled, "unhandled", item)
		}
		if item.cases > 0 {
			add(cases, "has_case", item)
		} else {
			add(cases, "no_case", item)
		}
	}
	return Breakdowns{ByState: buckets(state), ByDecision: buckets(decision), ByHandled: buckets(handled), ByCases: buckets(cases)}
}

func buckets(values map[string]*CountBucket) []CountBucket {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]CountBucket, 0, len(keys))
	for _, key := range keys {
		result = append(result, *values[key])
	}
	return result
}
func problemKey(service, fingerprint string) string { return service + "\x00" + fingerprint }

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func writeAudit(config Config, audit Audit, inputs []Artifact) (Result, error) {
	directory := filepath.Join(config.OutputRoot, config.ProductLine, config.RunID)
	if _, err := os.Stat(directory); err == nil {
		return Result{}, fmt.Errorf("audit output already exists: %s", directory)
	} else if !os.IsNotExist(err) {
		return Result{}, err
	}
	parent := filepath.Dir(directory)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return Result{}, err
	}
	temporary, err := os.MkdirTemp(parent, "."+config.RunID+"-tmp-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(temporary)

	auditData, err := marshalJSON(audit)
	if err != nil {
		return Result{}, err
	}
	jsonlData, err := marshalJSONLines(audit.ProblemDecisions)
	if err != nil {
		return Result{}, err
	}
	markdown := []byte(renderMarkdown(audit))
	outputs := []struct {
		name string
		data []byte
	}{{"audit.json", auditData}, {"audit.md", markdown}, {"problem-decisions.jsonl", jsonlData}}
	var outputArtifacts []Artifact
	for _, output := range outputs {
		if err := os.WriteFile(filepath.Join(temporary, output.name), output.data, 0o600); err != nil {
			return Result{}, err
		}
		sum := sha256.Sum256(output.data)
		outputArtifacts = append(outputArtifacts, Artifact{Path: output.name, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(output.data))})
	}
	run := Run{SchemaVersion: RunSchemaVersion, AuditRunID: config.RunID, ProductLine: config.ProductLine, CoraBuild: audit.CoraBuild, CapturedAt: audit.CapturedAt, Inputs: inputs, Artifacts: outputArtifacts}
	runData, err := marshalJSON(run)
	if err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(filepath.Join(temporary, "run.json"), runData, 0o600); err != nil {
		return Result{}, err
	}
	if err := os.Rename(temporary, directory); err != nil {
		return Result{}, err
	}
	return Result{Directory: directory, Audit: audit, Run: run}, nil
}

func marshalJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
func marshalJSONLines(values []ProblemDecision) ([]byte, error) {
	var b strings.Builder
	w := bufio.NewWriter(&b)
	for _, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if _, err = w.Write(data); err != nil {
			return nil, err
		}
		if err = w.WriteByte('\n'); err != nil {
			return nil, err
		}
	}
	if err := w.Flush(); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}
