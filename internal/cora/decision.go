package cora

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	DecisionAttention = "attention"
	DecisionObserve   = "observe"
	DecisionIgnore    = "ignore"
	TraceRoleUnknown  = "unknown"
	TraceRoleWrapper  = "wrapper"
	TraceRoleCause    = "cause"
)

// DecisionRequest is the stable Cora Server/Core boundary. A future external
// Core can implement the same contract without changing ingestion.
type DecisionRequest struct {
	Event           Event  `json:"event"`
	Fingerprint     string `json:"fingerprint"`
	OccurrenceCount int64  `json:"occurrence_count"`
	FirstOccurrence bool   `json:"first_occurrence"`
}

type CoraDecision struct {
	Decision          string    `json:"decision"`
	RootCauseKey      string    `json:"root_cause_key"`
	Category          string    `json:"category"`
	RuleID            string    `json:"rule_id"`
	Reason            string    `json:"reason"`
	Source            string    `json:"source"`
	ExperienceVersion string    `json:"experience_version,omitempty"`
	TraceRole         string    `json:"trace_role,omitempty"`
	DecidedAt         time.Time `json:"decided_at"`
}

type AttentionItem struct {
	ProblemID            int64                     `json:"problem_id"`
	ProductLine          string                    `json:"product_line"`
	Fingerprint          string                    `json:"fingerprint"`
	Service              string                    `json:"service"`
	Environment          string                    `json:"environment"`
	ExceptionType        string                    `json:"exception_type"`
	Logger               string                    `json:"logger"`
	Count                int64                     `json:"count"`
	LastSeen             time.Time                 `json:"last_seen"`
	State                string                    `json:"state"`
	StateChangedAt       time.Time                 `json:"state_changed_at"`
	Decision             string                    `json:"decision"`
	RootCauseKey         string                    `json:"root_cause_key"`
	Category             string                    `json:"category"`
	RuleID               string                    `json:"rule_id"`
	Reason               string                    `json:"reason"`
	Source               string                    `json:"source"`
	ExperienceVersion    string                    `json:"experience_version,omitempty"`
	DecidedAt            time.Time                 `json:"decided_at"`
	IncidentKey          string                    `json:"incident_key,omitempty"`
	IncidentProblemCount int                       `json:"incident_problem_count,omitempty"`
	RelatedCount         int                       `json:"related_problem_count,omitempty"`
	IncidentServices     []string                  `json:"incident_services,omitempty"`
	SharedTraceIDs       []string                  `json:"shared_trace_ids,omitempty"`
	RelatedProblems      []AttentionRelatedProblem `json:"related_problems,omitempty"`
	TraceProjectionMode  string                    `json:"trace_projection_mode,omitempty"`
	TraceProjection      *TraceProjectionSummary   `json:"trace_projection,omitempty"`
}

type TraceProjectionSummary struct {
	Projected   int               `json:"projected"`
	Ambiguous   int               `json:"ambiguous"`
	Unresolved  int               `json:"unresolved"`
	Projections []TraceProjection `json:"projections,omitempty"`
}

type TraceProjection struct {
	TraceID             string    `json:"trace_id"`
	Status              string    `json:"status"`
	WrapperProblemIDs   []int64   `json:"wrapper_problem_ids"`
	ProjectedDecision   string    `json:"projected_decision,omitempty"`
	ProjectedRootCause  string    `json:"projected_root_cause_key,omitempty"`
	CauseProblemIDs     []int64   `json:"cause_problem_ids,omitempty"`
	CauseServices       []string  `json:"cause_services,omitempty"`
	CauseRuleIDs        []string  `json:"cause_rule_ids,omitempty"`
	CandidateRootCauses []string  `json:"candidate_root_cause_keys,omitempty"`
	LastSeen            time.Time `json:"last_seen"`
}

type AttentionRelatedProblem struct {
	ProblemID      int64     `json:"problem_id"`
	Service        string    `json:"service"`
	Fingerprint    string    `json:"fingerprint"`
	State          string    `json:"state"`
	Decision       string    `json:"decision"`
	Category       string    `json:"category"`
	RootCauseKey   string    `json:"root_cause_key"`
	RelationKinds  []string  `json:"relation_kinds"`
	Count          int64     `json:"count"`
	LastSeen       time.Time `json:"last_seen"`
	SharedTraceIDs []string  `json:"shared_trace_ids"`
}

type Cora interface {
	Decide(context.Context, DecisionRequest) (CoraDecision, error)
}

