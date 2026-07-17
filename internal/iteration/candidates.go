package iteration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/claracore/cora/internal/cora"
)

var dynamicNumberPattern = regexp.MustCompile(`\d{6,}`)

func buildTriage(runID string, problems []DailyProblem, escalations []FrequencyEscalation, evidence map[string][]CodeEvidence) []TriageResult {
	escalated := map[string]bool{}
	for _, item := range escalations {
		escalated[problemKey(item.Service, item.Fingerprint, item.RootCauseKey)] = true
	}
	result := make([]TriageResult, 0, len(problems))
	for _, problem := range problems {
		codeEvidenceStatus := "not_collected"
		key := problemKey(problem.Service, problem.Fingerprint, problem.RootCauseKey)
		items := evidence[key]
		if len(items) == 0 {
			items = evidence[problemKey(problem.Service, problem.Fingerprint, "")]
		}
		if len(items) > 0 {
			codeEvidenceStatus = items[0].Status
		}
		classification := problem.Decision
		next := "review current runtime evidence and record an outcome if investigation changes the known case set"
		if problem.State == cora.ProblemStateRecurring {
			classification = "recurring"
			next = "compare the recurrence with the prior case and deployed rule provenance"
		} else if escalated[key] {
			classification = "ignore_frequency_escalation"
			next = "verify the frequency baseline and business meaning before changing the ignore rule"
		}
		result = append(result, TriageResult{
			SchemaVersion: "cora.triage-result.v1", IterationRunID: runID,
			ProductLine: problem.ProductLine, Service: problem.Service, Fingerprint: problem.Fingerprint,
			RootCauseKey:   problem.RootCauseKey,
			Classification: classification, WindowCount: problem.WindowCount,
			PriorDailyAverage: problem.PriorDailyAverage, FrequencyRatio: problem.FrequencyRatio,
			CurrentDecision: problem.Decision, CurrentRuleID: problem.RuleID,
			CaseIDs: problem.CaseIDs, CodeEvidenceStatus: codeEvidenceStatus,
			ReviewStatus: "needs_review", RecommendedNextStep: next,
		})
	}
	return result
}

