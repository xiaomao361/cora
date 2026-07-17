package iteration

import (
	"context"
	"time"

	"github.com/claracore/cora/internal/buildinfo"
	"github.com/claracore/cora/internal/cora"
)

const (
	RunSchemaVersion       = "cora.iteration-run.v1"
	CandidateSchemaVersion = "cora.rule-candidate.v1"
)

type ServerSnapshot struct {
	Build   buildinfo.Info   `json:"build"`
	Storage cora.StoreHealth `json:"storage"`
}

type Source interface {
	Health(context.Context) (ServerSnapshot, error)
	Attention(context.Context, string, int) ([]cora.AttentionItem, error)
	IterationSnapshot(context.Context, string, string, string, int, int) (cora.IterationSnapshot, error)
	Problem(context.Context, string, string, string, string) (cora.ProblemDetail, error)
	ExportCases(context.Context, string, int64, int64, int) (cora.CaseExportPage, error)
	Close() error
}

type Config struct {
	ProductLine             string
	BusinessDate            string
	Location                *time.Location
	OutputRoot              string
	RunID                   string
	PackManifestPath        string
	CodeEvidencePath        string
	PageSize                int
	AttentionLimit          int
	BaselineDays            int
	FrequencyMinimum        int64
	FrequencyRatioThreshold float64
	Now                     func() time.Time
}

type Run struct {
	SchemaVersion     string       `json:"schema_version"`
	IterationRunID    string       `json:"iteration_run_id"`
	ProductLine       string       `json:"product_line"`
	BusinessDate      string       `json:"business_date"`
	InputWindow       InputWindow  `json:"input_window"`
	CreatedAt         time.Time    `json:"created_at"`
	CompletedAt       time.Time    `json:"completed_at"`
	Status            string       `json:"status"`
	CoraBuild         RunBuild     `json:"cora_build"`
	Pack              PackIdentity `json:"pack"`
	CaseSnapshot      CaseSnapshot `json:"case_snapshot"`
	AttentionSnapshot Artifact     `json:"attention_snapshot"`
	Artifacts         []Artifact   `json:"artifacts"`
}

type InputWindow struct {
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Timezone string    `json:"timezone"`
}

type RunBuild struct {
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	BuildTime     string `json:"build_time,omitempty"`
	GoVersion     string `json:"go_version,omitempty"`
	SchemaVersion int    `json:"schema_version"`
}

type PackIdentity struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

type CaseSnapshot struct {
	SnapshotID   string             `json:"snapshot_id"`
	ThroughID    int64              `json:"through_case_id"`
	Pages        []CaseSnapshotPage `json:"pages"`
	ManifestHash string             `json:"manifest_sha256"`
	VerifiedAt   time.Time          `json:"verified_at"`
}

type CaseSnapshotPage struct {
	AfterCaseID     int64  `json:"after_case_id"`
	NextAfterCaseID int64  `json:"next_after_case_id"`
	CaseCount       int    `json:"case_count"`
	PageSHA256      string `json:"page_sha256"`
}