// RootCauseClassifier is an optional fast path used before window aggregation.
// Implementations must return a deterministic key independent of service, node,
// occurrence count, and wall-clock time.
type RootCauseClassifier interface {
	ClassifyRootCause(context.Context, DecisionRequest) (string, error)
}

type experiencePack struct {
	SchemaVersion   string           `json:"schema_version"`
	ProductLine     string           `json:"product_line"`
	Version         string           `json:"version"`
	DefaultDecision string           `json:"default_decision"`
	PriorityOrder   []string         `json:"priority_order"`
	Rules           []experienceRule `json:"rules"`
}

type experienceRule struct {
	ID           string      `json:"id"`
	Decision     string      `json:"decision"`
	RootCauseKey string      `json:"root_cause_key,omitempty"`
	Category     string      `json:"category"`
	Reason       string      `json:"reason"`
	TraceRole    string      `json:"trace_role,omitempty"`
	Match        ruleMatcher `json:"match"`
}

type ruleMatcher struct {
	Class                            string     `json:"class"`
	ClassContains                    stringList `json:"class_contains"`
	Method                           string     `json:"method"`
	MessageContains                  stringList `json:"message_contains"`
	Exception                        string     `json:"exception"`
	ExceptionContains                stringList `json:"exception_contains"`
	ExcludeExceptionContains         stringList `json:"exclude_exception_contains"`
	BreadcrumbMessageContains        stringList `json:"breadcrumb_message_contains"`
	ExcludeBreadcrumbMessageContains stringList `json:"exclude_breadcrumb_message_contains"`
}

type stringList []string

func (items *stringList) UnmarshalJSON(data []byte) error {
	var many []string
	if err := json.Unmarshal(data, &many); err == nil {
		*items = many
		return nil
	}
	var one string
	if err := json.Unmarshal(data, &one); err != nil {
		return errors.New("rule match value must be a string or string array")
	}
	*items = []string{one}
	return nil
}

type ruleCora struct{ packs map[string]experiencePack }

func defaultCoraCore() (Cora, error) {
	return &ruleCora{packs: make(map[string]experiencePack)}, nil
}

// LoadExperiencePacks loads product-specific rules from an explicit directory.
// Public Cora binaries contain no product packs; deployments opt in to private
// packs through configuration.
func LoadExperiencePacks(directory string) (Cora, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return defaultCoraCore()
	}
	return loadRuleCora(os.DirFS(directory))
}

func loadRuleCora(files fs.FS) (Cora, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, fmt.Errorf("read Cora experience packs: %w", err)
	}
	core := &ruleCora{packs: make(map[string]experiencePack)}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := fs.ReadFile(files, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read experience pack %s: %w", entry.Name(), err)
		}
		var pack experiencePack
		if err := json.Unmarshal(data, &pack); err != nil {
			return nil, fmt.Errorf("parse experience pack %s: %w", entry.Name(), err)
		}
		if err := validateExperiencePack(pack); err != nil {
			return nil, fmt.Errorf("invalid experience pack %s: %w", entry.Name(), err)
		}
		if _, exists := core.packs[pack.ProductLine]; exists {
			return nil, fmt.Errorf("duplicate experience pack for %s", pack.ProductLine)
		}
		priority := make(map[string]int, len(pack.PriorityOrder))
		for index, decision := range pack.PriorityOrder {
			priority[decision] = index
		}
		sort.SliceStable(pack.Rules, func(i, j int) bool {
			return priority[pack.Rules[i].Decision] < priority[pack.Rules[j].Decision]
		})
		core.packs[pack.ProductLine] = pack
	}
	return core, nil
}

func validateExperiencePack(pack experiencePack) error {
	if pack.SchemaVersion != "cora.experience-pack.v1" || pack.ProductLine == "" || pack.Version == "" {
		return errors.New("schema_version, product_line, and version are required")
	}
	if !validDecision(pack.DefaultDecision) {
		return fmt.Errorf("invalid default decision %q", pack.DefaultDecision)
	}
	priority := make(map[string]bool, len(pack.PriorityOrder))
	for _, decision := range pack.PriorityOrder {
		if !validDecision(decision) {
			return fmt.Errorf("invalid priority decision %q", decision)
		}
		priority[decision] = true
	}
	for _, rule := range pack.Rules {
		if rule.ID == "" || !validDecision(rule.Decision) || !priority[rule.Decision] {
			return fmt.Errorf("rule %q has invalid id, decision, or priority", rule.ID)
		}
		if strings.ContainsAny(rule.RootCauseKey, " \t\r\n") {
			return fmt.Errorf("rule %q has invalid root_cause_key", rule.ID)
		}
		if rule.TraceRole != "" && rule.TraceRole != TraceRoleUnknown && rule.TraceRole != TraceRoleWrapper && rule.TraceRole != TraceRoleCause {
			return fmt.Errorf("rule %q has invalid trace_role", rule.ID)
		}
	}
	return nil
}

