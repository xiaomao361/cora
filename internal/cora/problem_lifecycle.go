package cora

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	ProblemStateNew          = "new"
	ProblemStateAcknowledged = "acknowledged"
	ProblemStateResolved     = "resolved"
	ProblemStateRecurring    = "recurring"
)

type ProblemCase struct {
	ID              int64               `json:"id"`
	ProblemID       int64               `json:"problem_id"`
	ProductLine     string              `json:"product_line"`
	Service         string              `json:"service"`
	Fingerprint     string              `json:"fingerprint"`
	Actor           string              `json:"actor"`
	IsRealProblem   bool                `json:"is_real_problem"`
	Handled         bool                `json:"handled"`
	RootCause       string              `json:"root_cause"`
	Action          string              `json:"action"`
	PriorState      string              `json:"prior_state"`
	ResultingState  string              `json:"resulting_state"`
	ContextSnapshot CaseContextSnapshot `json:"context_snapshot"`
	RecordedAt      time.Time           `json:"recorded_at"`
}

type CaseContextSnapshot struct {
	Problem  Problem      `json:"problem"`
	Decision CoraDecision `json:"decision"`
}

type ProblemDetail struct {
	Problem         Problem          `json:"problem"`
	Decision        CoraDecision     `json:"decision"`
	TrendPoints     []TrendPoint     `json:"trend_points"`
	NodeOccurrences []NodeOccurrence `json:"node_occurrences"`
	NodeTrendPoints []NodeTrendPoint `json:"node_trend_points"`
	RelatedProblems []RelatedProblem `json:"related_problems"`
	Cases           []ProblemCase    `json:"cases"`
}

type RelatedProblem struct {
	ProblemID      int64     `json:"problem_id"`
	Service        string    `json:"service"`
	Fingerprint    string    `json:"fingerprint"`
	State          string    `json:"state"`
	Decision       string    `json:"decision"`
	Category       string    `json:"category"`
	Count          int64     `json:"count"`
	FirstSeen      time.Time `json:"first_seen"`
	LastSeen       time.Time `json:"last_seen"`
	SharedTraceIDs []string  `json:"shared_trace_ids"`
}

type Outcome struct {
	ProductLine   string `json:"product_line"`
	Service       string `json:"service"`
	Fingerprint   string `json:"fingerprint"`
	Actor         string `json:"actor"`
	IsRealProblem bool   `json:"is_real_problem"`
	Handled       bool   `json:"handled"`
	RootCause     string `json:"root_cause"`
	Action        string `json:"action"`
}

type CaseExportPage struct {
	SchemaVersion         string        `json:"schema_version"`
	SnapshotID            string        `json:"snapshot_id"`
	ProductLine           string        `json:"product_line"`
	SnapshotThroughCaseID int64         `json:"snapshot_through_case_id"`
	AfterCaseID           int64         `json:"after_case_id"`
	NextAfterCaseID       int64         `json:"next_after_case_id"`
	HasMore               bool          `json:"has_more"`
	PageSHA256            string        `json:"page_sha256"`
	Cases                 []ProblemCase `json:"cases"`
}

