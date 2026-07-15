package cora

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	DecisionAttention = "attention"
	DecisionObserve   = "observe"
	DecisionIgnore    = "ignore"
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
	Category          string    `json:"category"`
	RuleID            string    `json:"rule_id"`
	Reason            string    `json:"reason"`
	Source            string    `json:"source"`
	ExperienceVersion string    `json:"experience_version,omitempty"`
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
}

type AttentionRelatedProblem struct {
	ProblemID      int64     `json:"problem_id"`
	Service        string    `json:"service"`
	Fingerprint    string    `json:"fingerprint"`
	State          string    `json:"state"`
	Decision       string    `json:"decision"`
	Category       string    `json:"category"`
	Count          int64     `json:"count"`
	LastSeen       time.Time `json:"last_seen"`
	SharedTraceIDs []string  `json:"shared_trace_ids"`
}

type Cora interface {
	Decide(context.Context, DecisionRequest) (CoraDecision, error)
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
	ID       string      `json:"id"`
	Decision string      `json:"decision"`
	Category string      `json:"category"`
	Reason   string      `json:"reason"`
	Match    ruleMatcher `json:"match"`
}

type ruleMatcher struct {
	Class                            string     `json:"class"`
	ClassContains                    stringList `json:"class_contains"`
	Method                           string     `json:"method"`
	MessageContains                  stringList `json:"message_contains"`
	Exception                        string     `json:"exception"`
	ExceptionContains                stringList `json:"exception_contains"`
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

//go:embed experience/*.json
var embeddedExperiencePacks embed.FS

var (
	defaultCoraOnce sync.Once
	defaultCora     Cora
	defaultCoraErr  error
)

func defaultCoraCore() (Cora, error) {
	defaultCoraOnce.Do(func() {
		defaultCora, defaultCoraErr = loadRuleCora(embeddedExperiencePacks)
	})
	return defaultCora, defaultCoraErr
}

func loadRuleCora(files embed.FS) (Cora, error) {
	entries, err := files.ReadDir("experience")
	if err != nil {
		return nil, fmt.Errorf("read Cora experience packs: %w", err)
	}
	core := &ruleCora{packs: make(map[string]experiencePack)}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := files.ReadFile("experience/" + entry.Name())
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
	}
	return nil
}

func validDecision(decision string) bool {
	return decision == DecisionAttention || decision == DecisionObserve || decision == DecisionIgnore
}

func (c *ruleCora) Decide(_ context.Context, request DecisionRequest) (CoraDecision, error) {
	now := time.Now().UTC()
	pack, exists := c.packs[request.Event.ProductLine]
	if !exists {
		return CoraDecision{
			Decision: DecisionObserve, Category: "untrained-product-line",
			RuleID: "cora.default.untrained-product-line",
			Reason: "no product-line experience pack; keep visible for review",
			Source: "framework_default", DecidedAt: now,
		}, nil
	}
	for _, rule := range pack.Rules {
		if rule.Match.matches(request.Event) {
			return CoraDecision{
				Decision: rule.Decision, Category: rule.Category, RuleID: rule.ID,
				Reason: rule.Reason, Source: "experience_pack",
				ExperienceVersion: pack.Version, DecidedAt: now,
			}, nil
		}
	}
	return CoraDecision{
		Decision: pack.DefaultDecision, Category: "unmatched",
		RuleID: "cora.default.unmatched", Reason: "no stable product-line rule matched",
		Source: "experience_pack", ExperienceVersion: pack.Version, DecidedAt: now,
	}, nil
}

func (match ruleMatcher) matches(event Event) bool {
	classText := event.Logger + "\n" + event.Stacktrace
	methodText := event.Stacktrace
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