func validDecision(decision string) bool {
	return decision == DecisionAttention || decision == DecisionObserve || decision == DecisionIgnore
}

func (c *ruleCora) Decide(_ context.Context, request DecisionRequest) (CoraDecision, error) {
	now := time.Now().UTC()
	derivedKey := derivedRootCauseKey(request.Event, request.Fingerprint)
	pack, exists := c.packs[request.Event.ProductLine]
	if !exists {
		return CoraDecision{
			Decision: DecisionObserve, Category: "untrained-product-line",
			RootCauseKey: derivedKey,
			RuleID:       "cora.default.untrained-product-line",
			Reason:       "no product-line experience pack; keep visible for review",
			Source:       "framework_default", DecidedAt: now,
		}, nil
	}
	for _, rule := range pack.Rules {
		if rule.Match.matches(request.Event) {
			rootCauseKey := rule.RootCauseKey
			if rootCauseKey == "" {
				rootCauseKey = derivedKey
			}
			return CoraDecision{
				Decision: rule.Decision, Category: rule.Category, RuleID: rule.ID,
				RootCauseKey: rootCauseKey,
				Reason:       rule.Reason, Source: "experience_pack",
				ExperienceVersion: pack.Version, TraceRole: normalizedTraceRole(rule.TraceRole), DecidedAt: now,
			}, nil
		}
	}
	return CoraDecision{
		Decision: pack.DefaultDecision, Category: "unmatched",
		RootCauseKey: derivedKey,
		RuleID:       "cora.default.unmatched", Reason: "no stable product-line rule matched",
		Source: "experience_pack", ExperienceVersion: pack.Version, TraceRole: TraceRoleUnknown, DecidedAt: now,
	}, nil
}

func normalizedTraceRole(role string) string {
	if role == "" {
		return TraceRoleUnknown
	}
	return role
}

func (c *ruleCora) ClassifyRootCause(ctx context.Context, request DecisionRequest) (string, error) {
	decision, err := c.Decide(ctx, request)
	if err != nil {
		return "", err
	}
	return decision.RootCauseKey, nil
}

func derivedRootCauseKey(event Event, fingerprint string) string {
	message := numberPattern.ReplaceAllString(uuidPattern.ReplaceAllString(event.Message, "<uuid>"), "<n>")
	message = strings.Join(strings.Fields(message), " ")
	if message == "" {
		if fingerprint == "" {
			fingerprint = Fingerprint(event)
		}
		return "fingerprint:" + fingerprint
	}
	sum := sha256.Sum256([]byte(message))
	return "message:" + hex.EncodeToString(sum[:12])
}

func (match ruleMatcher) matches(event Event) bool {
	classText := event.Logger + "\n" + event.Stacktrace
	methodText := event.Method + "\n" + event.Stacktrace
	exceptionText := event.ExceptionType + "\n" + event.Stacktrace
	var breadcrumbText strings.Builder
	for _, breadcrumb := range event.Breadcrumbs {
		breadcrumbText.WriteString(breadcrumb.Message)
		breadcrumbText.WriteByte('\n')
	}
	return containsIfSet(classText, match.Class) &&
		containsAnyIfSet(classText, match.ClassContains) &&
		containsIfSet(methodText, match.Method) &&
		containsAnyIfSet(event.Message, match.MessageContains) &&
		containsIfSet(exceptionText, match.Exception) &&
		containsAnyIfSet(exceptionText, match.ExceptionContains) &&
		containsNoneIfSet(exceptionText, match.ExcludeExceptionContains) &&
		containsAnyIfSet(breadcrumbText.String(), match.BreadcrumbMessageContains) &&
		containsNoneIfSet(breadcrumbText.String(), match.ExcludeBreadcrumbMessageContains)
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

func containsNoneIfSet(text string, patterns []string) bool {
	for _, pattern := range patterns {
		if strings.Contains(text, pattern) {
			return false
		}
	}
	return true
}