func (s *Store) CurrentAttention(ctx context.Context, productLine string, limit int) ([]AttentionItem, error) {
	if strings.TrimSpace(productLine) == "" {
		return nil, errors.New("product_line is required")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, p.product_line, p.fingerprint, p.service, p.environment,
		       p.exception_type, p.logger, p.count, p.last_seen, p.state, p.state_changed_at,
		       d.decision, d.category, d.rule_id, d.reason, d.source,
		       d.experience_version, d.decided_at
		FROM problems p
		JOIN cora_decisions d ON d.product_line = p.product_line AND d.service = p.service
		 AND d.fingerprint = p.fingerprint
		WHERE p.product_line = ? AND p.state IN ('new', 'acknowledged', 'recurring') AND d.decision != 'ignore'
		ORDER BY CASE p.state WHEN 'recurring' THEN 0 WHEN 'new' THEN 1 ELSE 2 END,
		         CASE d.decision WHEN 'attention' THEN 0 ELSE 1 END,
		         p.last_seen DESC
		LIMIT ?`, productLine, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []AttentionItem{}
	for rows.Next() {
		var item AttentionItem
		var lastSeen, stateChangedAt, decidedAt string
		if err := rows.Scan(&item.ProblemID, &item.ProductLine, &item.Fingerprint,
			&item.Service, &item.Environment, &item.ExceptionType, &item.Logger,
			&item.Count, &lastSeen, &item.State, &stateChangedAt, &item.Decision,
			&item.Category, &item.RuleID, &item.Reason, &item.Source,
			&item.ExperienceVersion, &decidedAt); err != nil {
			return nil, err
		}
		item.LastSeen, _ = time.Parse(time.RFC3339Nano, lastSeen)
		item.StateChangedAt, _ = time.Parse(time.RFC3339Nano, stateChangedAt)
		item.DecidedAt, _ = time.Parse(time.RFC3339Nano, decidedAt)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetProblem(ctx context.Context, productLine, service, fingerprint string) (ProblemDetail, error) {
	var detail ProblemDetail
	if strings.TrimSpace(productLine) == "" || strings.TrimSpace(service) == "" || strings.TrimSpace(fingerprint) == "" {
		return detail, errors.New("product_line, service, and fingerprint are required")
	}
	var firstSeen, lastSeen, stateChangedAt, decidedAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT p.id, p.product_line, p.fingerprint, p.service, p.environment,
		       p.exception_type, p.logger, p.count, p.first_seen, p.last_seen,
		       p.first_sample, p.latest_sample, p.state, p.state_changed_at,
		       d.decision, d.category, d.rule_id, d.reason, d.source,
		       d.experience_version, d.decided_at
		FROM problems p
		JOIN cora_decisions d ON d.product_line = p.product_line AND d.service = p.service
		 AND d.fingerprint = p.fingerprint
		WHERE p.product_line = ? AND p.service = ? AND p.fingerprint = ?`,
		productLine, service, fingerprint).Scan(
		&detail.Problem.ID, &detail.Problem.ProductLine, &detail.Problem.Fingerprint,
		&detail.Problem.Service, &detail.Problem.Environment, &detail.Problem.ExceptionType,
		&detail.Problem.Logger, &detail.Problem.Count, &firstSeen, &lastSeen,
		&detail.Problem.FirstSample, &detail.Problem.LatestSample, &detail.Problem.State,
		&stateChangedAt, &detail.Decision.Decision, &detail.Decision.Category,
		&detail.Decision.RuleID, &detail.Decision.Reason, &detail.Decision.Source,
		&detail.Decision.ExperienceVersion, &decidedAt)
	if err != nil {
		return detail, err
	}
	detail.Problem.FirstSeen, _ = time.Parse(time.RFC3339Nano, firstSeen)
	detail.Problem.LastSeen, _ = time.Parse(time.RFC3339Nano, lastSeen)
	detail.Problem.StateChangedAt, _ = time.Parse(time.RFC3339Nano, stateChangedAt)
	detail.Decision.DecidedAt, _ = time.Parse(time.RFC3339Nano, decidedAt)
	if detail.TrendPoints, err = s.TrendPoints(ctx, productLine, service, fingerprint); err != nil {
		return ProblemDetail{}, err
	}
	if detail.NodeOccurrences, err = s.NodeOccurrences(ctx, productLine, service, fingerprint); err != nil {
		return ProblemDetail{}, err
	}
	if detail.NodeTrendPoints, err = s.NodeTrendPoints(ctx, productLine, service, fingerprint, ""); err != nil {
		return ProblemDetail{}, err
	}
	if detail.RelatedProblems, err = s.relatedProblems(ctx, detail.Problem); err != nil {
		return ProblemDetail{}, err
	}
	if detail.Cases, err = s.ProblemCases(ctx, productLine, service, fingerprint); err != nil {
		return ProblemDetail{}, err
	}
	return detail, nil
}