type Artifact struct {
	Path      string    `json:"path"`
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"created_at"`
	Role      string    `json:"role,omitempty"`
}

type AttentionSnapshot struct {
	SchemaVersion       string                `json:"schema_version"`
	ProductLine         string                `json:"product_line"`
	BusinessDate        string                `json:"business_date"`
	Window              InputWindow           `json:"window"`
	CapturedAt          time.Time             `json:"captured_at"`
	CurrentIncidents    []cora.AttentionItem  `json:"current_incidents"`
	DailyProblems       []DailyProblem        `json:"daily_problems"`
	FrequencyEscalation []FrequencyEscalation `json:"frequency_escalations"`
	Summary             DailySummary          `json:"summary"`
}

type DailyProblem struct {
	ProblemID          int64               `json:"problem_id"`
	ProductLine        string              `json:"product_line"`
	Service            string              `json:"service"`
	Fingerprint        string              `json:"fingerprint"`
	RootCauseKey       string              `json:"root_cause_key"`
	State              string              `json:"state"`
	Decision           string              `json:"decision"`
	Category           string              `json:"category"`
	Reason             string              `json:"reason"`
	RuleID             string              `json:"rule_id"`
	WindowCount        int64               `json:"window_count"`
	PriorDailyAverage  float64             `json:"prior_daily_average"`
	FrequencyRatio     *float64            `json:"frequency_ratio,omitempty"`
	NodeCounts         []NodeCount         `json:"node_counts"`
	RelatedProblemKeys []string            `json:"related_problem_keys"`
	CaseIDs            []int64             `json:"case_ids"`
	FirstSeen          time.Time           `json:"first_seen"`
	LastSeen           time.Time           `json:"last_seen"`
	Detail             *cora.ProblemDetail `json:"-"`
}

type NodeCount struct {
	Node            string `json:"node"`
	DeploymentGroup string `json:"deployment_group,omitempty"`
	Count           int64  `json:"count"`
}

type FrequencyEscalation struct {
	Service           string   `json:"service"`
	Fingerprint       string   `json:"fingerprint"`
	RootCauseKey      string   `json:"root_cause_key"`
	RuleID            string   `json:"rule_id"`
	WindowCount       int64    `json:"window_count"`
	PriorDailyAverage float64  `json:"prior_daily_average"`
	FrequencyRatio    *float64 `json:"frequency_ratio,omitempty"`
	BaselineDays      int      `json:"baseline_days"`
	Reason            string   `json:"reason"`
	RecommendedReview string   `json:"recommended_review"`
}

type DailySummary struct {
	ProblemCount              int              `json:"problem_count"`
	OccurrenceCount           int64            `json:"occurrence_count"`
	DecisionProblemCounts     map[string]int   `json:"decision_problem_counts"`
	DecisionOccurrenceCounts  map[string]int64 `json:"decision_occurrence_counts"`
	FrequencyEscalationCount  int              `json:"frequency_escalation_count"`
	CurrentAttentionIncidents int              `json:"current_attention_incidents"`
}

type TriageResult struct {
	SchemaVersion       string   `json:"schema_version"`
	IterationRunID      string   `json:"iteration_run_id"`
	ProductLine         string   `json:"product_line"`
	Service             string   `json:"service"`
	Fingerprint         string   `json:"fingerprint"`
	RootCauseKey        string   `json:"root_cause_key"`
	Classification      string   `json:"classification"`
	WindowCount         int64    `json:"window_count"`
	PriorDailyAverage   float64  `json:"prior_daily_average"`
	FrequencyRatio      *float64 `json:"frequency_ratio,omitempty"`
	CurrentDecision     string   `json:"current_decision"`
	CurrentRuleID       string   `json:"current_rule_id"`
	CaseIDs             []int64  `json:"case_ids"`
	CodeEvidenceStatus  string   `json:"code_evidence_status"`
	ReviewStatus        string   `json:"review_status"`
	RecommendedNextStep string   `json:"recommended_next_step"`
}

type CodeEvidence struct {
	SchemaVersion string    `json:"schema_version"`
	EvidenceID    string    `json:"evidence_id"`
	ProductLine   string    `json:"product_line"`
	Service       string    `json:"service"`
	Fingerprint   string    `json:"fingerprint"`
	RootCauseKey  string    `json:"root_cause_key,omitempty"`
	Source        string    `json:"source"`
	Status        string    `json:"status"`
	Summary       string    `json:"summary"`
	References    []string  `json:"references"`
	CollectedAt   time.Time `json:"collected_at"`
}

type RuleCandidate struct {
	SchemaVersion  string            `json:"schema_version"`
	CandidateID    string            `json:"candidate_id"`
	IterationRunID string            `json:"iteration_run_id"`
	ProductLine    string            `json:"product_line"`
	RuleID         string            `json:"rule_id"`
	Decision       string            `json:"decision"`
	Category       string            `json:"category"`
	Reason         string            `json:"reason"`
	Match          RuleMatch         `json:"match"`
	SourceCaseIDs  []int64           `json:"source_case_ids"`
	Evidence       CandidateEvidence `json:"evidence"`
	Risk           CandidateRisk     `json:"risk"`
	Status         string            `json:"status"`
	CreatedAt      time.Time         `json:"created_at"`
}

type RuleMatch struct {
	Class             string   `json:"class,omitempty"`
	ClassContains     []string `json:"class_contains,omitempty"`
	Method            string   `json:"method,omitempty"`
	MessageContains   []string `json:"message_contains,omitempty"`
	Exception         string   `json:"exception,omitempty"`
	ExceptionContains []string `json:"exception_contains,omitempty"`
}

type CandidateEvidence struct {
	CaseSnapshotID    string   `json:"case_snapshot_id"`
	EvalRunID         string   `json:"eval_run_id"`
	BaselineDecision  string   `json:"baseline_decision"`
	CandidateDecision string   `json:"candidate_decision"`
	Summary           string   `json:"summary"`
	CodeEvidenceIDs   []string `json:"code_evidence_ids,omitempty"`
}

type CandidateRisk struct {
	FalsePositive               string `json:"false_positive"`
	FalseNegative               string `json:"false_negative"`
	FrequencyEscalationReviewed bool   `json:"frequency_escalation_reviewed"`
}

type ShadowEval struct {
	SchemaVersion          string                `json:"schema_version"`
	IterationRunID         string                `json:"iteration_run_id"`
	ProductLine            string                `json:"product_line"`
	CaseSnapshotID         string                `json:"case_snapshot_id"`
	CandidateCount         int                   `json:"candidate_count"`
	DailyProblems          int                   `json:"daily_problems"`
	DailyOccurrences       int64                 `json:"daily_occurrences"`
	ProblemTransitions     map[string]int        `json:"problem_transitions"`
	OccurrenceTransitions  map[string]int64      `json:"occurrence_transitions"`
	KnownRealProblemRecall RecallMetric          `json:"known_real_problem_recall"`
	KnownNoiseEscalated    int                   `json:"known_noise_escalated"`
	FrequencyEscalations   []FrequencyEscalation `json:"frequency_escalations"`
	ErrorIdentities        []ErrorIdentity       `json:"error_identities"`
	CandidateMatches       map[string][]string   `json:"candidate_matches"`
	Notes                  []string              `json:"notes"`
}

type ErrorIdentity struct {
	ProblemID      int64    `json:"problem_id"`
	RuleID         string   `json:"rule_id"`
	Name           string   `json:"name"`
	Reason         string   `json:"reason"`
	Signature      string   `json:"signature"`
	Service        string   `json:"service"`
	Fingerprint    string   `json:"fingerprint"`
	RootCauseKey   string   `json:"root_cause_key"`
	Decision       string   `json:"decision"`
	WindowCount    int64    `json:"window_count"`
	RelatedRuleIDs []string `json:"related_rule_ids"`
}

type RecallMetric struct {
	Total     int     `json:"total"`
	Attention int     `json:"attention"`
	Recall    float64 `json:"recall"`
}
