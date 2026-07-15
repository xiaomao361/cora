package iteration

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/claracore/cora/internal/cora"
)

var safeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var commitPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

type Result struct {
	Directory            string `json:"directory"`
	RunID                string `json:"iteration_run_id"`
	BusinessDate         string `json:"business_date"`
	ProductLine          string `json:"product_line"`
	DailyProblems        int    `json:"daily_problems"`
	DailyOccurrences     int64  `json:"daily_occurrences"`
	FrequencyEscalations int    `json:"frequency_escalations"`
	RuleCandidates       int    `json:"rule_candidates"`
	CaseSnapshotID       string `json:"case_snapshot_id"`
}

func RunIteration(ctx context.Context, source Source, config Config) (result Result, resultErr error) {
	config, window, capturedAt, err := normalizeConfig(config)
	if err != nil {
		return result, err
	}
	finalDirectory := filepath.Join(config.OutputRoot, config.ProductLine, config.BusinessDate, config.RunID)
	if _, err := os.Stat(finalDirectory); err == nil {
		return result, fmt.Errorf("iteration output already exists: %s", finalDirectory)
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	parent := filepath.Dir(finalDirectory)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return result, err
	}
	temporaryDirectory, err := os.MkdirTemp(parent, "."+config.RunID+".tmp-")
	if err != nil {
		return result, err
	}
	defer func() {
		if resultErr != nil {
			_ = os.RemoveAll(temporaryDirectory)
		}
	}()

	health, err := source.Health(ctx)
	if err != nil {
		return result, fmt.Errorf("read Cora health: %w", err)
	}
	if health.Build.Version == "" || !commitPattern.MatchString(health.Build.Commit) || health.Storage.SchemaVersion < 1 {
		return result, errors.New("Cora health is missing build or schema identity")
	}
	pack, err := loadPackIdentity(config.PackManifestPath, config.ProductLine)
	if err != nil {
		return result, err
	}

	cases, caseSnapshot, caseArtifacts, err := freezeCases(ctx, source, config, capturedAt, temporaryDirectory)
	if err != nil {
		return result, err
	}
	currentAttention, err := source.Attention(ctx, config.ProductLine, config.AttentionLimit)
	if err != nil {
		return result, fmt.Errorf("read attention incidents: %w", err)
	}
	iterationSnapshot, err := source.IterationSnapshot(ctx, config.ProductLine, config.BusinessDate,
		config.Location.String(), config.BaselineDays, 1000)
	if err != nil {
		return result, fmt.Errorf("read iteration snapshot: %w", err)
	}
	iterationSnapshotArtifact, err := writeJSONArtifact(temporaryDirectory, "iteration-snapshot.json", "iteration_snapshot", capturedAt, iterationSnapshot)
	if err != nil {
		return result, err
	}
	dailyProblems, escalations, summary, err := summarizeSnapshot(ctx, source, config, iterationSnapshot, len(currentAttention))
	if err != nil {
		return result, err
	}

	attentionSnapshot := AttentionSnapshot{
		SchemaVersion: "cora.attention-snapshot.v1", ProductLine: config.ProductLine,
		BusinessDate: config.BusinessDate, Window: window, CapturedAt: capturedAt,
		CurrentIncidents: currentAttention, DailyProblems: dailyProblems,
		FrequencyEscalation: escalations, Summary: summary,
	}
	attentionArtifact, err := writeJSONArtifact(temporaryDirectory, "attention-incidents.json", "attention_incidents", capturedAt, attentionSnapshot)
	if err != nil {
		return result, err
	}

	codeEvidence, evidenceByProblem, err := loadCodeEvidence(config.CodeEvidencePath, config.ProductLine)
	if err != nil {
		return result, err
	}
	triage := buildTriage(config.RunID, dailyProblems, escalations, evidenceByProblem)
	triageArtifact, err := writeJSONLinesArtifact(temporaryDirectory, "triage-results.jsonl", "triage_results", capturedAt, triage)
	if err != nil {
		return result, err
	}
	candidates := crystallizeCandidates(config.RunID, config.ProductLine, caseSnapshot.SnapshotID, cases, evidenceByProblem, capturedAt)
	candidateArtifact, err := writeJSONArtifact(temporaryDirectory, "rule-candidates.json", "rule_candidates", capturedAt, map[string]any{
		"schema_version": "cora.rule-candidates.v1", "iteration_run_id": config.RunID,
		"product_line": config.ProductLine, "candidates": candidates,
	})
	if err != nil {
		return result, err
	}

	shadow := evaluateCandidates(config.RunID, config.ProductLine, caseSnapshot.SnapshotID, candidates, dailyProblems, cases, escalations)
	shadowArtifact, err := writeJSONArtifact(temporaryDirectory, "shadow-eval.json", "shadow_eval_json", capturedAt, shadow)
	if err != nil {
		return result, err
	}
	markdownArtifact, err := writeArtifact(temporaryDirectory, "shadow-eval.md", "shadow_eval_markdown", capturedAt, []byte(renderShadowMarkdown(shadow)))
	if err != nil {
		return result, err
	}

	artifacts := append([]Artifact{}, caseArtifacts...)
	artifacts = append(artifacts, iterationSnapshotArtifact)
	artifacts = append(artifacts, attentionArtifact, triageArtifact, candidateArtifact, shadowArtifact, markdownArtifact)
	if len(codeEvidence) > 0 {
		evidenceArtifact, err := writeJSONLinesArtifact(temporaryDirectory, "code-evidence.jsonl", "code_evidence", capturedAt, codeEvidence)
		if err != nil {
			return result, err
		}
		artifacts = append(artifacts, evidenceArtifact)
	}
	if len(candidates) > 0 {
		patchArtifact, err := writeJSONArtifact(temporaryDirectory, "candidate-pack.patch", "candidate_pack_patch", capturedAt, buildPackPatch(candidates))
		if err != nil {
			return result, err
		}
		artifacts = append(artifacts, patchArtifact)
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Path < artifacts[j].Path })

	run := Run{
		SchemaVersion: RunSchemaVersion, IterationRunID: config.RunID,
		ProductLine: config.ProductLine, BusinessDate: config.BusinessDate,
		InputWindow: window, CreatedAt: capturedAt, CompletedAt: capturedAt, Status: "completed",
		CoraBuild: RunBuild{Version: health.Build.Version, Commit: health.Build.Commit,
			BuildTime: health.Build.BuildTime, GoVersion: health.Build.GoVersion,
			SchemaVersion: health.Storage.SchemaVersion},
		Pack: pack, CaseSnapshot: caseSnapshot, AttentionSnapshot: attentionArtifact, Artifacts: artifacts,
	}
	if _, err := writeJSONArtifact(temporaryDirectory, "run.json", "", capturedAt, run); err != nil {
		return result, err
	}
	if err := os.Rename(temporaryDirectory, finalDirectory); err != nil {
		return result, fmt.Errorf("publish iteration output: %w", err)
	}
	return Result{
		Directory: finalDirectory, RunID: config.RunID, BusinessDate: config.BusinessDate,
		ProductLine: config.ProductLine, DailyProblems: summary.ProblemCount,
		DailyOccurrences: summary.OccurrenceCount, FrequencyEscalations: len(escalations),
		RuleCandidates: len(candidates), CaseSnapshotID: caseSnapshot.SnapshotID,
	}, nil
}

