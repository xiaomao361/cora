package iteration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/claracore/cora/internal/buildinfo"
	"github.com/claracore/cora/internal/cora"
)

type fakeSource struct {
	health    ServerSnapshot
	problems  []cora.Problem
	attention []cora.AttentionItem
	snapshot  cora.IterationSnapshot
	details   map[string]cora.ProblemDetail
	cases     []cora.ProblemCase
	corrupt   bool
}

func (source *fakeSource) Health(context.Context) (ServerSnapshot, error) { return source.health, nil }

func (source *fakeSource) Problems(context.Context, string) ([]cora.Problem, error) {
	return source.problems, nil
}

func (source *fakeSource) Attention(context.Context, string, int) ([]cora.AttentionItem, error) {
	return source.attention, nil
}

func (source *fakeSource) IterationSnapshot(context.Context, string, string, string, int, int) (cora.IterationSnapshot, error) {
	return source.snapshot, nil
}

func (source *fakeSource) Problem(_ context.Context, productLine, service, fingerprint, _ string) (cora.ProblemDetail, error) {
	item, ok := source.details[productLine+":"+service+":"+fingerprint]
	if !ok {
		return cora.ProblemDetail{}, errors.New("not found")
	}
	return item, nil
}

func (source *fakeSource) ExportCases(_ context.Context, productLine string, after, through int64, limit int) (cora.CaseExportPage, error) {
	if through == 0 {
		through = int64(len(source.cases))
	}
	pageCases := []cora.ProblemCase{}
	for _, item := range source.cases {
		if item.ID > after && item.ID <= through {
			pageCases = append(pageCases, item)
		}
	}
	if len(pageCases) > limit {
		pageCases = pageCases[:limit]
	}
	data, _ := json.Marshal(pageCases)
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	if source.corrupt {
		hash = "0" + hash[1:]
	}
	next := after
	if len(pageCases) > 0 {
		next = pageCases[len(pageCases)-1].ID
	}
	return cora.CaseExportPage{SchemaVersion: "cora-case.v1", SnapshotID: productLine + ":2",
		ProductLine: productLine, SnapshotThroughCaseID: through, AfterCaseID: after,
		NextAfterCaseID: next, HasMore: next < through, PageSHA256: hash, Cases: pageCases}, nil
}

func (*fakeSource) Close() error { return nil }

func TestCommitPatternAcceptsAuditableDirtyBuildIdentity(t *testing.T) {
	for _, value := range []string{"abcdef0", "21802e1ebf77ee12a6dc8d1df8ecf5060a034b91-dirty"} {
		if !commitPattern.MatchString(value) {
			t.Errorf("commitPattern rejected %q", value)
		}
	}
	for _, value := range []string{"dirty", "21802e1-other", "21802e1-dirty-extra"} {
		if commitPattern.MatchString(value) {
			t.Errorf("commitPattern accepted %q", value)
		}
	}
}

