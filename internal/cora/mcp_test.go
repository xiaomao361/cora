package cora

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

func TestMCPAttentionInvestigationOutcomeAndRecurrenceLoop(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir() + "/cora.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	event := Event{
		ProductLine: "payments", Service: "checkout", Environment: "prod",
		Logger: "com.example.Checkout", ExceptionType: "java.lang.OutOfMemoryError",
		Message: "checkout failed", Stacktrace: "at com.example.Checkout.submit(Checkout.java:41)",
		Labels: map[string]string{"node": "service01", "deployment_group": "checkout-a"},
	}
	if err := store.Record(ctx, event); err != nil {
		t.Fatal(err)
	}
	fingerprint := Fingerprint(event)

	handler := HandlerWithOptions(store, HandlerOptions{
		BearerToken: "test-token",
		MCPHandler:  NewMCPHandler(store),
	})
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	unauthorized, err := http.Post(httpServer.URL+"/mcp", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized MCP status=%d, want %d", unauthorized.StatusCode, http.StatusUnauthorized)
	}

	httpClient := &http.Client{Transport: bearerTransport{token: "test-token", base: http.DefaultTransport}}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "cora-test", Version: "v0"}, nil)
	session, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint: httpServer.URL + "/mcp", HTTPClient: httpClient,
		DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	listResult := callMCPTool(t, ctx, session, "cora_list_attention", map[string]any{
		"product_line": "payments",
	})
	var listOutput listAttentionOutput
	decodeStructuredOutput(t, listResult, &listOutput)
	if len(listOutput.Attention) != 1 || listOutput.Attention[0].State != ProblemStateNew {
		t.Fatalf("initial attention=%+v", listOutput.Attention)
	}

	getResult := callMCPTool(t, ctx, session, "cora_get_problem", map[string]any{
		"product_line": "payments", "service": "checkout", "fingerprint": fingerprint,
	})
	var getOutput getProblemOutput
	decodeStructuredOutput(t, getResult, &getOutput)
	if getOutput.Detail.Problem.State != ProblemStateNew || len(getOutput.Detail.NodeOccurrences) != 1 || len(getOutput.Detail.Cases) != 0 {
		t.Fatalf("initial detail=%+v", getOutput.Detail)
	}

	recordResult := callMCPTool(t, ctx, session, "cora_record_outcome", map[string]any{
		"product_line": "payments", "service": "checkout", "fingerprint": fingerprint,
		"actor": "codex", "is_real_problem": true, "handled": true,
		"root_cause": "checkout retained oversized payloads", "action": "bounded payload retention",
	})
	var recordOutput recordOutcomeOutput
	decodeStructuredOutput(t, recordResult, &recordOutput)
	if recordOutput.Case.ResultingState != ProblemStateResolved ||
		recordOutput.Case.ContextSnapshot.Decision.Decision == "" {
		t.Fatalf("recorded case=%+v", recordOutput.Case)
	}

	listResult = callMCPTool(t, ctx, session, "cora_list_attention", map[string]any{
		"product_line": "payments",
	})
	decodeStructuredOutput(t, listResult, &listOutput)
	if len(listOutput.Attention) != 0 {
		t.Fatalf("resolved problem still in current attention: %+v", listOutput.Attention)
	}
	historical := event
	historical.OccurredAt = getOutput.Detail.Problem.FirstSeen.Add(-1)
	if err := store.Record(ctx, historical); err != nil {
		t.Fatal(err)
	}
	listResult = callMCPTool(t, ctx, session, "cora_list_attention", map[string]any{
		"product_line": "payments",
	})
	decodeStructuredOutput(t, listResult, &listOutput)
	if len(listOutput.Attention) != 0 {
		t.Fatalf("historical event reopened resolved problem: %+v", listOutput.Attention)
	}

	if err := store.Record(ctx, event); err != nil {
		t.Fatal(err)
	}
	listResult = callMCPTool(t, ctx, session, "cora_list_attention", map[string]any{
		"product_line": "payments",
	})
	decodeStructuredOutput(t, listResult, &listOutput)
	if len(listOutput.Attention) != 1 || listOutput.Attention[0].State != ProblemStateRecurring {
		t.Fatalf("recurring attention=%+v", listOutput.Attention)
	}

	getResult = callMCPTool(t, ctx, session, "cora_get_problem", map[string]any{
		"product_line": "payments", "service": "checkout", "fingerprint": fingerprint,
	})
	decodeStructuredOutput(t, getResult, &getOutput)
	if getOutput.Detail.Problem.State != ProblemStateRecurring || len(getOutput.Detail.Cases) != 1 ||
		getOutput.Detail.Cases[0].RootCause != "checkout retained oversized payloads" {
		t.Fatalf("recurring detail=%+v", getOutput.Detail)
	}

	event2 := event
	event2.Logger = "com.example.CheckoutInventory"
	event2.Message = "inventory reservation failed"
	event2.Stacktrace = "at com.example.CheckoutInventory.reserve(CheckoutInventory.java:51)"
	if err := store.Record(ctx, event2); err != nil {
		t.Fatal(err)
	}
	case2, err := store.RecordOutcome(ctx, Outcome{
		ProductLine: "payments", Service: "checkout", Fingerprint: Fingerprint(event2),
		Actor: "codex", IsRealProblem: true, Handled: false,
		RootCause: "inventory dependency timed out", Action: "investigate dependency latency",
	})
	if err != nil {
		t.Fatal(err)
	}

	firstExportResult := callMCPTool(t, ctx, session, "cora_export_cases", map[string]any{
		"product_line": "payments", "limit": 1,
	})
	var firstExport exportCasesOutput
	decodeStructuredOutput(t, firstExportResult, &firstExport)
	if len(firstExport.Export.Cases) != 1 || !firstExport.Export.HasMore ||
		firstExport.Export.SnapshotThroughCaseID != case2.ID || firstExport.Export.PageSHA256 == "" {
		t.Fatalf("first export=%+v", firstExport.Export)
	}

	event3 := event
	event3.Logger = "com.example.CheckoutPricing"
	event3.Message = "pricing lookup failed"
	event3.Stacktrace = "at com.example.CheckoutPricing.lookup(CheckoutPricing.java:61)"
	if err := store.Record(ctx, event3); err != nil {
		t.Fatal(err)
	}
	case3, err := store.RecordOutcome(ctx, Outcome{
		ProductLine: "payments", Service: "checkout", Fingerprint: Fingerprint(event3),
		Actor: "codex", IsRealProblem: true, Handled: true,
		RootCause: "pricing cache was unavailable", Action: "restored pricing cache",
	})
	if err != nil {
		t.Fatal(err)
	}

	secondExportResult := callMCPTool(t, ctx, session, "cora_export_cases", map[string]any{
		"product_line":    "payments",
		"after_case_id":   firstExport.Export.NextAfterCaseID,
		"through_case_id": firstExport.Export.SnapshotThroughCaseID,
		"limit":           1,
	})
	var secondExport exportCasesOutput
	decodeStructuredOutput(t, secondExportResult, &secondExport)
	if len(secondExport.Export.Cases) != 1 || secondExport.Export.HasMore ||
		secondExport.Export.Cases[0].ID != case2.ID || secondExport.Export.Cases[0].ID == case3.ID ||
		secondExport.Export.SnapshotID != firstExport.Export.SnapshotID {
		t.Fatalf("second export=%+v", secondExport.Export)
	}

	latestExportResult := callMCPTool(t, ctx, session, "cora_export_cases", map[string]any{
		"product_line": "payments", "limit": 200,
	})
	var latestExport exportCasesOutput
	decodeStructuredOutput(t, latestExportResult, &latestExport)
	if len(latestExport.Export.Cases) != 3 || latestExport.Export.SnapshotThroughCaseID != case3.ID {
		t.Fatalf("latest export=%+v", latestExport.Export)
	}
}

func callMCPTool(t *testing.T, ctx context.Context, session *mcpsdk.ClientSession, name string, arguments map[string]any) *mcpsdk.CallToolResult {
	t.Helper()
	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if result.IsError {
		t.Fatalf("call %s returned tool error: %+v", name, result.Content)
	}
	return result
}

func decodeStructuredOutput(t *testing.T, result *mcpsdk.CallToolResult, target any) {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("decode structured output %s: %v", data, err)
	}
}