func (s *Store) relatedProblems(ctx context.Context, problem Problem) ([]RelatedProblem, error) {
	targetTraceIDs := problemTraceIDs(problem)
	if len(targetTraceIDs) == 0 {
		return []RelatedProblem{}, nil
	}
	traceA := targetTraceIDs[0]
	traceB := traceA
	if len(targetTraceIDs) > 1 {
		traceB = targetTraceIDs[1]
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, p.service, p.fingerprint, p.state, p.count, p.first_seen,
		       p.last_seen, p.first_sample, p.latest_sample, d.decision, d.category
		FROM problems p
		JOIN cora_decisions d ON d.product_line = p.product_line AND d.service = p.service
		 AND d.fingerprint = p.fingerprint
		WHERE p.product_line = ?
		  AND NOT (p.service = ? AND p.fingerprint = ?)
		  AND (json_extract(p.first_sample, '$.trace_id') IN (?, ?)
		       OR json_extract(p.latest_sample, '$.trace_id') IN (?, ?))
		ORDER BY p.last_seen DESC, p.id DESC`, problem.ProductLine, problem.Service,
		problem.Fingerprint, traceA, traceB, traceA, traceB)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	related := []RelatedProblem{}
	for rows.Next() {
		var item RelatedProblem
		var firstSeen, lastSeen, firstSample, latestSample string
		if err := rows.Scan(&item.ProblemID, &item.Service, &item.Fingerprint,
			&item.State, &item.Count, &firstSeen, &lastSeen, &firstSample,
			&latestSample, &item.Decision, &item.Category); err != nil {
			return nil, err
		}
		item.FirstSeen, _ = time.Parse(time.RFC3339Nano, firstSeen)
		item.LastSeen, _ = time.Parse(time.RFC3339Nano, lastSeen)
		candidateTraceIDs := sampleTraceIDs(firstSample, latestSample)
		for _, traceID := range targetTraceIDs {
			if candidateTraceIDs[traceID] {
				item.SharedTraceIDs = append(item.SharedTraceIDs, traceID)
			}
		}
		related = append(related, item)
	}
	return related, rows.Err()
}

func problemTraceIDs(problem Problem) []string {
	traceIDs := sampleTraceIDs(problem.FirstSample, problem.LatestSample)
	result := make([]string, 0, len(traceIDs))
	for _, sample := range []string{problem.FirstSample, problem.LatestSample} {
		var event Event
		if json.Unmarshal([]byte(sample), &event) == nil && event.TraceID != "" && traceIDs[event.TraceID] {
			alreadyAdded := false
			for _, existing := range result {
				alreadyAdded = alreadyAdded || existing == event.TraceID
			}
			if !alreadyAdded {
				result = append(result, event.TraceID)
			}
		}
	}
	return result
}

func sampleTraceIDs(samples ...string) map[string]bool {
	traceIDs := map[string]bool{}
	for _, sample := range samples {
		var event Event
		if json.Unmarshal([]byte(sample), &event) == nil && event.TraceID != "" {
			traceIDs[event.TraceID] = true
		}
	}
	return traceIDs
}

func (s *Store) ProblemCases(ctx context.Context, productLine, service, fingerprint string) ([]ProblemCase, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, problem_id, product_line, service,
		fingerprint, actor, is_real_problem, handled, root_cause, action, prior_state,
		resulting_state, context_snapshot, recorded_at
		FROM problem_cases
		WHERE product_line = ? AND service = ? AND fingerprint = ?
		ORDER BY recorded_at DESC, id DESC`, productLine, service, fingerprint)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cases := []ProblemCase{}
	for rows.Next() {
		var item ProblemCase
		var isReal, handled int
		var contextSnapshot, recordedAt string
		if err := rows.Scan(&item.ID, &item.ProblemID, &item.ProductLine, &item.Service,
			&item.Fingerprint, &item.Actor, &isReal, &handled, &item.RootCause,
			&item.Action, &item.PriorState, &item.ResultingState, &contextSnapshot,
			&recordedAt); err != nil {
			return nil, err
		}
		item.IsRealProblem = isReal == 1
		item.Handled = handled == 1
		if err := json.Unmarshal([]byte(contextSnapshot), &item.ContextSnapshot); err != nil {
			return nil, fmt.Errorf("decode case context snapshot: %w", err)
		}
		item.RecordedAt, _ = time.Parse(time.RFC3339Nano, recordedAt)
		cases = append(cases, item)
	}
	return cases, rows.Err()
}