func TestRunIterationIsDeterministicAndFlagsIgnoreFrequency(t *testing.T) {
	fixture := newFixture(t)
	firstRoot := t.TempDir()
	secondRoot := t.TempDir()
	config := fixture.config
	config.OutputRoot = firstRoot
	first, err := RunIteration(context.Background(), fixture.source, config)
	if err != nil {
		t.Fatal(err)
	}
	config.OutputRoot = secondRoot
	second, err := RunIteration(context.Background(), fixture.source, config)
	if err != nil {
		t.Fatal(err)
	}
	if first.DailyProblems != 2 || first.DailyOccurrences != 64 || first.FrequencyEscalations != 1 || first.RuleCandidates != 1 {
		t.Fatalf("unexpected result: %+v", first)
	}
	for _, name := range []string{
		"case-snapshot.jsonl", "case-snapshot-manifest.json", "iteration-snapshot.json", "attention-incidents.json",
		"triage-results.jsonl", "rule-candidates.json", "candidate-pack.patch",
		"shadow-eval.json", "shadow-eval.md", "run.json",
	} {
		left, err := os.ReadFile(filepath.Join(first.Directory, name))
		if err != nil {
			t.Fatal(err)
		}
		right, err := os.ReadFile(filepath.Join(second.Directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(left, right) {
			t.Errorf("%s differs across identical frozen runs", name)
		}
	}
	var run Run
	readJSON(t, filepath.Join(first.Directory, "run.json"), &run)
	if run.Status != "completed" || len(run.Artifacts) != 10 {
		t.Fatalf("unexpected run manifest: %+v", run)
	}
	var candidates struct {
		Candidates []RuleCandidate `json:"candidates"`
	}
	readJSON(t, filepath.Join(first.Directory, "rule-candidates.json"), &candidates)
	if len(candidates.Candidates) != 1 || len(candidates.Candidates[0].SourceCaseIDs) != 2 ||
		candidates.Candidates[0].Decision != cora.DecisionIgnore {
		t.Fatalf("unexpected candidates: %+v", candidates.Candidates)
	}
	var shadow ShadowEval
	readJSON(t, filepath.Join(first.Directory, "shadow-eval.json"), &shadow)
	if len(shadow.ErrorIdentities) != 2 || shadow.ErrorIdentities[0].RuleID != "ig_07" ||
		shadow.ErrorIdentities[0].Name != "callback" || shadow.ErrorIdentities[0].Signature != "known callback noise" {
		t.Fatalf("unexpected error identities: %+v", shadow.ErrorIdentities)
	}
	markdown, err := os.ReadFile(filepath.Join(first.Directory, "shadow-eval.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !stringsContain(string(markdown), "## Error identity table") || !stringsContain(string(markdown), "`ig_07`") {
		t.Fatalf("error identity table missing from markdown:\n%s", markdown)
	}
}

func TestRunIterationDoesNotPublishCorruptExport(t *testing.T) {
	fixture := newFixture(t)
	fixture.source.corrupt = true
	root := t.TempDir()
	fixture.config.OutputRoot = root
	_, err := RunIteration(context.Background(), fixture.source, fixture.config)
	if err == nil || !stringsContain(err.Error(), "page hash mismatch") {
		t.Fatalf("error=%v, want page hash mismatch", err)
	}
	final := filepath.Join(root, fixture.config.ProductLine, fixture.config.BusinessDate, fixture.config.RunID)
	if _, statErr := os.Stat(final); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("corrupt run published at %s", final)
	}
}

func TestHTTPSourceRunsAgainstRealCoraServer(t *testing.T) {
	store, err := cora.OpenStore(filepath.Join(t.TempDir(), "cora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	aggregator := cora.NewAggregator(store, 100)
	now := time.Now().UTC()
	event := cora.Event{ProductLine: "payments", Service: "checkout", Environment: "prod",
		Logger: "TradeOrdersServiceImpl", Method: "validatePayPwdRetry",
		ExceptionType: "IllegalArgumentException", Message: "pay password invalid",
		Stacktrace: "at TradeOrdersServiceImpl.validatePayPwdRetry(TradeOrdersServiceImpl.java:1)",
		OccurredAt: now, Labels: map[string]string{"node": "service01"}}
	for index := 0; index < 3; index++ {
		if err := aggregator.Add(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := aggregator.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	const token = "iteration-test-token"
	handler := cora.HandlerWithOptions(store, cora.HandlerOptions{
		BearerToken: token, MCPHandler: cora.NewMCPHandler(store),
		BuildInfo: buildinfo.Info{Version: "v0.1.0-test", Commit: "abcdef0", BuildTime: "test", GoVersion: "go1.26"},
	}, aggregator)
	server := httptest.NewServer(handler)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	source, err := NewHTTPSource(ctx, server.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	result, err := RunIteration(ctx, source, Config{
		ProductLine: "payments", BusinessDate: now.In(location).Format("2006-01-02"), Location: location,
		OutputRoot: t.TempDir(), RunID: "http-e2e", PackManifestPath: filepath.Join("..", "..", "config", "cora-model.example.json"),
		PageSize: 1, AttentionLimit: 200, BaselineDays: 7, FrequencyMinimum: 2,
		FrequencyRatioThreshold: 3, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.DailyProblems != 1 || result.DailyOccurrences != 3 || result.CaseSnapshotID != "payments:0" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

type testFixture struct {
	source *fakeSource
	config Config
}

func newFixture(t *testing.T) testFixture {
	t.Helper()
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	capturedAt := time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC)
	windowStart := time.Date(2026, 7, 13, 16, 0, 0, 0, time.UTC)
	ignoreFingerprint := "11111111111111111111111111111111"
	attentionFingerprint := "22222222222222222222222222222222"
	ignoreEvent := cora.Event{ProductLine: "payments", Service: "checkout", Logger: "OrderNotifyService",
		Method: "handleNotify", ExceptionType: "RemoteCallException", Message: "callback failed",
		Stacktrace: "at OrderNotifyService.handleNotify(OrderNotifyService.java:1)"}
	eventJSON, _ := json.Marshal(ignoreEvent)
	problem := cora.Problem{ID: 1, ProductLine: "payments", Service: "checkout",
		Fingerprint: ignoreFingerprint, Count: 95, FirstSeen: windowStart.AddDate(0, 0, -7),
		LastSeen: windowStart.Add(12 * time.Hour), LatestSample: string(eventJSON), State: cora.ProblemStateResolved}
	decision := cora.CoraDecision{Decision: cora.DecisionObserve, RuleID: "cora.default.unmatched", Category: "unmatched"}
	cases := []cora.ProblemCase{
		{ID: 1, ProblemID: 1, ProductLine: "payments", Service: "checkout", Fingerprint: ignoreFingerprint,
			Actor: "agent-a", Handled: true, RootCause: "known callback noise", Action: "no action",
			ContextSnapshot: cora.CaseContextSnapshot{Problem: problem, Decision: decision}},
		{ID: 2, ProblemID: 1, ProductLine: "payments", Service: "checkout", Fingerprint: ignoreFingerprint,
			Actor: "agent-b", Handled: true, RootCause: "known callback noise", Action: "no action",
			ContextSnapshot: cora.CaseContextSnapshot{Problem: problem, Decision: decision}},
	}
	ignoreTrends := []cora.TrendPoint{{Count: 60, WindowEnd: windowStart.Add(12 * time.Hour)}}
	for day := 1; day <= 7; day++ {
		ignoreTrends = append(ignoreTrends, cora.TrendPoint{Count: 5, WindowEnd: windowStart.AddDate(0, 0, -day).Add(12 * time.Hour)})
	}
	attentionProblem := cora.Problem{ID: 2, ProductLine: "payments", Service: "ledger",
		Fingerprint: attentionFingerprint, FirstSeen: windowStart, LastSeen: windowStart.Add(time.Hour), State: cora.ProblemStateNew}
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	packPath := filepath.Join(t.TempDir(), "pack.json")
	packData := []byte(`{"schema_version":"cora.experience-pack.v1"}`)
	if err := os.WriteFile(packPath, packData, 0o600); err != nil {
		t.Fatal(err)
	}
	packSum := sha256.Sum256(packData)
	manifest := map[string]any{"product_lines": []map[string]any{{
		"product_line": "payments", "experience_pack": packPath,
		"experience_version": "pack-v1", "experience_sha256": hex.EncodeToString(packSum[:]),
	}}}
	manifestData, _ := json.Marshal(manifest)
	if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	evidencePath := filepath.Join(t.TempDir(), "evidence.jsonl")
	evidence := CodeEvidence{SchemaVersion: "cora.code-evidence.v1", EvidenceID: "atlas-checkout-1",
		ProductLine: "payments", Service: "checkout", Fingerprint: ignoreFingerprint,
		Source: "atlas", Status: "verified", Summary: "callback ownership verified",
		References: []string{"atlas:service:checkout"}, CollectedAt: capturedAt}
	evidenceData, _ := json.Marshal(evidence)
	if err := os.WriteFile(evidencePath, append(evidenceData, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	source := &fakeSource{
		health: ServerSnapshot{Build: buildinfo.Info{Version: "v0.1.0", Commit: "abcdef0", BuildTime: "now", GoVersion: "go1.26"},
			Storage: cora.StoreHealth{SchemaVersion: 5}},
		problems:  []cora.Problem{problem, attentionProblem},
		attention: []cora.AttentionItem{{ProblemID: 2, ProductLine: "payments", Service: "ledger", Fingerprint: attentionFingerprint}},
		snapshot: cora.IterationSnapshot{SchemaVersion: cora.IterationSnapshotSchemaVersion,
			ProductLine: "payments", BusinessDate: "2026-07-14", Timezone: "Asia/Shanghai",
			WindowStart: windowStart, WindowEnd: windowStart.AddDate(0, 0, 1), BaselineDays: 7,
			Summary: cora.IterationSnapshotSummary{MatchedProblemCount: 2, ReturnedProblemCount: 2,
				OccurrenceCount: 64, DecisionProblemCounts: map[string]int{cora.DecisionIgnore: 1, cora.DecisionAttention: 1},
				DecisionOccurrenceCounts: map[string]int64{cora.DecisionIgnore: 60, cora.DecisionAttention: 4}},
			Problems: []cora.IterationProblem{
				{ProblemID: 1, ProductLine: "payments", Service: "checkout", Fingerprint: ignoreFingerprint,
					State: cora.ProblemStateResolved, Decision: cora.DecisionIgnore, Category: "callback", RuleID: "ig_07",
					WindowCount: 60, PriorDailyAverage: 5, FrequencyRatio: floatPointer(12),
					FirstSeen: problem.FirstSeen, LastSeen: problem.LastSeen, CaseIDs: []int64{1, 2}},
				{ProblemID: 2, ProductLine: "payments", Service: "ledger", Fingerprint: attentionFingerprint,
					State: cora.ProblemStateNew, Decision: cora.DecisionAttention, Category: "database", RuleID: "at_01",
					WindowCount: 4, FirstSeen: attentionProblem.FirstSeen, LastSeen: attentionProblem.LastSeen},
			}},
		cases: cases,
		details: map[string]cora.ProblemDetail{
			"payments:checkout:" + ignoreFingerprint: {Problem: problem, Decision: cora.CoraDecision{
				Decision: cora.DecisionIgnore, RuleID: "ig_07", Category: "callback"}, TrendPoints: ignoreTrends, Cases: cases},
			"payments:ledger:" + attentionFingerprint: {Problem: attentionProblem, Decision: cora.CoraDecision{
				Decision: cora.DecisionAttention, RuleID: "at_01", Category: "database"},
				TrendPoints: []cora.TrendPoint{{Count: 4, WindowEnd: windowStart.Add(time.Hour)}}},
		},
	}
	return testFixture{source: source, config: Config{
		ProductLine: "payments", BusinessDate: "2026-07-14", Location: location,
		RunID: "run-fixed", PackManifestPath: manifestPath, PageSize: 1,
		CodeEvidencePath: evidencePath,
		AttentionLimit:   200, BaselineDays: 7, FrequencyMinimum: 20, FrequencyRatioThreshold: 3,
		Now: func() time.Time { return capturedAt },
	}}
}

func floatPointer(value float64) *float64 { return &value }

func readJSON(t *testing.T, path string, target any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

func stringsContain(value, fragment string) bool {
	for index := 0; index+len(fragment) <= len(value); index++ {
		if value[index:index+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
