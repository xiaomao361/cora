package cora

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const IterationSnapshotSchemaVersion = "cora.iteration-snapshot.v1"

type IterationSnapshot struct {
	SchemaVersion string                   `json:"schema_version"`
	ProductLine   string                   `json:"product_line"`
	BusinessDate  string                   `json:"business_date"`
	Timezone      string                   `json:"timezone"`
	WindowStart   time.Time                `json:"window_start"`
	WindowEnd     time.Time                `json:"window_end"`
	BaselineStart time.Time                `json:"baseline_start"`
	BaselineDays  int                      `json:"baseline_days"`
	GeneratedAt   time.Time                `json:"generated_at"`
	Summary       IterationSnapshotSummary `json:"summary"`
	Problems      []IterationProblem       `json:"problems"`
}

type IterationSnapshotSummary struct {
	MatchedProblemCount      int              `json:"matched_problem_count"`
	ReturnedProblemCount     int              `json:"returned_problem_count"`
	OccurrenceCount          int64            `json:"occurrence_count"`
	DecisionProblemCounts    map[string]int   `json:"decision_problem_counts"`
	DecisionOccurrenceCounts map[string]int64 `json:"decision_occurrence_counts"`
	Truncated                bool             `json:"truncated"`
}

type IterationProblem struct {
	ProblemID         int64                 `json:"problem_id"`
	ProductLine       string                `json:"product_line"`
	Service           string                `json:"service"`
	Fingerprint       string                `json:"fingerprint"`
	Environment       string                `json:"environment"`
	ExceptionType     string                `json:"exception_type"`
	Logger            string                `json:"logger"`
	State             string                `json:"state"`
	TotalCount        int64                 `json:"total_count"`
	FirstSeen         time.Time             `json:"first_seen"`
	LastSeen          time.Time             `json:"last_seen"`
	Decision          string                `json:"decision"`
	RootCauseKey      string                `json:"root_cause_key"`
	Category          string                `json:"category"`
	RuleID            string                `json:"rule_id"`
	Reason            string                `json:"reason"`
	Source            string                `json:"source"`
	ExperienceVersion string                `json:"experience_version,omitempty"`
	DecidedAt         time.Time             `json:"decided_at"`
	WindowCount       int64                 `json:"window_count"`
	PriorDailyCounts  []IterationDailyCount `json:"prior_daily_counts"`
	PriorDailyAverage float64               `json:"prior_daily_average"`
	FrequencyRatio    *float64              `json:"frequency_ratio,omitempty"`
	NodeCounts        []IterationNodeCount  `json:"node_counts"`
	CaseIDs           []int64               `json:"case_ids"`
}

type IterationDailyCount struct {
	BusinessDate string `json:"business_date"`
	Count        int64  `json:"count"`
}

type IterationNodeCount struct {
	Node            string `json:"node"`
	DeploymentGroup string `json:"deployment_group,omitempty"`
	Count           int64  `json:"count"`
}

type iterationProblemAccumulator struct {
	problem    IterationProblem
	priorIndex map[string]int
}

