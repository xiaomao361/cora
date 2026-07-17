package cora

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"
)

func TestCheckedInExamplePackLoads(t *testing.T) {
	core, err := LoadExperiencePacks("../../config/experience-packs")
	if err != nil {
		t.Fatal(err)
	}
	pack := core.(*ruleCora).packs["payments"]
	if pack.Version != "payments-example-v1" || len(pack.Rules) != 2 {
		t.Fatalf("pack=%+v", pack)
	}
}

const testExperiencePack = `{
  "schema_version": "cora.experience-pack.v1",
  "product_line": "payments",
  "version": "payments-example-v1",
  "default_decision": "observe",
  "priority_order": ["attention", "observe", "ignore"],
  "rules": [
    {
      "id": "attention.database-unavailable",
      "decision": "attention",
      "category": "database",
      "reason": "database connectivity failures require review",
      "trace_role": "cause",
      "match": {"class": "DatabaseClient"}
    },
    {
      "id": "ignore.client-cancelled",
      "decision": "ignore",
      "root_cause_key": "payments.client-cancelled",
      "category": "client-cancelled",
      "reason": "the caller cancelled before processing completed",
      "trace_role": "cause",
      "match": {"class": "CheckoutHandler", "message_contains": ["client cancelled"]}
    },
    {
      "id": "attention.retry-wrapper",
      "decision": "attention",
      "category": "retry-wrapper",
      "reason": "retry exhaustion is a wrapper until a cause is identified",
      "trace_role": "wrapper",
      "match": {"class": "RetryExecutor", "message_contains": ["retry exhausted"]}
    }
  ]
}`

