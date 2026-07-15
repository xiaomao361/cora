package retention

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestRunAuditIsReadOnlyProductScopedDeterministicAndConservative(t *testing.T) {
	root := t.TempDir()
	if externalRoot := os.Getenv("CORA_RETENTION_TEST_ROOT"); externalRoot != "" {
		root = externalRoot
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	database := filepath.Join(root, "fixture.db")
	createFixtureDatabase(t, database)
	walConnection := openCheckpointedWALFixture(t, database)
	defer walConnection.Close()
	iterations := filepath.Join(root, "iterations")
	closures := filepath.Join(root, "closures")
	artifactHashes := writeFixtureArtifacts(t, iterations)
	writeFixtureReceipts(t, closures, artifactHashes)

	before, err := snapshotDatabase(database)
	if err != nil {
		t.Fatal(err)
	}
	beforeFiles := directoryFiles(t, root)
	beforeSHM, err := snapshotDatabase(database + "-shm")
	if err != nil {
		t.Fatal(err)
	}
	beforeWAL, err := snapshotDatabase(database + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	first, err := RunAudit(context.Background(), Config{DatabasePath: database, ProductLine: "line-a", CoraBuildVersion: "test-build", CoraSourceDigest: strings.Repeat("a", 64), IterationRoot: iterations, ClosureRoot: closures, OutputRoot: filepath.Join(root, "audit-one"), RunID: "stable-run"})
	if err != nil {
		t.Fatal(err)
	}
	after, err := snapshotDatabase(database)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("database changed: before=%+v after=%+v", before, after)
	}
	assertFileSnapshot(t, database+"-shm", beforeSHM)
	assertFileSnapshot(t, database+"-wal", beforeWAL)
	afterFiles := directoryFiles(t, root)
	for _, suffix := range []string{"fixture.db-wal", "fixture.db-shm", "fixture.db-journal"} {
		if containsString(afterFiles, suffix) && !containsString(beforeFiles, suffix) {
			t.Fatalf("audit created SQLite sidecar %s", suffix)
		}
	}

	if first.Audit.Summary.ProblemCount != 5 || first.Audit.Summary.EligibleProblems != 1 || first.Audit.Summary.IneligibleProblems != 4 {
		t.Fatalf("unexpected summary: %+v", first.Audit.Summary)
	}
	if first.Audit.Summary.InvalidReceiptFiles != 1 {
		t.Fatalf("invalid receipts=%d, want 1", first.Audit.Summary.InvalidReceiptFiles)
	}
	decisions := map[int64]ProblemDecision{}
	for _, decision := range first.Audit.ProblemDecisions {
		decisions[decision.ProblemID] = decision
	}
	assertReasons(t, decisions[1], "problem_active")
	assertReasons(t, decisions[2], "handled_case_missing", "receipt_cases_not_handled")
	assertReasons(t, decisions[3], "closure_receipt_missing")
	assertReasons(t, decisions[4], "rule_pack_hash_missing")
	if !decisions[5].RetentionEligible || len(decisions[5].BlockingReasons) != 0 || decisions[5].EstimatedReleasableBytes == 0 || decisions[5].EstimatedReleasableRows != 2 {
		t.Fatalf("eligible decision=%+v", decisions[5])
	}

	for _, name := range []string{"audit.json", "audit.md", "problem-decisions.jsonl", "run.json"} {
		if _, err := os.Stat(filepath.Join(first.Directory, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	auditJSON, err := os.ReadFile(filepath.Join(first.Directory, "audit.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(auditJSON), "line-b") || strings.Contains(string(auditJSON), "ffffffffffffffffffffffffffffffff") {
		t.Fatal("cross-product-line data leaked into audit")
	}
	var decoded Audit
	if err := json.Unmarshal(auditJSON, &decoded); err != nil {
		t.Fatalf("audit JSON invalid: %v", err)
	}
	if decoded.SchemaVersion != AuditSchemaVersion {
		t.Fatalf("schema version=%s", decoded.SchemaVersion)
	}
	verifyRunHashes(t, first.Directory, root)

	second, err := RunAudit(context.Background(), Config{DatabasePath: database, ProductLine: "line-a", CoraBuildVersion: "test-build", CoraSourceDigest: strings.Repeat("a", 64), IterationRoot: iterations, ClosureRoot: closures, OutputRoot: filepath.Join(root, "audit-two"), RunID: "stable-run"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"audit.json", "audit.md", "problem-decisions.jsonl", "run.json"} {
		one, _ := os.ReadFile(filepath.Join(first.Directory, name))
		two, _ := os.ReadFile(filepath.Join(second.Directory, name))
		if !reflect.DeepEqual(one, two) {
			t.Errorf("%s is not byte-stable", name)
		}
	}
	assertFileSnapshot(t, database+"-shm", beforeSHM)
	assertFileSnapshot(t, database+"-wal", beforeWAL)
}

func openCheckpointedWALFixture(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal mode=%s, want wal", mode)
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-shm", "-wal"} {
		if _, err := os.Stat(path + suffix); err != nil {
			t.Fatalf("missing WAL sidecar %s: %v", suffix, err)
		}
	}
	return db
}

func assertFileSnapshot(t *testing.T, path string, before databaseSnapshot) {
	t.Helper()
	after, err := snapshotDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("%s changed: before=%+v after=%+v", path, before, after)
	}
}

func createFixtureDatabase(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE problems (id INTEGER PRIMARY KEY, product_line TEXT, fingerprint TEXT, service TEXT, environment TEXT, exception_type TEXT, logger TEXT, count INTEGER, first_seen TEXT, last_seen TEXT, first_sample TEXT, latest_sample TEXT, state TEXT, state_changed_at TEXT)`,
		`CREATE TABLE trend_points (id INTEGER PRIMARY KEY, product_line TEXT, service TEXT, fingerprint TEXT, count INTEGER, window_start TEXT, window_end TEXT)`,
		`CREATE TABLE cora_decisions (id INTEGER PRIMARY KEY, product_line TEXT, service TEXT, fingerprint TEXT, decision TEXT, category TEXT, rule_id TEXT, reason TEXT, source TEXT, experience_version TEXT, decided_at TEXT)`,
		`CREATE TABLE node_occurrences (id INTEGER PRIMARY KEY, product_line TEXT, service TEXT, fingerprint TEXT, node TEXT, deployment_group TEXT, environment TEXT, count INTEGER, first_seen TEXT, last_seen TEXT)`,
		`CREATE TABLE node_trend_points (id INTEGER PRIMARY KEY, product_line TEXT, service TEXT, fingerprint TEXT, node TEXT, deployment_group TEXT, count INTEGER, window_start TEXT, window_end TEXT)`,
		`CREATE TABLE problem_cases (id INTEGER PRIMARY KEY, problem_id INTEGER, product_line TEXT, service TEXT, fingerprint TEXT, actor TEXT, is_real_problem INTEGER, handled INTEGER, root_cause TEXT, action TEXT, prior_state TEXT, resulting_state TEXT, context_snapshot TEXT, recorded_at TEXT)`,
		`PRAGMA user_version = 5`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for id := 1; id <= 5; id++ {
		state := "resolved"
		handled := 1
		if id == 1 {
			state = "new"
		}
		if id == 2 {
			handled = 0
		}
		fingerprint := fixtureFingerprint(id)
		if _, err := db.Exec(`INSERT INTO problems VALUES(?, 'line-a', ?, 'svc', 'prod', 'Error', 'logger', ?, '2026-01-01T00:00:00Z', '2026-01-02T00:00:00Z', 'first sample', 'latest sample', ?, '2026-01-02T00:00:00Z')`, id, fingerprint, id*10, state); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO cora_decisions VALUES(?, 'line-a', 'svc', ?, 'attention', 'test', 'rule', 'reason', 'pack', 'v1', '2026-01-01T00:00:00Z')`, id, fingerprint); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO problem_cases VALUES(?, ?, 'line-a', 'svc', ?, 'tester', 1, ?, 'root', 'action', 'new', 'resolved', '{}', '2026-01-02T00:00:00Z')`, id, id, fingerprint, handled); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO trend_points VALUES(?, 'line-a', 'svc', ?, 1, '2026-01-01T00:00:00Z', '2026-01-01T00:01:00Z')`, id, fingerprint); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO node_occurrences VALUES(?, 'line-a', 'svc', ?, 'node', 'group', 'prod', 1, '2026-01-01T00:00:00Z', '2026-01-02T00:00:00Z')`, id, fingerprint); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO node_trend_points VALUES(?, 'line-a', 'svc', ?, 'node', 'group', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:01:00Z')`, id, fingerprint); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO problems VALUES(99, 'line-b', 'ffffffffffffffffffffffffffffffff', 'secret-service', 'prod', 'Error', 'logger', 999, '2026-01-01T00:00:00Z', '2026-01-02T00:00:00Z', 'secret', 'secret', 'resolved', '2026-01-02T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeFixtureArtifacts(t *testing.T, root string) map[string]string {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	result := map[string]string{}
	for _, name := range []string{"case-snapshot.jsonl", "pack.json", "evaluation.json", "observation.json"} {
		data := []byte(name + "\n")
		if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		result[name] = hex.EncodeToString(sum[:])
	}
	manifest := map[string]any{"schema_version": "cora.case-snapshot-manifest.v1", "snapshot_id": "snapshot-fixed", "product_line": "line-a", "through_case_id": 5, "case_count": 5, "case_snapshot_sha256": result["case-snapshot.jsonl"], "pages": []any{map[string]any{"page_sha256": strings.Repeat("a", 64)}}, "verified_at": "2026-01-02T01:00:00Z"}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(root, "case-manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	result["case-manifest.json"] = hex.EncodeToString(sum[:])
	return result
}

func writeFixtureReceipts(t *testing.T, root string, hashes map[string]string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{1, 2, 4, 5} {
		packHash := hashes["pack.json"]
		if id == 4 {
			packHash = strings.Repeat("0", 64)
		}
		receipt := map[string]any{
			"schema_version": "cora.closure-receipt.v1", "closure_receipt_id": fmtID("receipt", id), "iteration_run_id": "iteration-fixed", "product_line": "line-a", "service": "svc", "fingerprint": fixtureFingerprint(id),
			"case_snapshot": map[string]any{"snapshot_id": "snapshot-fixed", "through_case_id": 5, "case_ids": []int{id}, "manifest_sha256": hashes["case-manifest.json"], "verified_at": "2026-01-02T01:00:00Z"},
			"rule":          map[string]any{"candidate_id": "candidate-fixed", "rule_id": "rule-fixed", "pack_version": "pack-v1", "pack_sha256": packHash, "approved_at": "2026-01-02T02:00:00Z"},
			"evaluation":    map[string]any{"eval_run_id": "eval-fixed", "status": "passed", "artifact_sha256": hashes["evaluation.json"]},
			"deployment":    map[string]any{"status": "deployed", "build_version": "v1", "build_commit": "abcdef1", "deployed_at": "2026-01-02T03:00:00Z"},
			"observation":   map[string]any{"status": "passed", "ends_at": "2026-01-03T00:00:00Z", "validated_at": "2026-01-03T01:00:00Z", "evidence_sha256": hashes["observation.json"]},
			"status":        "validated", "retention_eligible": true, "created_at": "2026-01-03T02:00:00Z",
		}
		data, _ := json.MarshalIndent(receipt, "", "  ")
		data = append(data, '\n')
		if err := os.WriteFile(filepath.Join(root, fmtID("receipt", id)+".json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func verifyRunHashes(t *testing.T, directory, inputRoot string) {
	t.Helper()
	var run Run
	data, err := os.ReadFile(filepath.Join(directory, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &run); err != nil {
		t.Fatal(err)
	}
	for _, artifact := range run.Artifacts {
		digest, size, err := hashFile(filepath.Join(directory, artifact.Path))
		if err != nil {
			t.Fatal(err)
		}
		if digest != artifact.SHA256 || size != artifact.Bytes {
			t.Errorf("artifact mismatch: %+v", artifact)
		}
	}
	for _, artifact := range run.Inputs {
		digest, size, err := hashFile(filepath.Join(inputRoot, filepath.FromSlash(artifact.Path)))
		if err != nil {
			t.Fatal(err)
		}
		if digest != artifact.SHA256 || size != artifact.Bytes {
			t.Errorf("input mismatch: %+v", artifact)
		}
	}
}

func assertReasons(t *testing.T, decision ProblemDecision, expected ...string) {
	t.Helper()
	for _, reason := range expected {
		if !containsString(decision.BlockingReasons, reason) {
			t.Errorf("problem %d reasons=%v missing %s", decision.ProblemID, decision.BlockingReasons, reason)
		}
	}
}
func directoryFiles(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	result := make([]string, 0, len(entries))
	for _, e := range entries {
		result = append(result, e.Name())
	}
	sort.Strings(result)
	return result
}
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func fixtureFingerprint(id int) string   { return strings.Repeat(fmt.Sprintf("%x", id), 32) }
func fmtID(prefix string, id int) string { return prefix + "-" + fmt.Sprintf("%d", id) }
