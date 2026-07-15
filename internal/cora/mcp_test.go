package cora

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPProblemDetailRelatesSharedTracesAndBoundsBreadcrumbs(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir() + "/cora.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	breadcrumbs := make([]Breadcrumb, 12)
	for index := range breadcrumbs {
		breadcrumbs[index] = Breadcrumb{
			Level: "INFO", Message: fmt.Sprintf("breadcrumb-%02d %s", index, strings.Repeat("上下文", 400)),
		}
	}
	breadcrumbs[0].Message = "https://bucket.oss.example/a?OSSAccessKeyId=historical-key&Expires=1720000000&Signature=historical-signature " + breadcrumbs[0].Message
	primary := Event{
		ProductLine: "payments", Service: "checkout", Environment: "prod", TraceID: "trace-shared",
		Logger: "com.example.Checkout", ExceptionType: "CheckoutFailure",
		Message:     "checkout failed https://bucket.s3.example/a?X-Amz-Credential=historical-credential&X-Amz-Signature=historical-amz-signature",
		Stacktrace:  "at com.example.Checkout.submit(Checkout.java:41) https://bucket.oss.example/a?Signature=historical-stack-signature",
		Labels:      map[string]string{"download": "https://bucket.oss.example/a?OSSAccessKeyId=historical-label-key"},
		Breadcrumbs: breadcrumbs,
	}
	related := Event{
		ProductLine: "payments", Service: "inventory", Environment: "prod", TraceID: "trace-shared",
		Logger: "com.example.Inventory", ExceptionType: "InventoryFailure", Message: "inventory failed",
		Stacktrace: "at com.example.Inventory.reserve(Inventory.java:51)",
	}
	unrelated := related
	unrelated.Service = "pricing"
	unrelated.TraceID = "trace-other"
	unrelated.Logger = "com.example.Pricing"
	unrelated.Stacktrace = "at com.example.Pricing.lookup(Pricing.java:61)"
	for _, event := range []Event{primary, related, unrelated} {
		if err := store.Record(ctx, event); err != nil {
			t.Fatal(err)
		}
	}

	httpServer := httptest.NewServer(NewMCPHandler(store))
	defer httpServer.Close()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "cora-test", Version: "v0"}, nil)
	session, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint: httpServer.URL, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	result := callMCPTool(t, ctx, session, "cora_get_problem", map[string]any{
		"product_line": "payments", "service": "checkout", "fingerprint": Fingerprint(primary),
	})
	var output getProblemOutput
	decodeStructuredOutput(t, result, &output)
	if len(output.Detail.RelatedProblems) != 1 ||
		output.Detail.RelatedProblems[0].Service != "inventory" ||
		len(output.Detail.RelatedProblems[0].SharedTraceIDs) != 1 ||
		output.Detail.RelatedProblems[0].SharedTraceIDs[0] != "trace-shared" {
		t.Fatalf("related problems=%+v", output.Detail.RelatedProblems)
	}
	listResult := callMCPTool(t, ctx, session, "cora_list_attention", map[string]any{
		"product_line": "payments",
	})
	var listOutput listAttentionOutput
	decodeStructuredOutput(t, listResult, &listOutput)
	if len(listOutput.Attention) != 2 {
		t.Fatalf("attention incidents=%d, want 2: %+v", len(listOutput.Attention), listOutput.Attention)
	}
	var grouped *AttentionItem
	for index := range listOutput.Attention {
		if listOutput.Attention[index].IncidentProblemCount == 2 {
			grouped = &listOutput.Attention[index]
			break
		}
	}
	if grouped == nil || grouped.IncidentKey == "" || grouped.RelatedCount != 1 ||
		len(grouped.RelatedProblems) != 1 || len(grouped.SharedTraceIDs) != 1 ||
		grouped.SharedTraceIDs[0] != "trace-shared" ||
		len(grouped.IncidentServices) != 2 || grouped.IncidentServices[0] != "checkout" ||
		grouped.IncidentServices[1] != "inventory" {
		t.Fatalf("grouped attention incident=%+v", grouped)
	}
	limitedResult := callMCPTool(t, ctx, session, "cora_list_attention", map[string]any{
		"product_line": "payments", "limit": 1,
	})
	var limitedOutput listAttentionOutput
	decodeStructuredOutput(t, limitedResult, &limitedOutput)
	if len(limitedOutput.Attention) != 1 {
		t.Fatalf("incident limit applied before grouping: %+v", limitedOutput.Attention)
	}
	var sample Event
	if err := json.Unmarshal([]byte(output.Detail.Problem.FirstSample), &sample); err != nil {
		t.Fatal(err)
	}
	if len(sample.Breadcrumbs) != mcpBreadcrumbLimit {
		t.Fatalf("MCP breadcrumbs=%d, want %d", len(sample.Breadcrumbs), mcpBreadcrumbLimit)
	}
	sanitizedSample := output.Detail.Problem.FirstSample
	for _, secret := range []string{"historical-key", "1720000000", "historical-signature", "historical-credential", "historical-amz-signature", "historical-stack-signature", "historical-label-key"} {
		if strings.Contains(sanitizedSample, secret) {
			t.Fatalf("historical signed URL secret %q leaked through MCP: %s", secret, sanitizedSample)
		}
	}
	if !strings.Contains(sanitizedSample, "[REDACTED]") {
		t.Fatalf("MCP sample missing signed URL redaction: %s", sanitizedSample)
	}
	if !strings.Contains(sample.Breadcrumbs[0].Message, "breadcrumb-00") ||
		!strings.HasPrefix(sample.Breadcrumbs[len(sample.Breadcrumbs)-1].Message, "breadcrumb-11") {
		t.Fatalf("bounded breadcrumbs lost chronological edges: %+v", sample.Breadcrumbs)
	}
	for _, breadcrumb := range sample.Breadcrumbs {
		if len(breadcrumb.Message) > mcpBreadcrumbMessageBytes || !strings.HasSuffix(breadcrumb.Message, "[truncated]") {
			t.Fatalf("unbounded MCP breadcrumb bytes=%d suffix=%q", len(breadcrumb.Message), breadcrumb.Message[len(breadcrumb.Message)-20:])
		}
	}
	stored, err := store.GetProblem(ctx, "payments", "checkout", Fingerprint(primary))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(stored.Problem.FirstSample), &sample); err != nil {
		t.Fatal(err)
	}
	if len(sample.Breadcrumbs) != len(breadcrumbs) {
		t.Fatalf("stored breadcrumbs=%d, want original %d", len(sample.Breadcrumbs), len(breadcrumbs))
	}
	if !strings.Contains(stored.Problem.FirstSample, "historical-key") {
		t.Fatalf("read-time sanitization unexpectedly mutated stored sample: %s", stored.Problem.FirstSample)
	}
}

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
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	iterationResult := callMCPTool(t, ctx, session, "cora_iteration_snapshot", map[string]any{
		"product_line": "payments", "business_date": time.Now().In(location).Format("2006-01-02"),
		"timezone": "Asia/Shanghai", "baseline_days": 7,
	})
	var iterationOutput iterationSnapshotOutput
	decodeStructuredOutput(t, iterationResult, &iterationOutput)
	if iterationOutput.Snapshot.SchemaVersion != IterationSnapshotSchemaVersion ||
		len(iterationOutput.Snapshot.Problems) != 1 ||
		iterationOutput.Snapshot.Problems[0].Fingerprint != fingerprint ||
		iterationOutput.Snapshot.Problems[0].WindowCount != 1 {
		t.Fatalf("iteration snapshot=%+v", iterationOutput.Snapshot)
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
	listResult = callMCPTool(t, ctx, session, "cora_list_attention", map[string]any{
		"product_line": "payments",
	})
	decodeStructuredOutput(t, listResult, &listOutput)
	acknowledgedVisible := false
	for _, item := range listOutput.Attention {
		if item.Fingerprint == Fingerprint(event2) && item.State == ProblemStateAcknowledged {
			acknowledgedVisible = true
			break
		}
	}
	if !acknowledgedVisible {
		t.Fatalf("acknowledged unhandled problem missing from attention: %+v", listOutput.Attention)
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

func TestMCPRetentionAuditIsProductScopedReadOnlyAndPaged(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(t.TempDir() + "/cora.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	makeEvent := func(productLine, logger, method string) Event {
		return Event{
			ProductLine: productLine, Service: "checkout", Environment: "prod",
			Logger: logger, ExceptionType: "CheckoutFailure", Message: method + " failed",
			Stacktrace: "at com.example.Checkout." + method + "(Checkout.java:41)",
		}
	}
	active := makeEvent("payments", "com.example.Active", "active")
	acknowledged := makeEvent("payments", "com.example.Acknowledged", "acknowledged")
	resolved := makeEvent("payments", "com.example.Resolved", "resolved")
	otherLine := makeEvent("orders", "com.example.Orders", "orders")
	for _, event := range []Event{active, acknowledged, resolved, otherLine} {
		if err := store.Record(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.RecordOutcome(ctx, Outcome{
		ProductLine: "payments", Service: "checkout", Fingerprint: Fingerprint(acknowledged),
		Actor: "codex", IsRealProblem: true, Handled: false,
		RootCause: "investigation pending", Action: "continue investigation",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordOutcome(ctx, Outcome{
		ProductLine: "payments", Service: "checkout", Fingerprint: Fingerprint(resolved),
		Actor: "codex", IsRealProblem: true, Handled: true,
		RootCause: "bounded issue", Action: "deployed bounded fix",
	}); err != nil {
		t.Fatal(err)
	}

	var beforeProblems, beforeCases int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM problems`).Scan(&beforeProblems); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM problem_cases`).Scan(&beforeCases); err != nil {
		t.Fatal(err)
	}

	httpServer := httptest.NewServer(NewMCPHandler(store))
	defer httpServer.Close()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "cora-test", Version: "v0"}, nil)
	session, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint: httpServer.URL, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	firstResult := callMCPTool(t, ctx, session, "cora_retention_audit", map[string]any{
		"product_line": "payments", "limit": 2,
	})
	var first retentionAuditOutput
	decodeStructuredOutput(t, firstResult, &first)
	if first.Audit.SchemaVersion != OnlineRetentionAuditSchemaVersion ||
		first.Audit.Mode != onlineRetentionAuditMode || !first.Audit.ForensicAuditRequired {
		t.Fatalf("audit contract=%+v", first.Audit)
	}
	if first.Audit.Summary.ProblemCount != 3 || first.Audit.Summary.ResolvedProblems != 1 ||
		first.Audit.Summary.ProblemsWithHandledCase != 1 ||
		first.Audit.Summary.ForensicAuditCandidates != 1 ||
		first.Audit.Summary.RetentionEligibleProblems != 0 {
		t.Fatalf("audit summary=%+v", first.Audit.Summary)
	}
	if len(first.Audit.Problems) != 2 || !first.Audit.HasMore || first.Audit.NextAfterProblemID == 0 {
		t.Fatalf("first audit page=%+v", first.Audit)
	}

	secondResult := callMCPTool(t, ctx, session, "cora_retention_audit", map[string]any{
		"product_line": "payments", "after_problem_id": first.Audit.NextAfterProblemID, "limit": 2,
	})
	var second retentionAuditOutput
	decodeStructuredOutput(t, secondResult, &second)
	if len(second.Audit.Problems) != 1 || second.Audit.HasMore {
		t.Fatalf("second audit page=%+v", second.Audit)
	}
	all := append(append([]OnlineRetentionProblem{}, first.Audit.Problems...), second.Audit.Problems...)
	byFingerprint := map[string]OnlineRetentionProblem{}
	for _, problem := range all {
		byFingerprint[problem.Fingerprint] = problem
		if problem.RetentionEligible || !containsReason(problem.BlockingReasons, offlineReceiptReason) {
			t.Fatalf("online audit authorized retention: %+v", problem)
		}
	}
	if !containsReason(byFingerprint[Fingerprint(active)].BlockingReasons, "problem_active") ||
		!containsReason(byFingerprint[Fingerprint(active)].BlockingReasons, "handled_case_missing") {
		t.Fatalf("active blockers=%+v", byFingerprint[Fingerprint(active)])
	}
	if !containsReason(byFingerprint[Fingerprint(acknowledged)].BlockingReasons, "problem_active") ||
		!containsReason(byFingerprint[Fingerprint(acknowledged)].BlockingReasons, "handled_case_missing") {
		t.Fatalf("acknowledged blockers=%+v", byFingerprint[Fingerprint(acknowledged)])
	}
	if !byFingerprint[Fingerprint(resolved)].ForensicAuditCandidate ||
		len(byFingerprint[Fingerprint(resolved)].BlockingReasons) != 1 {
		t.Fatalf("resolved candidate=%+v", byFingerprint[Fingerprint(resolved)])
	}
	if _, exists := byFingerprint[Fingerprint(otherLine)]; exists {
		t.Fatalf("cross-product-line problem leaked into audit")
	}

	var afterProblems, afterCases int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM problems`).Scan(&afterProblems); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM problem_cases`).Scan(&afterCases); err != nil {
		t.Fatal(err)
	}
	if beforeProblems != afterProblems || beforeCases != afterCases {
		t.Fatalf("retention audit wrote production facts: problems %d->%d cases %d->%d",
			beforeProblems, afterProblems, beforeCases, afterCases)
	}
}

func containsReason(reasons []string, target string) bool {
	for _, reason := range reasons {
		if reason == target {
			return true
		}
	}
	return false
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
