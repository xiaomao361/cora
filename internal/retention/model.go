package retention

import "time"

const (
	AuditSchemaVersion = "cora.retention-audit.v1"
	RunSchemaVersion   = "cora.retention-audit-run.v1"
)

type Config struct {
	DatabasePath     string
	ProductLine      string
	CoraBuildVersion string
	CoraSourceDigest string
	IterationRoot    string
	ClosureRoot      string
	OutputRoot       string
	RunID            string
}

type Result struct {
	Directory string `json:"directory"`
	Audit     Audit  `json:"audit"`
	Run       Run    `json:"run"`
}

type Audit struct {
	SchemaVersion     string              `json:"schema_version"`
	AuditRunID        string              `json:"audit_run_id"`
	ProductLine       string              `json:"product_line"`
	CapturedAt        time.Time           `json:"captured_at"`
	Database          DatabaseIdentity    `json:"database"`
	CoraBuild         CoraBuildIdentity   `json:"cora_build"`
	Storage           StorageStats        `json:"storage"`
	Tables            []TableStats        `json:"tables"`
	Breakdowns        Breakdowns          `json:"breakdowns"`
	Summary           Summary             `json:"summary"`
	LogicalRelease    LogicalRelease      `json:"logical_release_estimate"`
	ProblemDecisions  []ProblemDecision   `json:"problem_decisions"`
	UnmatchedReceipts []ReceiptDiagnostic `json:"unmatched_receipts"`
}

type CoraBuildIdentity struct {
	Version      string `json:"version"`
	SourceDigest string `json:"source_digest"`
}

type DatabaseIdentity struct {
	Path          string    `json:"path"`
	SHA256        string    `json:"sha256"`
	SizeBytes     int64     `json:"size_bytes"`
	ModifiedAt    time.Time `json:"modified_at"`
	SchemaVersion int       `json:"schema_version"`
}

type StorageStats struct {
	PageCount             int64  `json:"page_count"`
	PageSizeBytes         int64  `json:"page_size_bytes"`
	FreelistCount         int64  `json:"freelist_count"`
	DatabaseBytesByPages  int64  `json:"database_bytes_by_pages"`
	ReusableFreelistBytes int64  `json:"reusable_freelist_bytes"`
	WALSizeBytes          int64  `json:"wal_size_bytes"`
	SHMSizeBytes          int64  `json:"shm_size_bytes"`
	QuickCheck            string `json:"quick_check"`
}

type TableStats struct {
	Table      string `json:"table"`
	Rows       int64  `json:"rows"`
	EarliestAt string `json:"earliest_at,omitempty"`
	LatestAt   string `json:"latest_at,omitempty"`
}

type Breakdowns struct {
	ByState    []CountBucket `json:"by_state"`
	ByDecision []CountBucket `json:"by_decision"`
	ByHandled  []CountBucket `json:"by_handled"`
	ByCases    []CountBucket `json:"by_case_presence"`
}

type CountBucket struct {
	Key         string `json:"key"`
	Problems    int64  `json:"problems"`
	Occurrences int64  `json:"occurrences"`
}

type Summary struct {
	ProblemCount        int `json:"problem_count"`
	EligibleProblems    int `json:"eligible_problems"`
	IneligibleProblems  int `json:"ineligible_problems"`
	InvalidReceiptFiles int `json:"invalid_receipt_files"`
}

type LogicalRelease struct {
	EstimatedRows  int64  `json:"estimated_rows"`
	EstimatedBytes int64  `json:"estimated_bytes"`
	Method         string `json:"method"`
	PhysicalCaveat string `json:"physical_caveat"`
}

type ProblemDecision struct {
	ProblemID                int64    `json:"problem_id"`
	ProductLine              string   `json:"product_line"`
	Service                  string   `json:"service"`
	Fingerprint              string   `json:"fingerprint"`
	RootCauseKey             string   `json:"root_cause_key"`
	State                    string   `json:"state"`
	Decision                 string   `json:"decision"`
	OccurrenceCount          int64    `json:"occurrence_count"`
	LastSeen                 string   `json:"last_seen"`
	CaseCount                int64    `json:"case_count"`
	HandledCaseCount         int64    `json:"handled_case_count"`
	ClosureReceiptID         string   `json:"closure_receipt_id,omitempty"`
	RetentionEligible        bool     `json:"retention_eligible"`
	BlockingReasons          []string `json:"blocking_reasons"`
	EstimatedReleasableRows  int64    `json:"estimated_releasable_rows"`
	EstimatedReleasableBytes int64    `json:"estimated_releasable_bytes"`
}

type ReceiptDiagnostic struct {
	Path        string   `json:"path"`
	ProductLine string   `json:"product_line,omitempty"`
	Service     string   `json:"service,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	Reasons     []string `json:"reasons"`
}

type Run struct {
	SchemaVersion string            `json:"schema_version"`
	AuditRunID    string            `json:"audit_run_id"`
	ProductLine   string            `json:"product_line"`
	CoraBuild     CoraBuildIdentity `json:"cora_build"`
	CapturedAt    time.Time         `json:"captured_at"`
	Inputs        []Artifact        `json:"inputs"`
	Artifacts     []Artifact        `json:"artifacts"`
}

type Artifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}