func (s *Store) ExportCases(ctx context.Context, productLine string, afterCaseID, throughCaseID int64, limit int) (CaseExportPage, error) {
	productLine = strings.TrimSpace(productLine)
	if productLine == "" {
		return CaseExportPage{}, errors.New("product_line is required")
	}
	if afterCaseID < 0 || throughCaseID < 0 {
		return CaseExportPage{}, errors.New("after_case_id and through_case_id must not be negative")
	}
	if afterCaseID > 0 && throughCaseID == 0 {
		return CaseExportPage{}, errors.New("through_case_id is required after the first page")
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if throughCaseID == 0 {
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0)
			FROM problem_cases WHERE product_line = ?`, productLine).Scan(&throughCaseID); err != nil {
			return CaseExportPage{}, err
		}
	}
	if afterCaseID > throughCaseID {
		return CaseExportPage{}, errors.New("after_case_id must not exceed through_case_id")
	}

	rows, err := s.db.QueryContext(ctx, `SELECT id, problem_id, product_line, service,
		fingerprint, actor, is_real_problem, handled, root_cause, action, prior_state,
		resulting_state, context_snapshot, recorded_at
		FROM problem_cases
		WHERE product_line = ? AND id > ? AND id <= ?
		ORDER BY id ASC
		LIMIT ?`, productLine, afterCaseID, throughCaseID, limit+1)
	if err != nil {
		return CaseExportPage{}, err
	}
	defer rows.Close()
	cases := make([]ProblemCase, 0, limit+1)
	for rows.Next() {
		var item ProblemCase
		var isReal, handled int
		var contextSnapshot, recordedAt string
		if err := rows.Scan(&item.ID, &item.ProblemID, &item.ProductLine, &item.Service,
			&item.Fingerprint, &item.Actor, &isReal, &handled, &item.RootCause,
			&item.Action, &item.PriorState, &item.ResultingState, &contextSnapshot,
			&recordedAt); err != nil {
			return CaseExportPage{}, err
		}
		item.IsRealProblem = isReal == 1
		item.Handled = handled == 1
		if err := json.Unmarshal([]byte(contextSnapshot), &item.ContextSnapshot); err != nil {
			return CaseExportPage{}, fmt.Errorf("decode case context snapshot: %w", err)
		}
		item.RecordedAt, _ = time.Parse(time.RFC3339Nano, recordedAt)
		cases = append(cases, item)
	}
	if err := rows.Err(); err != nil {
		return CaseExportPage{}, err
	}
	hasMore := len(cases) > limit
	if hasMore {
		cases = cases[:limit]
	}
	nextAfterCaseID := afterCaseID
	if len(cases) > 0 {
		nextAfterCaseID = cases[len(cases)-1].ID
	}
	canonical, err := json.Marshal(cases)
	if err != nil {
		return CaseExportPage{}, fmt.Errorf("marshal exported cases: %w", err)
	}
	pageHash := sha256.Sum256(canonical)
	return CaseExportPage{
		SchemaVersion: "cora-case.v1", SnapshotID: fmt.Sprintf("%s:%d", productLine, throughCaseID),
		ProductLine: productLine, SnapshotThroughCaseID: throughCaseID, AfterCaseID: afterCaseID,
		NextAfterCaseID: nextAfterCaseID, HasMore: hasMore,
		PageSHA256: hex.EncodeToString(pageHash[:]), Cases: cases,
	}, nil
}

func (s *Store) RecordOutcome(ctx context.Context, outcome Outcome) (result ProblemCase, resultErr error) {
	outcome.ProductLine = strings.TrimSpace(outcome.ProductLine)
	outcome.Service = strings.TrimSpace(outcome.Service)
	outcome.Fingerprint = strings.TrimSpace(outcome.Fingerprint)
	outcome.Actor = strings.TrimSpace(outcome.Actor)
	outcome.RootCause = strings.TrimSpace(outcome.RootCause)
	outcome.Action = strings.TrimSpace(outcome.Action)
	if outcome.ProductLine == "" || outcome.Service == "" || outcome.Fingerprint == "" ||
		outcome.Actor == "" || outcome.RootCause == "" || outcome.Action == "" {
		return result, errors.New("product_line, service, fingerprint, actor, root_cause, and action are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.recordWrite(err)
		return result, err
	}
	defer tx.Rollback()
	writeAttempted := false
	defer func() {
		if writeAttempted {
			s.recordWrite(resultErr)
		}
	}()
	var problem Problem
	var decision CoraDecision
	var firstSeen, lastSeen, stateChangedAt, decidedAt string
	err = tx.QueryRowContext(ctx, `SELECT p.id, p.product_line, p.fingerprint, p.service,
		p.environment, p.exception_type, p.logger, p.count, p.first_seen, p.last_seen,
		p.first_sample, p.latest_sample, p.state, p.state_changed_at,
		d.decision, d.category, d.rule_id, d.reason, d.source, d.experience_version, d.decided_at
		FROM problems p JOIN cora_decisions d
		ON d.product_line = p.product_line AND d.service = p.service AND d.fingerprint = p.fingerprint
		WHERE p.product_line = ? AND p.service = ? AND p.fingerprint = ?`, outcome.ProductLine,
		outcome.Service, outcome.Fingerprint).Scan(&problem.ID, &problem.ProductLine,
		&problem.Fingerprint, &problem.Service, &problem.Environment, &problem.ExceptionType,
		&problem.Logger, &problem.Count, &firstSeen, &lastSeen, &problem.FirstSample,
		&problem.LatestSample, &problem.State, &stateChangedAt, &decision.Decision,
		&decision.Category, &decision.RuleID, &decision.Reason, &decision.Source,
		&decision.ExperienceVersion, &decidedAt)
	if err != nil {
		return result, err
	}
	problem.FirstSeen, _ = time.Parse(time.RFC3339Nano, firstSeen)
	problem.LastSeen, _ = time.Parse(time.RFC3339Nano, lastSeen)
	problem.StateChangedAt, _ = time.Parse(time.RFC3339Nano, stateChangedAt)
	decision.DecidedAt, _ = time.Parse(time.RFC3339Nano, decidedAt)
	contextSnapshot := CaseContextSnapshot{Problem: problem, Decision: decision}
	snapshot, err := json.Marshal(contextSnapshot)
	if err != nil {
		return result, fmt.Errorf("marshal problem snapshot: %w", err)
	}
	resultingState := ProblemStateAcknowledged
	if outcome.Handled {
		resultingState = ProblemStateResolved
	}
	now := time.Now().UTC()
	writeAttempted = true
	dbResult, err := tx.ExecContext(ctx, `INSERT INTO problem_cases
		(problem_id, product_line, service, fingerprint, actor, is_real_problem, handled,
		 root_cause, action, prior_state, resulting_state, context_snapshot, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, problem.ID, outcome.ProductLine,
		outcome.Service, outcome.Fingerprint, outcome.Actor, outcome.IsRealProblem,
		outcome.Handled, outcome.RootCause, outcome.Action, problem.State, resultingState,
		string(snapshot), now.Format(time.RFC3339Nano))
	if err != nil {
		return result, err
	}
	caseID, err := dbResult.LastInsertId()
	if err != nil {
		return result, err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE problems SET state = ?, state_changed_at = ?
		WHERE id = ? AND state = ?`, resultingState, now.Format(time.RFC3339Nano), problem.ID, problem.State)
	if err != nil {
		return result, err
	}
	rowsAffected, err := updated.RowsAffected()
	if err != nil || rowsAffected != 1 {
		if err == nil {
			err = errors.New("problem changed while recording outcome")
		}
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return ProblemCase{
		ID: caseID, ProblemID: problem.ID, ProductLine: outcome.ProductLine,
		Service: outcome.Service, Fingerprint: outcome.Fingerprint, Actor: outcome.Actor,
		IsRealProblem: outcome.IsRealProblem, Handled: outcome.Handled,
		RootCause: outcome.RootCause, Action: outcome.Action, PriorState: problem.State,
		ResultingState: resultingState, ContextSnapshot: contextSnapshot, RecordedAt: now,
	}, nil
}
