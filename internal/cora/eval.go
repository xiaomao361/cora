package cora

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type ShadowEvalReport struct {
	SchemaVersion     string             `json:"schema_version"`
	Model             string             `json:"model"`
	ModelVersion      string             `json:"model_version"`
	ProductLine       string             `json:"product_line"`
	ExperienceVersion string             `json:"experience_version"`
	SourceSHA256      string             `json:"source_sha256"`
	DataQuality       EvalDataQuality    `json:"data_quality"`
	RowMetrics        EvalRowMetrics     `json:"row_metrics"`
	Signature         EvalSignature      `json:"signature_metrics"`
	RuleConflicts     EvalRuleConflicts  `json:"rule_conflicts"`
	Disagreements     []EvalDisagreement `json:"redacted_disagreements"`
}

type EvalDataQuality struct {
	Rows                   int            `json:"rows"`
	Columns                int            `json:"columns"`
	DuplicateIDs           int            `json:"duplicate_ids"`
	MissingByColumn        map[string]int `json:"missing_by_column"`
	LabelCounts            map[string]int `json:"label_counts"`
	SourceCounts           map[string]int `json:"source_counts"`
	ParsedTimestamps       int            `json:"parsed_timestamps"`
	TimestampCoverage      float64        `json:"timestamp_coverage"`
	TimestampMin           string         `json:"timestamp_min,omitempty"`
	TimestampMax           string         `json:"timestamp_max,omitempty"`
	TimeSplitAvailable     bool           `json:"time_split_available"`
	TimeSplitUnavailableBy string         `json:"time_split_unavailable_reason,omitempty"`
	LenientCSVParsing      bool           `json:"lenient_csv_parsing"`
	CSVParseWarning        string         `json:"csv_parse_warning,omitempty"`
}

type EvalRowMetrics struct {
	DecisionCounts       map[string]int            `json:"decision_counts"`
	Confusion            map[string]map[string]int `json:"label_by_decision"`
	DecidedRows          int                       `json:"decided_rows"`
	Coverage             float64                   `json:"coverage"`
	AgreementOnDecided   float64                   `json:"agreement_on_decided"`
	AttentionRecall      float64                   `json:"attention_recall"`
	AttentionToIgnore    int                       `json:"attention_to_ignore"`
	IgnoreToAttention    int                       `json:"ignore_to_attention"`
	UnsupportedLabelRows int                       `json:"unsupported_label_rows"`
	TransitionByRule     map[string]int            `json:"transition_by_rule"`
}

type EvalSignature struct {
	Unique                  int     `json:"unique"`
	DuplicateRows           int     `json:"duplicate_rows"`
	DuplicateRate           float64 `json:"duplicate_rate"`
	LabelConflictSignatures int     `json:"label_conflict_signatures"`
	DecisionConflicts       int     `json:"decision_conflict_signatures"`
	StableSignatures        int     `json:"stable_signatures"`
	StableDecided           int     `json:"stable_decided_signatures"`
	StableAgreement         float64 `json:"stable_agreement_on_decided"`
}

type EvalRuleConflicts struct {
	RowsMatchingMultipleRules int `json:"rows_matching_multiple_rules"`
	MaxRulesMatched           int `json:"max_rules_matched"`
}

type EvalDisagreement struct {
	Fingerprint string `json:"fingerprint"`
	SourceFile  string `json:"source_file"`
	LineNumber  string `json:"line_number"`
	Label       string `json:"label"`
	Decision    string `json:"decision"`
	RuleID      string `json:"rule_id"`
	Category    string `json:"category"`
}

type evalSignatureState struct {
	labels    map[string]bool
	decisions map[string]bool
}

