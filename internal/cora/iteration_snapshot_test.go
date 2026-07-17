package cora

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestIterationSnapshotIncludesAllDecisionsAndBusinessDayBaseline(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStoreWithCora(t.TempDir()+"/cora.db", testRuleCore(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	businessDate := time.Date(2026, 7, 14, 0, 0, 0, 0, location)
	windowStart := businessDate.UTC()
	ignore := Event{ProductLine: "payments", Service: "checkout", Environment: "prod",
		Logger: "CheckoutHandler", Method: "handle", ExceptionType: "ClientCancelled",
		Message: "client cancelled request", Stacktrace: "at CheckoutHandler.handle(CheckoutHandler.java:1)",
		Labels: map[string]string{"node": "service01", "deployment_group": "service"}}
	flushIterationTestAggregate(t, store, ignore, windowStart.AddDate(0, 0, -2).Add(time.Hour), 5)
	flushIterationTestAggregate(t, store, ignore, windowStart.AddDate(0, 0, -1).Add(time.Hour), 10)
	flushIterationTestAggregate(t, store, ignore, windowStart.Add(time.Hour), 60)

	attention := Event{ProductLine: "payments", Service: "ledger", Environment: "prod",
		Logger: "DatabaseClient", ExceptionType: "DatabaseUnavailable", Message: "database unavailable",
		Stacktrace: "at DatabaseClient.query(DatabaseClient.java:1)",
		Labels:     map[string]string{"node": "payment01", "deployment_group": "payment"}}
	flushIterationTestAggregate(t, store, attention, windowStart.Add(2*time.Hour), 4)
	if _, err := store.db.ExecContext(ctx, `DELETE FROM node_trend_points WHERE product_line = ? AND service = ?`,
		"payments", "ledger"); err != nil {
		t.Fatal(err)
	}
	foreign := ignore
	foreign.ProductLine = "orders"
	flushIterationTestAggregate(t, store, foreign, windowStart.Add(3*time.Hour), 99)

	ignoreFingerprint := Fingerprint(ignore)
	caseItem, err := store.RecordOutcome(ctx, Outcome{ProductLine: "payments", Service: "checkout",
		Fingerprint: ignoreFingerprint, Actor: "codex", IsRealProblem: false, Handled: true,
		RootCause: "known callback noise", Action: "review frequency"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.IterationSnapshot(ctx, "payments", "2026-07-14", "Asia/Shanghai", 2, 200)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SchemaVersion != IterationSnapshotSchemaVersion || len(snapshot.Problems) != 2 ||
		snapshot.Summary.MatchedProblemCount != 2 || snapshot.Summary.OccurrenceCount != 64 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	if snapshot.Summary.DecisionProblemCounts[DecisionIgnore] != 1 ||
		snapshot.Summary.DecisionProblemCounts[DecisionAttention] != 1 ||
		snapshot.Summary.DecisionOccurrenceCounts[DecisionIgnore] != 60 {
		t.Fatalf("decision summary=%+v", snapshot.Summary)
	}
	item := snapshot.Problems[0]
	if item.Decision != DecisionIgnore || item.RuleID != "ignore.client-cancelled" || item.WindowCount != 60 ||
		item.PriorDailyAverage != 7.5 || item.FrequencyRatio == nil || *item.FrequencyRatio != 8 ||
		len(item.PriorDailyCounts) != 2 || item.PriorDailyCounts[0].Count != 5 ||
		item.PriorDailyCounts[1].Count != 10 || len(item.NodeCounts) != 1 ||
		item.NodeCounts[0].Count != 60 || len(item.CaseIDs) != 1 || item.CaseIDs[0] != caseItem.ID {
		t.Fatalf("ignore problem=%+v", item)
	}
	attentionItem := snapshot.Problems[1]
	if attentionItem.Decision != DecisionAttention || attentionItem.NodeCounts == nil ||
		len(attentionItem.NodeCounts) != 0 || attentionItem.CaseIDs == nil || len(attentionItem.CaseIDs) != 0 {
		t.Fatalf("empty collections must be encoded as arrays: %+v", attentionItem)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "latest_sample") || strings.Contains(string(encoded), "callback failed") {
		t.Fatalf("iteration snapshot leaked raw samples: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"node_counts":[],"case_ids":[]`) {
		t.Fatalf("iteration snapshot encoded empty collections as null: %s", encoded)
	}

	limited, err := store.IterationSnapshot(ctx, "payments", "2026-07-14", "Asia/Shanghai", 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited.Problems) != 1 || !limited.Summary.Truncated ||
		limited.Summary.MatchedProblemCount != 2 || limited.Summary.ReturnedProblemCount != 1 {
		t.Fatalf("limited snapshot=%+v", limited)
	}
}

func TestIterationSnapshotRejectsInvalidBoundary(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/cora.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, test := range []struct {
		productLine, date, timezone string
	}{
		{"", "2026-07-14", "Asia/Shanghai"},
		{"payments", "2026/07/14", "Asia/Shanghai"},
		{"payments", "2026-07-14", "Mars/Base"},
	} {
		if _, err := store.IterationSnapshot(context.Background(), test.productLine, test.date, test.timezone, 7, 200); err == nil {
			t.Fatalf("boundary unexpectedly accepted: %+v", test)
		}
	}
}

func flushIterationTestAggregate(t *testing.T, store *Store, event Event, windowEnd time.Time, count int64) {
	t.Helper()
	event.OccurredAt = windowEnd.Add(-10 * time.Second)
	fingerprint := Fingerprint(event)
	rootCauseKey := derivedRootCauseKey(event, fingerprint)
	item := newAggregate(event, fingerprint, rootCauseKey)
	item.Count = count
	for node, value := range item.Nodes {
		value.Count = count
		item.Nodes[node] = value
	}
	if err := store.Flush(context.Background(), windowEnd, map[string]aggregate{
		aggregateKey(event, fingerprint, rootCauseKey): item,
	}); err != nil {
		t.Fatal(err)
	}
}