func testRuleCore(t *testing.T) Cora {
	t.Helper()
	core, err := loadRuleCora(fstest.MapFS{
		"payments.json": &fstest.MapFile{Data: []byte(testExperiencePack)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return core
}

func TestDefaultCoreContainsNoProductSpecificRules(t *testing.T) {
	core, err := defaultCoraCore()
	if err != nil {
		t.Fatal(err)
	}
	decision, err := core.Decide(context.Background(), DecisionRequest{Event: Event{
		ProductLine: "payments", Service: "checkout", ExceptionType: "DatabaseUnavailable",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Decision != DecisionObserve || decision.Source != "framework_default" ||
		decision.RuleID != "cora.default.untrained-product-line" {
		t.Fatalf("decision=%+v", decision)
	}
}

func TestExternalExperiencePackGoldenCases(t *testing.T) {
	core := testRuleCore(t)
	tests := []struct {
		name, wantDecision, wantRule, wantRootCause, wantTraceRole string
		event                                                      Event
	}{
		{name: "database failure", wantDecision: DecisionAttention,
			wantRule: "attention.database-unavailable", wantTraceRole: TraceRoleCause,
			event: Event{ProductLine: "payments", Service: "ledger", Logger: "DatabaseClient", ExceptionType: "DatabaseUnavailable"}},
		{name: "known cancellation", wantDecision: DecisionIgnore,
			wantRule: "ignore.client-cancelled", wantRootCause: "payments.client-cancelled", wantTraceRole: TraceRoleCause,
			event: Event{ProductLine: "payments", Service: "checkout", Logger: "CheckoutHandler", Message: "client cancelled request"}},
		{name: "retry wrapper", wantDecision: DecisionAttention,
			wantRule: "attention.retry-wrapper", wantTraceRole: TraceRoleWrapper,
			event: Event{ProductLine: "payments", Service: "checkout", Logger: "RetryExecutor", Message: "retry exhausted"}},
		{name: "unmatched", wantDecision: DecisionObserve, wantRule: "cora.default.unmatched",
			event: Event{ProductLine: "payments", Service: "checkout", Message: "unexpected response"}},
		{name: "untrained product line", wantDecision: DecisionObserve, wantRule: "cora.default.untrained-product-line",
			event: Event{ProductLine: "orders", Service: "api", ExceptionType: "DatabaseUnavailable"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, err := core.Decide(context.Background(), DecisionRequest{Event: test.event, Fingerprint: Fingerprint(test.event)})
			if err != nil {
				t.Fatal(err)
			}
			if decision.Decision != test.wantDecision || decision.RuleID != test.wantRule ||
				(test.wantRootCause != "" && decision.RootCauseKey != test.wantRootCause) ||
				(test.wantTraceRole != "" && decision.TraceRole != test.wantTraceRole) {
				t.Fatalf("decision=%+v", decision)
			}
		})
	}
}

func TestRootCauseKeyIsStableAcrossServiceAndNodeButSeparatesMessages(t *testing.T) {
	core := testRuleCore(t)
	base := Event{ProductLine: "payments", Service: "checkout", Logger: "CheckoutHandler",
		Message: "client cancelled request 123", Labels: map[string]string{"node": "node-a"}}
	otherService := base
	otherService.Service = "ledger"
	otherService.Labels = map[string]string{"node": "node-b"}
	differentCause := base
	differentCause.Logger = "OtherHandler"
	differentCause.Message = "upstream timed out"
	decide := func(event Event) CoraDecision {
		decision, err := core.Decide(context.Background(), DecisionRequest{Event: event, Fingerprint: Fingerprint(event)})
		if err != nil {
			t.Fatal(err)
		}
		return decision
	}
	first := decide(base)
	if first.RootCauseKey != "payments.client-cancelled" || decide(otherService).RootCauseKey != first.RootCauseKey {
		t.Fatalf("root cause drifted: first=%+v other=%+v", first, decide(otherService))
	}
	if got := decide(differentCause); got.RootCauseKey == first.RootCauseKey {
		t.Fatalf("different message merged into known cause: %+v", got)
	}
}

func TestStorePersistsExternalDecisionAndAttentionExcludesIgnore(t *testing.T) {
	store, err := OpenStoreWithCora(t.TempDir()+"/cora.db", testRuleCore(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	attention := Event{ProductLine: "payments", Service: "ledger", Logger: "DatabaseClient", ExceptionType: "DatabaseUnavailable"}
	ignored := Event{ProductLine: "payments", Service: "checkout", Logger: "CheckoutHandler",
		ExceptionType: "ClientCancelled", Message: "client cancelled request"}
	for _, event := range []Event{attention, ignored} {
		if err := store.Record(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.Attention(context.Background(), "payments")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].RuleID != "attention.database-unavailable" {
		t.Fatalf("attention items=%+v", items)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/attention?product_line=payments", nil)
	response := httptest.NewRecorder()
	Handler(store).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Attention []AttentionItem `json:"attention"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Attention) != 1 || body.Attention[0].Fingerprint != Fingerprint(attention) {
		t.Fatalf("body=%s", response.Body.String())
	}
}

func TestStorePassesOccurrenceContextAcrossCoraBoundary(t *testing.T) {
	core := &recordingCora{}
	store, err := OpenStoreWithCora(t.TempDir()+"/cora.db", core)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	event := Event{ProductLine: "line", Service: "api", ExceptionType: "Timeout"}
	if err := store.Record(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(core.requests) != 2 || !core.requests[0].FirstOccurrence || core.requests[0].OccurrenceCount != 1 ||
		core.requests[1].FirstOccurrence || core.requests[1].OccurrenceCount != 2 {
		t.Fatalf("requests=%+v", core.requests)
	}
}

func TestCoraFailureDoesNotRollbackProblemFacts(t *testing.T) {
	store, err := OpenStoreWithCora(t.TempDir()+"/cora.db", failingCora{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	event := Event{ProductLine: "line", Service: "api", ExceptionType: "Timeout"}
	if err := store.Record(context.Background(), event); err != nil {
		t.Fatalf("record should survive Cora failure: %v", err)
	}
	problems, err := store.Problems(context.Background(), "line")
	if err != nil || len(problems) != 1 {
		t.Fatalf("problem facts missing: problems=%v err=%v", problems, err)
	}
	items, err := store.Attention(context.Background(), "line")
	if err != nil || len(items) != 1 || items[0].Decision != DecisionObserve ||
		items[0].RuleID != "cora.default.core-unavailable" {
		t.Fatalf("fallback decision missing: items=%v err=%v", items, err)
	}
}

type recordingCora struct{ requests []DecisionRequest }

func (c *recordingCora) Decide(_ context.Context, request DecisionRequest) (CoraDecision, error) {
	c.requests = append(c.requests, request)
	return CoraDecision{Decision: DecisionObserve, Category: "test", RuleID: "test",
		Reason: "test", Source: "test", DecidedAt: time.Now().UTC()}, nil
}

type failingCora struct{}

func (failingCora) Decide(context.Context, DecisionRequest) (CoraDecision, error) {
	return CoraDecision{}, errors.New("unavailable")
}
