package cora

import "testing"

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
