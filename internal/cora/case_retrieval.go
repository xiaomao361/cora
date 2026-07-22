package cora

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

const (
	CaseRetrievalSchemaVersion = "cora.case-retrieval.v1"
	caseRetrievalCandidatePool = 500
	caseRetrievalMinimumScore  = 40
)

var ErrCaseRetrievalRequiresUnmatched = errors.New("case retrieval requires a cora.default.unmatched problem")

type SimilarCase struct {
	Score        int         `json:"score"`
	MatchReasons []string    `json:"match_reasons"`
	Case         ProblemCase `json:"case"`
}

type CaseRetrieval struct {
	SchemaVersion  string        `json:"schema_version"`
	ProductLine    string        `json:"product_line"`
	QueryProblemID int64         `json:"query_problem_id"`
	QueryRuleID    string        `json:"query_rule_id"`
	Cases          []SimilarCase `json:"cases"`
}

// RetrieveCases returns deterministic, read-only evidence for one unmatched
// Problem. It never changes the stored decision and never crosses product-line
// boundaries. Only handled Cases are eligible because they carry a completed
// human or Agent outcome rather than an in-progress investigation.
func (s *Store) RetrieveCases(ctx context.Context, productLine, service, fingerprint, rootCauseKey string, limit int) (CaseRetrieval, error) {
	productLine = strings.TrimSpace(productLine)
	service = strings.TrimSpace(service)
	fingerprint = strings.TrimSpace(fingerprint)
	rootCauseKey = strings.TrimSpace(rootCauseKey)
	if productLine == "" || service == "" || fingerprint == "" {
		return CaseRetrieval{}, errors.New("product_line, service, and fingerprint are required")
	}
	if limit <= 0 || limit > 20 {
		limit = 5
	}

	query, err := s.GetProblemCause(ctx, productLine, service, fingerprint, rootCauseKey)
	if err != nil {
		return CaseRetrieval{}, err
	}
	if query.Decision.RuleID != "cora.default.unmatched" {
		return CaseRetrieval{}, ErrCaseRetrievalRequiresUnmatched
	}

	rows, err := s.db.QueryContext(ctx, `SELECT id, problem_id, product_line, service,
		fingerprint, actor, is_real_problem, handled, root_cause, action, prior_state,
		resulting_state, context_snapshot, recorded_at
		FROM problem_cases
		WHERE product_line = ? AND handled = 1
		ORDER BY recorded_at DESC, id DESC
		LIMIT ?`, productLine, caseRetrievalCandidatePool)
	if err != nil {
		return CaseRetrieval{}, err
	}
	defer rows.Close()

	matches := make([]SimilarCase, 0, limit)
	for rows.Next() {
		var item ProblemCase
		var isReal, handled int
		var contextSnapshot, recordedAt string
		if err := rows.Scan(&item.ID, &item.ProblemID, &item.ProductLine, &item.Service,
			&item.Fingerprint, &item.Actor, &isReal, &handled, &item.RootCause,
			&item.Action, &item.PriorState, &item.ResultingState, &contextSnapshot,
			&recordedAt); err != nil {
			return CaseRetrieval{}, err
		}
		item.IsRealProblem = isReal == 1
		item.Handled = handled == 1
		if err := json.Unmarshal([]byte(contextSnapshot), &item.ContextSnapshot); err != nil {
			return CaseRetrieval{}, err
		}
		item.RecordedAt, _ = time.Parse(time.RFC3339Nano, recordedAt)
		score, reasons := caseSimilarity(query.Problem, item)
		if score < caseRetrievalMinimumScore {
			continue
		}
		matches = append(matches, SimilarCase{Score: score, MatchReasons: reasons, Case: item})
	}
	if err := rows.Err(); err != nil {
		return CaseRetrieval{}, err
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		if !matches[i].Case.RecordedAt.Equal(matches[j].Case.RecordedAt) {
			return matches[i].Case.RecordedAt.After(matches[j].Case.RecordedAt)
		}
		return matches[i].Case.ID > matches[j].Case.ID
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return CaseRetrieval{
		SchemaVersion: CaseRetrievalSchemaVersion,
		ProductLine:   productLine, QueryProblemID: query.Problem.ID,
		QueryRuleID: query.Decision.RuleID, Cases: matches,
	}, nil
}

func caseSimilarity(query Problem, candidate ProblemCase) (int, []string) {
	caseProblem := candidate.ContextSnapshot.Problem
	caseDecision := candidate.ContextSnapshot.Decision
	score := 0
	reasons := []string{}
	add := func(points int, reason string) {
		score += points
		reasons = append(reasons, reason)
	}
	if query.Fingerprint == caseProblem.Fingerprint {
		add(100, "same_fingerprint")
	}
	if query.RootCauseKey != "" && query.RootCauseKey == caseDecision.RootCauseKey {
		add(80, "same_root_cause_key")
	}
	if query.Service == caseProblem.Service {
		add(25, "same_service")
	}
	if query.ExceptionType != "" && query.ExceptionType == caseProblem.ExceptionType {
		add(20, "same_exception_type")
	}
	if query.Logger != "" && query.Logger == caseProblem.Logger {
		add(15, "same_logger")
	}

	queryEvent, queryOK := sampleEvent(query.LatestSample)
	caseEvent, caseOK := sampleEvent(caseProblem.LatestSample)
	if queryOK && caseOK {
		if queryEvent.Method != "" && queryEvent.Method == caseEvent.Method {
			add(10, "same_method")
		}
		if normalizedMessage(queryEvent.Message) != "" && normalizedMessage(queryEvent.Message) == normalizedMessage(caseEvent.Message) {
			add(30, "same_normalized_message")
		}
		if sharesApplicationFrame(queryEvent.Stacktrace, caseEvent.Stacktrace) {
			add(10, "shared_application_frame")
		}
	}
	return score, reasons
}

func sampleEvent(sample string) (Event, bool) {
	var event Event
	if err := json.Unmarshal([]byte(sample), &event); err != nil {
		return Event{}, false
	}
	return event, true
}

func normalizedMessage(message string) string {
	message = numberPattern.ReplaceAllString(uuidPattern.ReplaceAllString(message, "<uuid>"), "<n>")
	return strings.Join(strings.Fields(message), " ")
}

func sharesApplicationFrame(left, right string) bool {
	leftFrames := applicationFrames(left, 5)
	if len(leftFrames) == 0 {
		return false
	}
	rightFrames := applicationFrames(right, 5)
	rightSet := make(map[string]bool, len(rightFrames))
	for _, frame := range rightFrames {
		rightSet[frame] = true
	}
	for _, frame := range leftFrames {
		if rightSet[frame] {
			return true
		}
	}
	return false
}