func EvaluateCoraCSV(ctx context.Context, reader io.Reader, productLine string) (ShadowEvalReport, error) {
	if productLine == "" {
		return ShadowEvalReport{}, fmt.Errorf("product line is required")
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return ShadowEvalReport{}, fmt.Errorf("read evaluation CSV: %w", err)
	}
	core, err := defaultCoraCore()
	if err != nil {
		return ShadowEvalReport{}, err
	}
	ruleCore, ok := core.(*ruleCora)
	if !ok {
		return ShadowEvalReport{}, fmt.Errorf("shadow evaluation requires rule Cora")
	}
	pack, ok := ruleCore.packs[productLine]
	if !ok {
		return ShadowEvalReport{}, fmt.Errorf("no experience pack for product line %q", productLine)
	}

	parser := csv.NewReader(bytes.NewReader(data))
	rows, strictParseErr := parser.ReadAll()
	if strictParseErr != nil {
		parser = csv.NewReader(bytes.NewReader(data))
		parser.LazyQuotes = true
		parser.FieldsPerRecord = -1
		rows, err = parser.ReadAll()
		if err != nil {
			return ShadowEvalReport{}, fmt.Errorf("parse evaluation CSV after strict failure %q: %w", strictParseErr, err)
		}
	}
	if len(rows) < 2 {
		return ShadowEvalReport{}, fmt.Errorf("evaluation CSV has no data rows")
	}
	header := make(map[string]int, len(rows[0]))
	for index, name := range rows[0] {
		header[strings.TrimSpace(name)] = index
	}
	required := []string{"id", "source_file", "line_number", "timestamp", "class_name", "method", "message", "exception", "label"}
	for _, name := range required {
		if _, exists := header[name]; !exists {
			return ShadowEvalReport{}, fmt.Errorf("evaluation CSV missing required column %q", name)
		}
	}

	sum := sha256.Sum256(data)
	report := ShadowEvalReport{
		SchemaVersion: "cora.shadow-eval.v1", Model: "cora", ModelVersion: "0.1.0",
		ProductLine: productLine, ExperienceVersion: pack.Version,
		SourceSHA256: hex.EncodeToString(sum[:]),
		DataQuality: EvalDataQuality{Columns: len(rows[0]), MissingByColumn: make(map[string]int),
			LabelCounts: make(map[string]int), SourceCounts: make(map[string]int)},
		RowMetrics: EvalRowMetrics{DecisionCounts: make(map[string]int), Confusion: make(map[string]map[string]int),
			TransitionByRule: make(map[string]int)},
	}
	if strictParseErr != nil {
		report.DataQuality.LenientCSVParsing = true
		report.DataQuality.CSVParseWarning = strictParseErr.Error()
	}
	ids := make(map[string]bool)
	signatures := make(map[string]*evalSignatureState)
	var timestampMin, timestampMax time.Time
	for _, row := range rows[1:] {
		report.DataQuality.Rows++
		value := func(name string) string {
			index := header[name]
			if index >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[index])
		}
		for name, index := range header {
			if index >= len(row) || strings.TrimSpace(row[index]) == "" {
				report.DataQuality.MissingByColumn[name]++
			}
		}
		id := value("id")
		if ids[id] {
			report.DataQuality.DuplicateIDs++
		}
		ids[id] = true
		label := value("label")
		report.DataQuality.LabelCounts[label]++
		source := filepath.Base(value("source_file"))
		report.DataQuality.SourceCounts[source]++
		if parsed, ok := parseEvalTimestamp(value("timestamp")); ok {
			report.DataQuality.ParsedTimestamps++
			if timestampMin.IsZero() || parsed.Before(timestampMin) {
				timestampMin = parsed
			}
			if timestampMax.IsZero() || parsed.After(timestampMax) {
				timestampMax = parsed
			}
		}

		className := value("class_name")
		method := value("method")
		stacktrace := value("exception")
		if className != "" && method != "" {
			stacktrace = "at " + className + "." + method + "(Unknown Source)\n" + stacktrace
		}
		event := Event{ProductLine: productLine, Service: strings.TrimSuffix(source, filepath.Ext(source)),
			Logger: className, ExceptionType: "CoraEvaluationError", Message: value("message"), Stacktrace: stacktrace}
		fingerprint := Fingerprint(event)
		decision, err := core.Decide(ctx, DecisionRequest{Event: event, Fingerprint: fingerprint, FirstOccurrence: true, OccurrenceCount: 1})
		if err != nil {
			return ShadowEvalReport{}, fmt.Errorf("evaluate row %q: %w", id, err)
		}
		report.RowMetrics.DecisionCounts[decision.Decision]++
		if report.RowMetrics.Confusion[label] == nil {
			report.RowMetrics.Confusion[label] = make(map[string]int)
		}
		report.RowMetrics.Confusion[label][decision.Decision]++
		if label != DecisionAttention && label != DecisionObserve && label != DecisionIgnore {
			report.RowMetrics.UnsupportedLabelRows++
		}

		state := signatures[fingerprint]
		if state == nil {
			state = &evalSignatureState{labels: make(map[string]bool), decisions: make(map[string]bool)}
			signatures[fingerprint] = state
		}
		state.labels[label] = true
		state.decisions[decision.Decision] = true

		matched := 0
		for _, rule := range pack.Rules {
			if rule.Match.matches(event) {
				matched++
			}
		}
		if matched > 1 {
			report.RuleConflicts.RowsMatchingMultipleRules++
		}
		if matched > report.RuleConflicts.MaxRulesMatched {
			report.RuleConflicts.MaxRulesMatched = matched
		}
		if label != decision.Decision && len(report.Disagreements) < 20 {
			report.Disagreements = append(report.Disagreements, EvalDisagreement{
				Fingerprint: fingerprint, SourceFile: source, LineNumber: value("line_number"),
				Label: label, Decision: decision.Decision, RuleID: decision.RuleID, Category: decision.Category,
			})
		}
		if label != decision.Decision {
			report.RowMetrics.TransitionByRule[label+"->"+decision.Decision+"|"+decision.RuleID]++
		}
	}

	report.DataQuality.TimestampCoverage = ratio(report.DataQuality.ParsedTimestamps, report.DataQuality.Rows)
	if report.DataQuality.ParsedTimestamps == report.DataQuality.Rows && report.DataQuality.Rows > 1 {
		report.DataQuality.TimeSplitAvailable = true
		report.DataQuality.TimestampMin = timestampMin.Format(time.RFC3339)
		report.DataQuality.TimestampMax = timestampMax.Format(time.RFC3339)
	} else {
		report.DataQuality.TimeSplitUnavailableBy = "timestamps are incomplete or not full dates; row order is not a valid temporal substitute"
	}

	attentionTotal := report.DataQuality.LabelCounts[DecisionAttention]
	attentionCorrect := report.RowMetrics.Confusion[DecisionAttention][DecisionAttention]
	report.RowMetrics.AttentionRecall = ratio(attentionCorrect, attentionTotal)
	report.RowMetrics.AttentionToIgnore = report.RowMetrics.Confusion[DecisionAttention][DecisionIgnore]
	report.RowMetrics.IgnoreToAttention = report.RowMetrics.Confusion[DecisionIgnore][DecisionAttention]
	report.RowMetrics.DecidedRows = report.RowMetrics.DecisionCounts[DecisionAttention] + report.RowMetrics.DecisionCounts[DecisionIgnore]
	report.RowMetrics.Coverage = ratio(report.RowMetrics.DecidedRows, report.DataQuality.Rows)
	agreements := report.RowMetrics.Confusion[DecisionAttention][DecisionAttention] + report.RowMetrics.Confusion[DecisionIgnore][DecisionIgnore]
	report.RowMetrics.AgreementOnDecided = ratio(agreements, report.RowMetrics.DecidedRows)

	report.Signature.Unique = len(signatures)
	report.Signature.DuplicateRows = report.DataQuality.Rows - len(signatures)
	report.Signature.DuplicateRate = ratio(report.Signature.DuplicateRows, report.DataQuality.Rows)
	stableAgreements := 0
	for _, state := range signatures {
		if len(state.labels) > 1 {
			report.Signature.LabelConflictSignatures++
		}
		if len(state.decisions) > 1 {
			report.Signature.DecisionConflicts++
		}
		if len(state.labels) != 1 || len(state.decisions) != 1 {
			continue
		}
		report.Signature.StableSignatures++
		label := onlyKey(state.labels)
		decision := onlyKey(state.decisions)
		if decision == DecisionObserve {
			continue
		}
		report.Signature.StableDecided++
		if label == decision {
			stableAgreements++
		}
	}
	report.Signature.StableAgreement = ratio(stableAgreements, report.Signature.StableDecided)
	return report, nil
}