func normalizeConfig(config Config) (Config, InputWindow, time.Time, error) {
	config.ProductLine = strings.TrimSpace(config.ProductLine)
	config.BusinessDate = strings.TrimSpace(config.BusinessDate)
	config.RunID = strings.TrimSpace(config.RunID)
	if !safeIDPattern.MatchString(config.ProductLine) {
		return config, InputWindow{}, time.Time{}, errors.New("product line must be a safe non-empty identifier")
	}
	if config.Location == nil {
		return config, InputWindow{}, time.Time{}, errors.New("timezone location is required")
	}
	date, err := time.ParseInLocation("2006-01-02", config.BusinessDate, config.Location)
	if err != nil {
		return config, InputWindow{}, time.Time{}, fmt.Errorf("business date must be YYYY-MM-DD: %w", err)
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	capturedAt := config.Now().UTC()
	if config.RunID == "" {
		config.RunID = fmt.Sprintf("%s-%s", config.BusinessDate, capturedAt.Format("20060102T150405Z"))
	}
	if !safeIDPattern.MatchString(config.RunID) {
		return config, InputWindow{}, time.Time{}, errors.New("run ID must contain only letters, numbers, dot, underscore, or dash")
	}
	if config.OutputRoot == "" || config.PackManifestPath == "" {
		return config, InputWindow{}, time.Time{}, errors.New("output root and Pack manifest path are required")
	}
	if config.PageSize <= 0 || config.PageSize > 200 {
		config.PageSize = 100
	}
	if config.AttentionLimit <= 0 || config.AttentionLimit > 200 {
		config.AttentionLimit = 200
	}
	if config.BaselineDays <= 0 {
		config.BaselineDays = 7
	}
	if config.FrequencyMinimum <= 0 {
		config.FrequencyMinimum = 20
	}
	if config.FrequencyRatioThreshold <= 1 {
		config.FrequencyRatioThreshold = 3
	}
	return config, InputWindow{
		Start: date.UTC(), End: date.AddDate(0, 0, 1).UTC(), Timezone: config.Location.String(),
	}, capturedAt, nil
}

func freezeCases(ctx context.Context, source Source, config Config, capturedAt time.Time, directory string) ([]cora.ProblemCase, CaseSnapshot, []Artifact, error) {
	var cases []cora.ProblemCase
	var pages []CaseSnapshotPage
	var snapshotID string
	var through int64
	after := int64(0)
	for {
		page, err := source.ExportCases(ctx, config.ProductLine, after, through, config.PageSize)
		if err != nil {
			return nil, CaseSnapshot{}, nil, fmt.Errorf("export cases after %d: %w", after, err)
		}
		if page.ProductLine != config.ProductLine || page.AfterCaseID != after {
			return nil, CaseSnapshot{}, nil, errors.New("case export changed product line or cursor")
		}
		if through == 0 {
			through, snapshotID = page.SnapshotThroughCaseID, page.SnapshotID
		} else if page.SnapshotThroughCaseID != through || page.SnapshotID != snapshotID {
			return nil, CaseSnapshot{}, nil, errors.New("case export snapshot changed between pages")
		}
		canonical, err := json.Marshal(page.Cases)
		if err != nil {
			return nil, CaseSnapshot{}, nil, err
		}
		if sha256Hex(canonical) != page.PageSHA256 {
			return nil, CaseSnapshot{}, nil, fmt.Errorf("case export page hash mismatch after %d", after)
		}
		pages = append(pages, CaseSnapshotPage{AfterCaseID: after, NextAfterCaseID: page.NextAfterCaseID,
			CaseCount: len(page.Cases), PageSHA256: page.PageSHA256})
		cases = append(cases, page.Cases...)
		if !page.HasMore {
			break
		}
		if page.NextAfterCaseID <= after {
			return nil, CaseSnapshot{}, nil, errors.New("case export cursor did not advance")
		}
		after = page.NextAfterCaseID
	}
	caseArtifact, err := writeJSONLinesArtifact(directory, "case-snapshot.jsonl", "case_snapshot", capturedAt, cases)
	if err != nil {
		return nil, CaseSnapshot{}, nil, err
	}
	manifest := map[string]any{
		"schema_version": "cora.case-snapshot-manifest.v1", "snapshot_id": snapshotID,
		"product_line": config.ProductLine, "through_case_id": through, "case_count": len(cases),
		"case_snapshot_sha256": caseArtifact.SHA256, "pages": pages, "verified_at": capturedAt,
	}
	manifestArtifact, err := writeJSONArtifact(directory, "case-snapshot-manifest.json", "case_snapshot_manifest", capturedAt, manifest)
	if err != nil {
		return nil, CaseSnapshot{}, nil, err
	}
	return cases, CaseSnapshot{SnapshotID: snapshotID, ThroughID: through, Pages: pages,
		ManifestHash: manifestArtifact.SHA256, VerifiedAt: capturedAt}, []Artifact{caseArtifact, manifestArtifact}, nil
}

func summarizeSnapshot(ctx context.Context, source Source, config Config, snapshot cora.IterationSnapshot, incidentCount int) ([]DailyProblem, []FrequencyEscalation, DailySummary, error) {
	if snapshot.SchemaVersion != cora.IterationSnapshotSchemaVersion || snapshot.ProductLine != config.ProductLine ||
		snapshot.BusinessDate != config.BusinessDate || snapshot.Timezone != config.Location.String() {
		return nil, nil, DailySummary{}, errors.New("iteration snapshot identity does not match the requested boundary")
	}
	if snapshot.Summary.Truncated {
		return nil, nil, DailySummary{}, errors.New("iteration snapshot is truncated; increase the MCP limit before publishing")
	}
	var daily []DailyProblem
	for _, problem := range snapshot.Problems {
		if problem.ProductLine != config.ProductLine || problem.WindowCount <= 0 {
			return nil, nil, DailySummary{}, fmt.Errorf("problem %d crossed the snapshot boundary", problem.ProblemID)
		}
		detail, err := source.Problem(ctx, config.ProductLine, problem.Service, problem.Fingerprint)
		if err != nil {
			return nil, nil, DailySummary{}, fmt.Errorf("get problem %s/%s: %w", problem.Service, problem.Fingerprint, err)
		}
		item := DailyProblem{ProblemID: problem.ProblemID, ProductLine: problem.ProductLine,
			Service: problem.Service, Fingerprint: problem.Fingerprint, State: problem.State,
			Decision: problem.Decision, Category: problem.Category,
			RuleID: problem.RuleID, WindowCount: problem.WindowCount,
			PriorDailyAverage: round(problem.PriorDailyAverage), FrequencyRatio: roundPointer(problem.FrequencyRatio),
			FirstSeen: problem.FirstSeen, LastSeen: problem.LastSeen, Detail: &detail}
		for _, node := range problem.NodeCounts {
			item.NodeCounts = append(item.NodeCounts, NodeCount{Node: node.Node,
				DeploymentGroup: node.DeploymentGroup, Count: node.Count})
		}
		for _, related := range detail.RelatedProblems {
			item.RelatedProblemKeys = append(item.RelatedProblemKeys, related.Service+":"+related.Fingerprint)
		}
		item.CaseIDs = append(item.CaseIDs, problem.CaseIDs...)
		sort.Strings(item.RelatedProblemKeys)
		sort.Slice(item.NodeCounts, func(i, j int) bool { return item.NodeCounts[i].Node < item.NodeCounts[j].Node })
		daily = append(daily, item)
	}
	sort.Slice(daily, func(i, j int) bool {
		if daily[i].WindowCount != daily[j].WindowCount {
			return daily[i].WindowCount > daily[j].WindowCount
		}
		return daily[i].Service+daily[i].Fingerprint < daily[j].Service+daily[j].Fingerprint
	})
	escalations := frequencyEscalations(config, daily)
	summary := DailySummary{DecisionProblemCounts: snapshot.Summary.DecisionProblemCounts,
		DecisionOccurrenceCounts: snapshot.Summary.DecisionOccurrenceCounts,
		ProblemCount:             snapshot.Summary.MatchedProblemCount, OccurrenceCount: snapshot.Summary.OccurrenceCount,
		FrequencyEscalationCount: len(escalations), CurrentAttentionIncidents: incidentCount}
	return daily, escalations, summary, nil
}

func frequencyEscalations(config Config, problems []DailyProblem) []FrequencyEscalation {
	var result []FrequencyEscalation
	for _, problem := range problems {
		if problem.Decision != cora.DecisionIgnore || problem.WindowCount < config.FrequencyMinimum {
			continue
		}
		reason := ""
		if problem.PriorDailyAverage == 0 {
			reason = "ignore rule has no prior-window baseline but exceeded the minimum daily count"
		} else if problem.FrequencyRatio != nil && *problem.FrequencyRatio >= config.FrequencyRatioThreshold {
			reason = fmt.Sprintf("ignore frequency is %.2fx the prior %d-day daily average", *problem.FrequencyRatio, config.BaselineDays)
		}
		if reason == "" {
			continue
		}
		result = append(result, FrequencyEscalation{Service: problem.Service, Fingerprint: problem.Fingerprint,
			RuleID: problem.RuleID, WindowCount: problem.WindowCount,
			PriorDailyAverage: problem.PriorDailyAverage, FrequencyRatio: problem.FrequencyRatio,
			BaselineDays: config.BaselineDays, Reason: reason,
			RecommendedReview: "verify business meaning and code/release evidence before proposing observe or attention"})
	}
	return result
}

func loadPackIdentity(path, productLine string) (PackIdentity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return PackIdentity{}, fmt.Errorf("read Pack manifest: %w", err)
	}
	var manifest struct {
		ProductLines []struct {
			ProductLine string `json:"product_line"`
			PackPath    string `json:"experience_pack"`
			Version     string `json:"experience_version"`
			SHA256      string `json:"experience_sha256"`
		} `json:"product_lines"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return PackIdentity{}, fmt.Errorf("parse Pack manifest: %w", err)
	}
	for _, item := range manifest.ProductLines {
		if item.ProductLine == productLine {
			if len(item.SHA256) != 64 || item.Version == "" || item.PackPath == "" {
				return PackIdentity{}, fmt.Errorf("Pack identity for %s is incomplete", productLine)
			}
			packPath := item.PackPath
			packData, err := os.ReadFile(packPath)
			if err != nil && !filepath.IsAbs(packPath) {
				packPath = filepath.Join(filepath.Dir(path), "..", item.PackPath)
				packData, err = os.ReadFile(packPath)
			}
			if err != nil {
				return PackIdentity{}, fmt.Errorf("read Pack %s: %w", item.PackPath, err)
			}
			if actual := sha256Hex(packData); actual != item.SHA256 {
				return PackIdentity{}, fmt.Errorf("Pack %s hash=%s, manifest=%s", item.PackPath, actual, item.SHA256)
			}
			return PackIdentity{Version: item.Version, SHA256: item.SHA256}, nil
		}
	}
	return PackIdentity{}, fmt.Errorf("Pack manifest has no product line %q", productLine)
}

func loadCodeEvidence(path, productLine string) ([]CodeEvidence, map[string][]CodeEvidence, error) {
	if strings.TrimSpace(path) == "" {
		return nil, map[string][]CodeEvidence{}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open code evidence: %w", err)
	}
	defer file.Close()
	var items []CodeEvidence
	byProblem := map[string][]CodeEvidence{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var item CodeEvidence
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			return nil, nil, fmt.Errorf("decode code evidence line %d: %w", len(items)+1, err)
		}
		if item.SchemaVersion != "cora.code-evidence.v1" || item.ProductLine != productLine ||
			item.EvidenceID == "" || item.Service == "" || item.Fingerprint == "" ||
			item.Source != "atlas" || (item.Status != "verified" && item.Status != "not_found") {
			return nil, nil, fmt.Errorf("invalid code evidence %q or product-line boundary", item.EvidenceID)
		}
		items = append(items, item)
		key := item.Service + ":" + item.Fingerprint
		byProblem[key] = append(byProblem[key], item)
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].EvidenceID < items[j].EvidenceID })
	return items, byProblem, nil
}

func writeJSONArtifact(directory, name, role string, capturedAt time.Time, value any) (Artifact, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return Artifact{}, err
	}
	data = append(data, '\n')
	return writeArtifact(directory, name, role, capturedAt, data)
}

func writeJSONLinesArtifact[T any](directory, name, role string, capturedAt time.Time, values []T) (Artifact, error) {
	var builder strings.Builder
	writer := bufio.NewWriter(&builder)
	for _, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			return Artifact{}, err
		}
		if _, err := writer.Write(data); err != nil {
			return Artifact{}, err
		}
		if err := writer.WriteByte('\n'); err != nil {
			return Artifact{}, err
		}
	}
	if err := writer.Flush(); err != nil {
		return Artifact{}, err
	}
	return writeArtifact(directory, name, role, capturedAt, []byte(builder.String()))
}

func writeArtifact(directory, name, role string, capturedAt time.Time, data []byte) (Artifact, error) {
	if filepath.Base(name) != name {
		return Artifact{}, errors.New("artifact name must be a base name")
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return Artifact{}, err
	}
	return Artifact{Path: name, SHA256: sha256Hex(data), CreatedAt: capturedAt, Role: role}, nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func average(values []int64) float64 {
	if len(values) == 0 {
		return 0
	}
	var total int64
	for _, value := range values {
		total += value
	}
	return float64(total) / float64(len(values))
}

func round(value float64) float64 { return math.Round(value*100) / 100 }

func roundPointer(value *float64) *float64 {
	if value == nil {
		return nil
	}
	rounded := round(*value)
	return &rounded
}