func (s *Store) IterationSnapshot(ctx context.Context, productLine, businessDate, timezone string, baselineDays, limit int) (IterationSnapshot, error) {
	productLine = strings.TrimSpace(productLine)
	businessDate = strings.TrimSpace(businessDate)
	timezone = strings.TrimSpace(timezone)
	if productLine == "" || businessDate == "" {
		return IterationSnapshot{}, errors.New("product_line and business_date are required")
	}
	if timezone == "" {
		timezone = "Asia/Shanghai"
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return IterationSnapshot{}, fmt.Errorf("load timezone %q: %w", timezone, err)
	}
	date, err := time.ParseInLocation("2006-01-02", businessDate, location)
	if err != nil || date.Format("2006-01-02") != businessDate {
		return IterationSnapshot{}, errors.New("business_date must be YYYY-MM-DD")
	}
	if baselineDays <= 0 || baselineDays > 30 {
		baselineDays = 7
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	windowStart := date.UTC()
	windowEnd := date.AddDate(0, 0, 1).UTC()
	baselineStart := date.AddDate(0, 0, -baselineDays).UTC()
	baselineDates := make([]string, 0, baselineDays)
	for offset := baselineDays; offset > 0; offset-- {
		baselineDates = append(baselineDates, date.AddDate(0, 0, -offset).Format("2006-01-02"))
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return IterationSnapshot{}, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		SELECT p.id, p.product_line, p.service, p.fingerprint, p.environment,
		       p.exception_type, p.logger, p.state, p.count, p.first_seen, p.last_seen,
		       d.decision, d.root_cause_key, d.category, d.rule_id, d.reason, d.source,
		       d.experience_version, d.decided_at, t.count, t.window_end
		FROM problems p
		JOIN cora_decisions d ON d.product_line = p.product_line
		 AND d.service = p.service AND d.fingerprint = p.fingerprint
		 AND d.root_cause_key = p.root_cause_key
		JOIN trend_points t ON t.product_line = p.product_line
		 AND t.service = p.service AND t.fingerprint = p.fingerprint
		 AND t.root_cause_key = p.root_cause_key
		WHERE p.product_line = ? AND t.window_end > ? AND t.window_end <= ?
		ORDER BY p.id, t.window_end`, productLine,
		baselineStart.Format(time.RFC3339Nano), windowEnd.Format(time.RFC3339Nano))
	if err != nil {
		return IterationSnapshot{}, err
	}
	defer rows.Close()
	accumulators := map[int64]*iterationProblemAccumulator{}
	for rows.Next() {
		var item IterationProblem
		var firstSeen, lastSeen, decidedAt, trendEnd string
		var trendCount int64
		if err := rows.Scan(&item.ProblemID, &item.ProductLine, &item.Service, &item.Fingerprint,
			&item.Environment, &item.ExceptionType, &item.Logger, &item.State, &item.TotalCount,
			&firstSeen, &lastSeen, &item.Decision, &item.RootCauseKey, &item.Category,
			&item.RuleID, &item.Reason,
			&item.Source, &item.ExperienceVersion, &decidedAt, &trendCount, &trendEnd); err != nil {
			return IterationSnapshot{}, err
		}
		accumulator := accumulators[item.ProblemID]
		if accumulator == nil {
			item.FirstSeen, _ = time.Parse(time.RFC3339Nano, firstSeen)
			item.LastSeen, _ = time.Parse(time.RFC3339Nano, lastSeen)
			item.DecidedAt, _ = time.Parse(time.RFC3339Nano, decidedAt)
			item.NodeCounts = make([]IterationNodeCount, 0)
			item.CaseIDs = make([]int64, 0)
			accumulator = &iterationProblemAccumulator{problem: item, priorIndex: map[string]int{}}
			for index, priorDate := range baselineDates {
				accumulator.problem.PriorDailyCounts = append(accumulator.problem.PriorDailyCounts,
					IterationDailyCount{BusinessDate: priorDate})
				accumulator.priorIndex[priorDate] = index
			}
			accumulators[item.ProblemID] = accumulator
		}
		parsedEnd, err := time.Parse(time.RFC3339Nano, trendEnd)
		if err != nil {
			return IterationSnapshot{}, fmt.Errorf("parse trend window_end: %w", err)
		}
		if parsedEnd.After(windowStart) && !parsedEnd.After(windowEnd) {
			accumulator.problem.WindowCount += trendCount
			continue
		}
		priorDate := parsedEnd.In(location).Format("2006-01-02")
		if index, ok := accumulator.priorIndex[priorDate]; ok {
			accumulator.problem.PriorDailyCounts[index].Count += trendCount
		}
	}
	if err := rows.Err(); err != nil {
		return IterationSnapshot{}, err
	}

	problems := make([]IterationProblem, 0, len(accumulators))
	for _, accumulator := range accumulators {
		item := accumulator.problem
		if item.WindowCount == 0 {
			continue
		}
		var priorTotal int64
		for _, daily := range item.PriorDailyCounts {
			priorTotal += daily.Count
		}
		item.PriorDailyAverage = float64(priorTotal) / float64(baselineDays)
		if item.PriorDailyAverage > 0 {
			ratio := float64(item.WindowCount) / item.PriorDailyAverage
			item.FrequencyRatio = &ratio
		}
		problems = append(problems, item)
	}
	sort.Slice(problems, func(i, j int) bool {
		if problems[i].WindowCount != problems[j].WindowCount {
			return problems[i].WindowCount > problems[j].WindowCount
		}
		if problems[i].Service != problems[j].Service {
			return problems[i].Service < problems[j].Service
		}
		return problems[i].Fingerprint < problems[j].Fingerprint
	})

	summary := IterationSnapshotSummary{MatchedProblemCount: len(problems),
		DecisionProblemCounts: map[string]int{}, DecisionOccurrenceCounts: map[string]int64{}}
	for _, item := range problems {
		summary.OccurrenceCount += item.WindowCount
		summary.DecisionProblemCounts[item.Decision]++
		summary.DecisionOccurrenceCounts[item.Decision] += item.WindowCount
	}
	if len(problems) > limit {
		problems = problems[:limit]
		summary.Truncated = true
	}
	summary.ReturnedProblemCount = len(problems)
	if err := attachIterationNodeCounts(ctx, tx, productLine, windowStart, windowEnd, problems); err != nil {
		return IterationSnapshot{}, err
	}
	if err := attachIterationCaseIDs(ctx, tx, productLine, problems); err != nil {
		return IterationSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return IterationSnapshot{}, err
	}
	return IterationSnapshot{SchemaVersion: IterationSnapshotSchemaVersion,
		ProductLine: productLine, BusinessDate: businessDate, Timezone: timezone,
		WindowStart: windowStart, WindowEnd: windowEnd, BaselineStart: baselineStart,
		BaselineDays: baselineDays, GeneratedAt: time.Now().UTC(), Summary: summary, Problems: problems}, nil
}

func attachIterationNodeCounts(ctx context.Context, tx *sql.Tx, productLine string, start, end time.Time, problems []IterationProblem) error {
	byIdentity := map[string]*IterationProblem{}
	for index := range problems {
		byIdentity[problems[index].Service+"\x00"+problems[index].Fingerprint+"\x00"+problems[index].RootCauseKey] = &problems[index]
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT service, fingerprint, root_cause_key, node, deployment_group, SUM(count)
		FROM node_trend_points
		WHERE product_line = ? AND window_end > ? AND window_end <= ?
		GROUP BY service, fingerprint, root_cause_key, node, deployment_group
		ORDER BY service, fingerprint, root_cause_key, SUM(count) DESC, node`, productLine,
		start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var service, fingerprint, rootCauseKey, node, group string
		var count int64
		if err := rows.Scan(&service, &fingerprint, &rootCauseKey, &node, &group, &count); err != nil {
			return err
		}
		if item := byIdentity[service+"\x00"+fingerprint+"\x00"+rootCauseKey]; item != nil {
			item.NodeCounts = append(item.NodeCounts, IterationNodeCount{Node: node, DeploymentGroup: group, Count: count})
		}
	}
	return rows.Err()
}

func attachIterationCaseIDs(ctx context.Context, tx *sql.Tx, productLine string, problems []IterationProblem) error {
	byID := map[int64]*IterationProblem{}
	for index := range problems {
		byID[problems[index].ProblemID] = &problems[index]
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, problem_id FROM problem_cases
		WHERE product_line = ? ORDER BY id`, productLine)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var caseID, problemID int64
		if err := rows.Scan(&caseID, &problemID); err != nil {
			return err
		}
		if item := byID[problemID]; item != nil {
			item.CaseIDs = append(item.CaseIDs, caseID)
		}
	}
	return rows.Err()
}
