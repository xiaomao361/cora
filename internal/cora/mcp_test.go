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