func crystallizeCandidates(runID, productLine, snapshotID string, cases []cora.ProblemCase, evidence map[string][]CodeEvidence, capturedAt time.Time) []RuleCandidate {
	groups := map[string][]cora.ProblemCase{}
	for _, item := range cases {
		if item.ProductLine != productLine || !item.Handled {
			continue
		}
		key := problemKey(item.Service, item.Fingerprint, item.ContextSnapshot.Decision.RootCauseKey)
		groups[key] = append(groups[key], item)
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := []RuleCandidate{}
	for _, key := range keys {
		items := groups[key]
		if len(items) < 2 || !consistentOutcomes(items) {
			continue
		}
		problemEvidence := evidence[key]
		if len(problemEvidence) == 0 {
			problemEvidence = evidence[problemKey(items[0].Service, items[0].Fingerprint, "")]
		}
		if !hasVerifiedEvidence(problemEvidence) {
			continue
		}
		targetDecision := cora.DecisionIgnore
		category := "case-crystallized-expected-noise"
		if items[0].IsRealProblem {
			targetDecision = cora.DecisionAttention
			category = "case-crystallized-real-problem"
		}
		baseline := items[len(items)-1].ContextSnapshot.Decision.Decision
		if baseline == targetDecision {
			continue
		}
		match, ok := matchFromCases(items)
		if !ok {
			continue
		}
		caseIDs := make([]int64, 0, len(items))
		for _, item := range items {
			caseIDs = append(caseIDs, item.ID)
		}
		digest := sha256.Sum256([]byte(productLine + "\n" + key + "\n" + targetDecision))
		short := hex.EncodeToString(digest[:8])
		reason := commonRootCause(items)
		if reason == "" {
			reason = fmt.Sprintf("%d consistent handled cases for the same service-scoped fingerprint", len(items))
		}
		evidenceIDs := make([]string, 0, len(problemEvidence))
		for _, item := range problemEvidence {
			if item.Status == "verified" {
				evidenceIDs = append(evidenceIDs, item.EvidenceID)
			}
		}
		sort.Strings(evidenceIDs)
		result = append(result, RuleCandidate{
			SchemaVersion: CandidateSchemaVersion, CandidateID: "candidate-" + short,
			IterationRunID: runID, ProductLine: productLine,
			RuleID: "candidate_" + short, Decision: targetDecision, Category: category,
			Reason: reason, Match: match, SourceCaseIDs: caseIDs,
			Evidence: CandidateEvidence{CaseSnapshotID: snapshotID, EvalRunID: "eval-" + short,
				BaselineDecision: baseline, CandidateDecision: targetDecision,
				Summary:         fmt.Sprintf("derived from %d consistent handled cases with verified Atlas evidence; requires human review", len(items)),
				CodeEvidenceIDs: evidenceIDs},
			Risk: CandidateRisk{
				FalsePositive:               "matcher may cover unrelated events sharing class, method, and exception",
				FalseNegative:               "message variants or alternate wrapper classes may remain unmatched",
				FrequencyEscalationReviewed: false,
			},
			Status: "proposed", CreatedAt: capturedAt,
		})
	}
	return result
}

func hasVerifiedEvidence(items []CodeEvidence) bool {
	for _, item := range items {
		if item.Status == "verified" {
			return true
		}
	}
	return false
}

func consistentOutcomes(items []cora.ProblemCase) bool {
	first := items[0]
	for _, item := range items[1:] {
		if item.IsRealProblem != first.IsRealProblem || item.Service != first.Service ||
			item.Fingerprint != first.Fingerprint ||
			item.ContextSnapshot.Decision.RootCauseKey != first.ContextSnapshot.Decision.RootCauseKey {
			return false
		}
	}
	return true
}

func matchFromCases(items []cora.ProblemCase) (RuleMatch, bool) {
	var baseline cora.Event
	for index, item := range items {
		var event cora.Event
		if err := json.Unmarshal([]byte(item.ContextSnapshot.Problem.LatestSample), &event); err != nil {
			return RuleMatch{}, false
		}
		if index == 0 {
			baseline = event
			continue
		}
		if event.Logger != baseline.Logger || event.Method != baseline.Method ||
			event.ExceptionType != baseline.ExceptionType {
			return RuleMatch{}, false
		}
	}
	match := RuleMatch{Class: strings.TrimSpace(baseline.Logger), Method: strings.TrimSpace(baseline.Method),
		Exception: strings.TrimSpace(baseline.ExceptionType)}
	if match.Class == "" || (match.Method == "" && match.Exception == "") {
		return RuleMatch{}, false
	}
	return match, true
}

func commonRootCause(items []cora.ProblemCase) string {
	root := strings.TrimSpace(items[0].RootCause)
	if root == "" {
		return ""
	}
	for _, item := range items[1:] {
		if strings.TrimSpace(item.RootCause) != root {
			return ""
		}
	}
	return root
}

func evaluateCandidates(runID, productLine, snapshotID string, candidates []RuleCandidate, problems []DailyProblem, cases []cora.ProblemCase, escalations []FrequencyEscalation) ShadowEval {
	report := ShadowEval{
		SchemaVersion: "cora.shadow-eval.v1", IterationRunID: runID, ProductLine: productLine,
		CaseSnapshotID: snapshotID, CandidateCount: len(candidates), DailyProblems: len(problems),
		ProblemTransitions: map[string]int{}, OccurrenceTransitions: map[string]int64{},
		CandidateMatches: map[string][]string{}, FrequencyEscalations: escalations,
		ErrorIdentities: buildErrorIdentities(problems),
		Notes: []string{
			"candidate decisions are evaluated in shadow only and are never activated",
			"daily counts use trend window_end in the selected business-day boundary",
		},
	}
	for _, problem := range problems {
		candidateDecision, candidateID := decisionForProblem(problem, candidates)
		transition := problem.Decision + "->" + candidateDecision
		report.ProblemTransitions[transition]++
		report.OccurrenceTransitions[transition] += problem.WindowCount
		report.DailyOccurrences += problem.WindowCount
		if candidateID != "" {
			report.CandidateMatches[candidateID] = append(report.CandidateMatches[candidateID], problemKey(problem.Service, problem.Fingerprint, problem.RootCauseKey))
		}
	}
	for _, item := range cases {
		if !item.Handled {
			continue
		}
		decision := item.ContextSnapshot.Decision.Decision
		if event, ok := caseEvent(item); ok {
			if candidate := matchingCandidate(event, candidates); candidate != nil {
				decision = candidate.Decision
			}
		}
		if item.IsRealProblem {
			report.KnownRealProblemRecall.Total++
			if decision == cora.DecisionAttention {
				report.KnownRealProblemRecall.Attention++
			}
		} else if decision == cora.DecisionAttention {
			report.KnownNoiseEscalated++
		}
	}
	if report.KnownRealProblemRecall.Total > 0 {
		report.KnownRealProblemRecall.Recall = round(float64(report.KnownRealProblemRecall.Attention) /
			float64(report.KnownRealProblemRecall.Total))
	}
	for key := range report.CandidateMatches {
		sort.Strings(report.CandidateMatches[key])
	}
	return report
}

func buildErrorIdentities(problems []DailyProblem) []ErrorIdentity {
	byKey := make(map[string]DailyProblem, len(problems))
	for _, problem := range problems {
		byKey[problemKey(problem.Service, problem.Fingerprint, problem.RootCauseKey)] = problem
	}
	result := make([]ErrorIdentity, 0, len(problems))
	for _, problem := range problems {
		relatedSet := map[string]bool{}
		for _, key := range problem.RelatedProblemKeys {
			if related, ok := byKey[key]; ok && related.RuleID != "" && related.RuleID != problem.RuleID {
				relatedSet[related.RuleID] = true
			}
		}
		relatedRuleIDs := make([]string, 0, len(relatedSet))
		for ruleID := range relatedSet {
			relatedRuleIDs = append(relatedRuleIDs, ruleID)
		}
		sort.Strings(relatedRuleIDs)
		result = append(result, ErrorIdentity{
			ProblemID: problem.ProblemID, RuleID: problem.RuleID, Name: problem.Category,
			Reason: problem.Reason, Signature: problemSignature(problem), Service: problem.Service,
			Fingerprint: problem.Fingerprint, RootCauseKey: problem.RootCauseKey, Decision: problem.Decision,
			WindowCount: problem.WindowCount, RelatedRuleIDs: relatedRuleIDs,
		})
	}
	return result
}

func problemKey(service, fingerprint, rootCauseKey string) string {
	return service + ":" + fingerprint + ":" + rootCauseKey
}

func problemSignature(problem DailyProblem) string {
	if problem.Detail == nil {
		return problem.Category
	}
	var latestHandled *cora.ProblemCase
	for index := range problem.Detail.Cases {
		item := &problem.Detail.Cases[index]
		if !item.Handled || strings.TrimSpace(item.RootCause) == "" {
			continue
		}
		if latestHandled == nil || item.RecordedAt.After(latestHandled.RecordedAt) {
			latestHandled = item
		}
	}
	if latestHandled != nil {
		return strings.TrimSpace(latestHandled.RootCause)
	}
	var event cora.Event
	if err := json.Unmarshal([]byte(problem.Detail.Problem.LatestSample), &event); err != nil {
		return problem.Category
	}
	parts := make([]string, 0, 3)
	if value := strings.TrimSpace(event.ExceptionType); value != "" {
		parts = append(parts, value)
	}
	if value := strings.TrimSpace(event.Method); value != "" {
		parts = append(parts, value)
	}
	if value := stableSignatureText(event.Message); value != "" {
		parts = append(parts, value)
	}
	if len(parts) == 0 {
		return problem.Category
	}
	return strings.Join(parts, " · ")
}

func stableSignatureText(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	for _, marker := range []string{" notifyRequest=", " request=", " ordersDto=", " customerId=", " 上下文:"} {
		if index := strings.Index(value, marker); index >= 0 {
			value = value[:index]
		}
	}
	return dynamicNumberPattern.ReplaceAllString(strings.TrimSpace(value), "[ID]")
}

func decisionForProblem(problem DailyProblem, candidates []RuleCandidate) (string, string) {
	if problem.Detail == nil {
		return problem.Decision, ""
	}
	var event cora.Event
	if err := json.Unmarshal([]byte(problem.Detail.Problem.LatestSample), &event); err != nil {
		return problem.Decision, ""
	}
	if candidate := matchingCandidate(event, candidates); candidate != nil {
		return candidate.Decision, candidate.CandidateID
	}
	return problem.Decision, ""
}

func caseEvent(item cora.ProblemCase) (cora.Event, bool) {
	var event cora.Event
	if err := json.Unmarshal([]byte(item.ContextSnapshot.Problem.LatestSample), &event); err != nil {
		return cora.Event{}, false
	}
	return event, true
}

func matchingCandidate(event cora.Event, candidates []RuleCandidate) *RuleCandidate {
	for index := range candidates {
		if candidates[index].Match.matches(event) {
			return &candidates[index]
		}
	}
	return nil
}

func (match RuleMatch) matches(event cora.Event) bool {
	classText := event.Logger + "\n" + event.Stacktrace
	exceptionText := event.ExceptionType + "\n" + event.Stacktrace
	return containsIfSet(classText, match.Class) && containsAnyIfSet(classText, match.ClassContains) &&
		containsIfSet(event.Stacktrace, match.Method) && containsAnyIfSet(event.Message, match.MessageContains) &&
		containsIfSet(exceptionText, match.Exception) && containsAnyIfSet(exceptionText, match.ExceptionContains)
}

func containsIfSet(text, pattern string) bool {
	return pattern == "" || strings.Contains(text, pattern)
}

func containsAnyIfSet(text string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}

func buildPackPatch(candidates []RuleCandidate) []map[string]any {
	patch := make([]map[string]any, 0, len(candidates))
	for _, candidate := range candidates {
		patch = append(patch, map[string]any{
			"op": "add", "path": "/rules/-", "value": map[string]any{
				"id": candidate.RuleID, "decision": candidate.Decision,
				"category": candidate.Category, "reason": candidate.Reason, "match": candidate.Match,
			},
		})
	}
	return patch
}
