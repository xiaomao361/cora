package cora

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const OnlineRetentionAuditSchemaVersion = "cora.online-retention-audit.v1"

const (
	onlineRetentionAuditMode = "live_read_only_preflight"
	offlineReceiptReason     = "closure_receipt_verification_requires_offline_audit"
)

type OnlineRetentionAudit struct {
	SchemaVersion         string                      `json:"schema_version"`
	Mode                  string                      `json:"mode"`
	ProductLine           string                      `json:"product_line"`
	CapturedAt            time.Time                   `json:"captured_at"`
	Summary               OnlineRetentionAuditSummary `json:"summary"`
	AfterProblemID        int64                       `json:"after_problem_id"`
	NextAfterProblemID    int64                       `json:"next_after_problem_id"`
	HasMore               bool                        `json:"has_more"`
	Problems              []OnlineRetentionProblem    `json:"problems"`
	ForensicAuditRequired bool                        `json:"forensic_audit_required"`
	Caveats               []string                    `json:"caveats"`
}

type OnlineRetentionAuditSummary struct {
	ProblemCount              int64 `json:"problem_count"`
	ResolvedProblems          int64 `json:"resolved_problems"`
	ProblemsWithHandledCase   int64 `json:"problems_with_handled_case"`
	ForensicAuditCandidates   int64 `json:"forensic_audit_candidates"`
	RetentionEligibleProblems int64 `json:"retention_eligible_problems"`
}

type OnlineRetentionProblem struct {
	ProblemID              int64     `json:"problem_id"`
	ProductLine            string    `json:"product_line"`
	Service                string    `json:"service"`
	Fingerprint            string    `json:"fingerprint"`
	State                  string    `json:"state"`
	Decision               string    `json:"decision"`
	OccurrenceCount        int64     `json:"occurrence_count"`
	LastSeen               time.Time `json:"last_seen"`
	CaseCount              int64     `json:"case_count"`
	HandledCaseCount       int64     `json:"handled_case_count"`
	ForensicAuditCandidate bool      `json:"forensic_audit_candidate"`
	RetentionEligible      bool      `json:"retention_eligible"`
	BlockingReasons        []string  `json:"blocking_reasons"`
}

func (s *Store) OnlineRetentionAudit(ctx context.Context, productLine string, afterProblemID int64, limit int) (OnlineRetentionAudit, error) {
	productLine = strings.TrimSpace(productLine)
	if productLine == "" {
		return OnlineRetentionAudit{}, errors.New("product_line is required")
	}
	if afterProblemID < 0 {
		return OnlineRetentionAudit{}, errors.New("after_problem_id must not be negative")
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return OnlineRetentionAudit{}, err
	}
	defer tx.Rollback()

	audit := OnlineRetentionAudit{
		SchemaVersion:         OnlineRetentionAuditSchemaVersion,
		Mode:                  onlineRetentionAuditMode,
		ProductLine:           productLine,
		CapturedAt:            time.Now().UTC(),
		AfterProblemID:        afterProblemID,
		Problems:              []OnlineRetentionProblem{},
		ForensicAuditRequired: true,
		Caveats: []string{
			"This live preflight does not read closure receipts or local immutable artifacts.",
			"Retention eligibility and deletion authorization require cora-retention-audit against a consistent backup.",
		},
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN p.state = 'resolved' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN COALESCE(c.handled_count, 0) > 0 THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN p.state = 'resolved' AND COALESCE(c.handled_count, 0) > 0 THEN 1 ELSE 0 END), 0)
		FROM problems p
		LEFT JOIN (
			SELECT problem_id, SUM(CASE WHEN handled = 1 THEN 1 ELSE 0 END) AS handled_count
			FROM problem_cases
			WHERE product_line = ?
			GROUP BY problem_id
		) c ON c.problem_id = p.id
		WHERE p.product_line = ?`, productLine, productLine).Scan(
		&audit.Summary.ProblemCount,
		&audit.Summary.ResolvedProblems,
		&audit.Summary.ProblemsWithHandledCase,
		&audit.Summary.ForensicAuditCandidates,
	); err != nil {
		return OnlineRetentionAudit{}, fmt.Errorf("summarize online retention preflight: %w", err)
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT p.id, p.product_line, p.service, p.fingerprint, p.state,
		       COALESCE(d.decision, 'none'), p.count, p.last_seen,
		       COUNT(c.id), COALESCE(SUM(CASE WHEN c.handled = 1 THEN 1 ELSE 0 END), 0)
		FROM problems p
		LEFT JOIN cora_decisions d ON d.product_line = p.product_line
		 AND d.service = p.service AND d.fingerprint = p.fingerprint
		LEFT JOIN problem_cases c ON c.problem_id = p.id AND c.product_line = p.product_line
		WHERE p.product_line = ? AND p.id > ?
		GROUP BY p.id, p.product_line, p.service, p.fingerprint, p.state,
		         d.decision, p.count, p.last_seen
		ORDER BY p.id
		LIMIT ?`, productLine, afterProblemID, limit+1)
	if err != nil {
		return OnlineRetentionAudit{}, fmt.Errorf("read online retention preflight: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item OnlineRetentionProblem
		var lastSeen string
		if err := rows.Scan(&item.ProblemID, &item.ProductLine, &item.Service, &item.Fingerprint,
			&item.State, &item.Decision, &item.OccurrenceCount, &lastSeen, &item.CaseCount,
			&item.HandledCaseCount); err != nil {
			return OnlineRetentionAudit{}, err
		}
		item.LastSeen, err = time.Parse(time.RFC3339Nano, lastSeen)
		if err != nil {
			return OnlineRetentionAudit{}, fmt.Errorf("parse problem %d last_seen: %w", item.ProblemID, err)
		}
		if item.State != ProblemStateResolved {
			item.BlockingReasons = append(item.BlockingReasons, "problem_active")
		}
		if item.HandledCaseCount == 0 {
			item.BlockingReasons = append(item.BlockingReasons, "handled_case_missing")
		}
		item.ForensicAuditCandidate = len(item.BlockingReasons) == 0
		item.BlockingReasons = append(item.BlockingReasons, offlineReceiptReason)
		audit.Problems = append(audit.Problems, item)
	}
	if err := rows.Err(); err != nil {
		return OnlineRetentionAudit{}, err
	}
	if len(audit.Problems) > limit {
		audit.HasMore = true
		audit.Problems = audit.Problems[:limit]
	}
	if len(audit.Problems) > 0 {
		audit.NextAfterProblemID = audit.Problems[len(audit.Problems)-1].ProblemID
	}
	if err := tx.Commit(); err != nil {
		return OnlineRetentionAudit{}, err
	}
	return audit, nil
}
