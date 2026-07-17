package cora

import (
	"context"
	"testing"
	"time"
)

func TestCurrentAttentionAddsShadowTraceProjectionWithoutChangingDecision(t *testing.T) {
	store, err := OpenStoreWithCora(t.TempDir()+"/cora.db", shadowProjectionCora{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	aggregator := NewAggregator(store, 20)
	base := time.Date(2026, 7, 17, 1, 0, 0, 0, time.UTC)
	events := []Event{
		shadowEvent("wrapper", "gateway", "trace-projected", base),
		shadowEvent("cause-a", "orders", "trace-projected", base.Add(time.Second)),
		shadowEvent("cause-a", "policy", "trace-projected", base.Add(2*time.Second)),
		shadowEvent("wrapper", "gateway", "trace-unresolved", base.Add(3*time.Second)),
		shadowEvent("wrapper", "gateway", "trace-ambiguous", base.Add(4*time.Second)),
		shadowEvent("cause-a", "orders", "trace-ambiguous", base.Add(5*time.Second)),
		shadowEvent("cause-b", "policy", "trace-ambiguous", base.Add(6*time.Second)),
	}
	for _, event := range events {
		if err := aggregator.Add(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := aggregator.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	items, err := store.CurrentAttention(context.Background(), "line", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Decision != DecisionAttention || items[0].Count != 3 {
		t.Fatalf("shadow projection changed effective attention: %+v", items)
	}
	projection := items[0].TraceProjection
	if items[0].TraceProjectionMode != "shadow" || projection == nil || projection.Projected != 1 ||
		projection.Ambiguous != 1 || projection.Unresolved != 1 || len(projection.Projections) != 3 {
		t.Fatalf("projection summary=%+v item=%+v", projection, items[0])
	}
	for _, trace := range projection.Projections {
		if trace.TraceID == "trace-projected" && (trace.ProjectedDecision != DecisionIgnore ||
			trace.ProjectedRootCause != "root:cause-a" || len(trace.CauseProblemIDs) != 2) {
			t.Fatalf("projected trace did not collapse same cause identity: %+v", trace)
		}
	}
	var ledgerRows, wrapperTraces int
	if err := store.db.QueryRow(`SELECT count(*), count(DISTINCT CASE WHEN trace_role='wrapper' THEN trace_id END)
		FROM trace_problem_occurrences`).Scan(&ledgerRows, &wrapperTraces); err != nil {
		t.Fatal(err)
	}
	if ledgerRows != 7 || wrapperTraces != 3 {
		t.Fatalf("trace ledger rows=%d wrapper_traces=%d", ledgerRows, wrapperTraces)
	}
}

type shadowProjectionCora struct{}

func (shadowProjectionCora) Decide(_ context.Context, request DecisionRequest) (CoraDecision, error) {
	decision := CoraDecision{Category: "test", Reason: "test", Source: "test", DecidedAt: time.Now().UTC()}
	switch request.Event.ExceptionType {
	case "wrapper":
		decision.Decision, decision.RuleID, decision.RootCauseKey, decision.TraceRole =
			DecisionAttention, "wrapper-rule", "root:wrapper", TraceRoleWrapper
	case "cause-a":
		decision.Decision, decision.RuleID, decision.RootCauseKey, decision.TraceRole =
			DecisionIgnore, "cause-a-rule", "root:cause-a", TraceRoleCause
	case "cause-b":
		decision.Decision, decision.RuleID, decision.RootCauseKey, decision.TraceRole =
			DecisionIgnore, "cause-b-rule", "root:cause-b", TraceRoleCause
	}
	return decision, nil
}

func (core shadowProjectionCora) ClassifyRootCause(ctx context.Context, request DecisionRequest) (string, error) {
	decision, err := core.Decide(ctx, request)
	return decision.RootCauseKey, err
}

func shadowEvent(exceptionType, service, traceID string, occurredAt time.Time) Event {
	return Event{ProductLine: "line", Service: service, Environment: "prod", Logger: "test",
		ExceptionType: exceptionType, Message: exceptionType, TraceID: traceID, OccurredAt: occurredAt}
}

func TestGroupAttentionCandidatesUsesTransitiveTraceEvidence(t *testing.T) {
	candidates := []attentionCandidate{
		attentionTestCandidate(1, "orders", "fingerprint-a", "trace-1"),
		attentionTestCandidate(2, "payments", "fingerprint-b", "trace-1", "trace-2"),
		attentionTestCandidate(3, "claims", "fingerprint-c", "trace-2"),
		attentionTestCandidate(4, "pricing", "fingerprint-d"),
	}
	grouped := groupAttentionCandidates(candidates, 50)
	if len(grouped) != 2 {
		t.Fatalf("incidents=%d, want 2: %+v", len(grouped), grouped)
	}
	incident := grouped[0]
	if incident.IncidentProblemCount != 3 || incident.RelatedCount != 2 ||
		len(incident.RelatedProblems) != 2 || len(incident.SharedTraceIDs) != 2 ||
		len(incident.IncidentServices) != 3 {
		t.Fatalf("transitive incident=%+v", incident)
	}
	if grouped[1].IncidentProblemCount != 1 || grouped[1].RelatedCount != 0 ||
		grouped[1].IncidentKey == "" {
		t.Fatalf("standalone incident=%+v", grouped[1])
	}
	limited := groupAttentionCandidates(candidates, 1)
	if len(limited) != 1 || limited[0].IncidentProblemCount != 3 {
		t.Fatalf("post-group limit=%+v", limited)
	}
	reordered := []attentionCandidate{candidates[2], candidates[1], candidates[0]}
	if got := groupAttentionCandidates(reordered, 50); len(got) != 1 || got[0].IncidentKey != incident.IncidentKey {
		t.Fatalf("incident key changed with candidate order: before=%s after=%+v", incident.IncidentKey, got)
	}
}

func TestGroupAttentionCandidatesUsesStableRootCauseAcrossServices(t *testing.T) {
	left := attentionTestCandidate(1, "gb-policy", "fingerprint-a")
	left.item.RootCauseKey = "payments.invoice.image-target-recognition"
	right := attentionTestCandidate(2, "checkout", "fingerprint-b")
	right.item.RootCauseKey = left.item.RootCauseKey
	other := attentionTestCandidate(3, "gb-policy", "fingerprint-c")
	other.item.RootCauseKey = "message:another-cause"

	grouped := groupAttentionCandidates([]attentionCandidate{left, right, other}, 50)
	if len(grouped) != 2 {
		t.Fatalf("incidents=%d, want 2: %+v", len(grouped), grouped)
	}
	incident := grouped[0]
	if incident.IncidentKey != "root-cause:"+left.item.RootCauseKey ||
		incident.IncidentProblemCount != 2 || len(incident.RelatedProblems) != 1 ||
		len(incident.RelatedProblems[0].RelationKinds) != 1 ||
		incident.RelatedProblems[0].RelationKinds[0] != "same_root_cause" {
		t.Fatalf("root-cause incident=%+v", incident)
	}
}

func attentionTestCandidate(id int64, service, fingerprint string, traceIDs ...string) attentionCandidate {
	traces := map[string]bool{}
	for _, traceID := range traceIDs {
		traces[traceID] = true
	}
	return attentionCandidate{
		item:     AttentionItem{ProblemID: id, ProductLine: "line", Service: service, Fingerprint: fingerprint},
		traceIDs: traces,
	}
}