func parseEvalTimestamp(value string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.000", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func onlyKey(values map[string]bool) string {
	for value := range values {
		return value
	}
	return ""
}

func WriteShadowEvalMarkdown(writer io.Writer, report ShadowEvalReport) error {
	percent := func(value float64) string { return fmt.Sprintf("%.1f%%", value*100) }
	_, err := fmt.Fprintf(writer, `# Cora Shadow Evaluation

Source hash: `+"`%s`"+`
Model: `+"`%s@%s`"+`
Experience: `+"`%s`"+`

## Dataset and grain

- Rows: %d
- Unique Cora fingerprints: %d
- Duplicate fingerprint rows: %d (%s)
- Labels: attention=%d, observe=%d, ignore=%d
- Sources: %d
- Parsed full timestamps: %d/%d (%s)
- Exception/stack populated: %d/%d (%s)
- Time split available: %t

## Row-level shadow results

| Metric | Value |
| --- | ---: |
| attention decisions | %d |
| observe decisions | %d |
| ignore decisions | %d |
| decisive coverage | %s |
| agreement among decisive rows | %s |
| attention recall | %s |
| old attention downgraded to ignore | %d |
| old ignore promoted to attention | %d |

## Signature and rule quality

- Signatures with conflicting historical labels: %d
- Signatures whose Cora decision changes across samples: %d
- Stable decisive signatures: %d
- Agreement on stable decisive signatures: %s
- Rows matching more than one rule: %d
- Maximum rules matched by one row: %d

## Data-quality judgment

`, report.SourceSHA256, report.Model, report.ModelVersion, report.ExperienceVersion,
		report.DataQuality.Rows, report.Signature.Unique,
		report.Signature.DuplicateRows, percent(report.Signature.DuplicateRate),
		report.DataQuality.LabelCounts[DecisionAttention], report.DataQuality.LabelCounts[DecisionObserve],
		report.DataQuality.LabelCounts[DecisionIgnore],
		len(report.DataQuality.SourceCounts), report.DataQuality.ParsedTimestamps, report.DataQuality.Rows,
		percent(report.DataQuality.TimestampCoverage),
		report.DataQuality.Rows-report.DataQuality.MissingByColumn["exception"], report.DataQuality.Rows,
		percent(ratio(report.DataQuality.Rows-report.DataQuality.MissingByColumn["exception"], report.DataQuality.Rows)),
		report.DataQuality.TimeSplitAvailable,
		report.RowMetrics.DecisionCounts[DecisionAttention], report.RowMetrics.DecisionCounts[DecisionObserve],
		report.RowMetrics.DecisionCounts[DecisionIgnore], percent(report.RowMetrics.Coverage),
		percent(report.RowMetrics.AgreementOnDecided), percent(report.RowMetrics.AttentionRecall),
		report.RowMetrics.AttentionToIgnore, report.RowMetrics.IgnoreToAttention,
		report.Signature.LabelConflictSignatures, report.Signature.DecisionConflicts,
		report.Signature.StableDecided, percent(report.Signature.StableAgreement),
		report.RuleConflicts.RowsMatchingMultipleRules, report.RuleConflicts.MaxRulesMatched)
	if err != nil {
		return err
	}
	if !report.DataQuality.TimeSplitAvailable {
		if _, err := fmt.Fprintf(writer, "- **High:** Time-based validation is blocked: %s.\n", report.DataQuality.TimeSplitUnavailableBy); err != nil {
			return err
		}
	}
	if report.DataQuality.LenientCSVParsing {
		if _, err := fmt.Fprintf(writer, "- **High:** Strict CSV parsing failed and compatibility parsing was required: %s.\n", report.DataQuality.CSVParseWarning); err != nil {
			return err
		}
	}
	if report.Signature.DuplicateRate > 0.2 {
		if _, err := fmt.Fprintf(writer, "- **High:** Row-random model evaluation is leakage-prone because %s of rows repeat an existing Cora fingerprint.\n", percent(report.Signature.DuplicateRate)); err != nil {
			return err
		}
	}
	if ratio(report.DataQuality.MissingByColumn["exception"], report.DataQuality.Rows) > 0.5 {
		if _, err := fmt.Fprintf(writer, "- **High:** Exception/stack data is missing on %s of rows, so production fingerprint fidelity and exception-based rule coverage cannot be validated.\n", percent(ratio(report.DataQuality.MissingByColumn["exception"], report.DataQuality.Rows))); err != nil {
			return err
		}
	}
	if report.Signature.DecisionConflicts > 0 {
		if _, err := fmt.Fprintf(writer, "- **Medium:** %d fingerprints change Cora decision across representative samples; latest-sample reevaluation can change queue state.\n", report.Signature.DecisionConflicts); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(writer, `
## Decision transitions by rule

| Transition | Rule | Rows |
| --- | --- | ---: |
`)
	if err != nil {
		return err
	}
	type transitionCount struct {
		key   string
		count int
	}
	transitions := make([]transitionCount, 0, len(report.RowMetrics.TransitionByRule))
	for key, count := range report.RowMetrics.TransitionByRule {
		transitions = append(transitions, transitionCount{key: key, count: count})
	}
	sort.Slice(transitions, func(i, j int) bool {
		if transitions[i].count == transitions[j].count {
			return transitions[i].key < transitions[j].key
		}
		return transitions[i].count > transitions[j].count
	})
	for _, transition := range transitions {
		parts := strings.SplitN(transition.key, "|", 2)
		if _, err := fmt.Fprintf(writer, "| %s | %s | %d |\n", parts[0], parts[1], transition.count); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(writer, `
## Redacted disagreement samples

No raw message, exception, trace ID, or payload is included.

| Fingerprint | Source | Line | Old label | Cora | Rule |
| --- | --- | ---: | --- | --- | --- |
`)
	if err != nil {
		return err
	}
	for _, sample := range report.Disagreements {
		if _, err := fmt.Fprintf(writer, "| `%s` | %s | %s | %s | %s | %s |\n",
			sample.Fingerprint, sample.SourceFile, sample.LineNumber, sample.Label, sample.Decision, sample.RuleID); err != nil {
			return err
		}
	}
	return nil
}
