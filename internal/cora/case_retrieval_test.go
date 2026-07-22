package cora

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRetrieveCasesRanksHandledCasesAndKeepsProductLineBoundary(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStoreWithCora(t.TempDir()+"/cora.db", testRuleCore(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	strong := Event{
		ProductLine: "payments", Service: "checkout", Environment: "prod",
		Logger: "LegacyPaymentProcessor", Method: "charge", ExceptionType: "GatewayTimeout",
		Message: "gateway timeout for order 111", Stacktrace: "at com.example.LegacyPaymentProcessor.charge(Payment.java:41)",
	}
	weak := Event{
		ProductLine: "payments", Service: "checkout", Environment: "prod",
		Logger: "InventoryClient", Method: "reserve", ExceptionType: "GatewayTimeout",
		Message: "inventory reservation failed", Stacktrace: "at com.example.InventoryClient.reserve(Inventory.java:51)",
	}
	unhandled := Event{
		ProductLine: "payments", Service: "checkout", Environment: "prod",
		Logger: "UnhandledPaymentProcessor", Method: "charge", ExceptionType: "GatewayTimeout",
		Message: "gateway timeout for order 222", Stacktrace: "at com.example.UnhandledPaymentProcessor.charge(Payment.java:61)",
	}
	serviceOnly := Event{
		ProductLine: "payments", Service: "checkout", Environment: "prod",
		Logger: "ProfileClient", Method: "load", ExceptionType: "ProfileMissing",
		Message: "customer profile missing", Stacktrace: "at com.example.ProfileClient.load(Profile.java:31)",
	}
	otherLine := strong
	otherLine.ProductLine = "orders"
	otherLine.Logger = "OrdersPaymentProcessor"
	otherLine.Stacktrace = "at com.example.OrdersPaymentProcessor.charge(Payment.java:71)"

	recordCase := func(event Event, handled bool, rootCause, action string) {
		t.Helper()
		if err := store.Record(ctx, event); err != nil {
			t.Fatal(err)
		}
		if _, err := store.RecordOutcome(ctx, Outcome{
			ProductLine: event.ProductLine, Service: event.Service, Fingerprint: Fingerprint(event),
			Actor: "codex", IsRealProblem: true, Handled: handled,
			RootCause: rootCause, Action: action,
		}); err != nil {
			t.Fatal(err)
		}
	}
	recordCase(strong, true, "payment gateway timed out", "retry with bounded backoff")
	recordCase(weak, true, "inventory dependency timed out", "inspect inventory latency")
	recordCase(unhandled, false, "investigation incomplete", "continue investigation")
	recordCase(serviceOnly, true, "customer profile was absent", "ask the customer to complete the profile")
	recordCase(otherLine, true, "orders gateway timed out", "inspect orders gateway")

	target := Event{
		ProductLine: "payments", Service: "checkout", Environment: "prod",
		Logger: "PaymentProcessor", Method: "charge", ExceptionType: "GatewayTimeout",
		Message: "gateway timeout for order 999", Stacktrace: "at com.example.PaymentProcessor.charge(Payment.java:81)",
	}
	if err := store.Record(ctx, target); err != nil {
		t.Fatal(err)
	}

	retrieval, err := store.RetrieveCases(ctx, "payments", "checkout", Fingerprint(target), "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if retrieval.SchemaVersion != CaseRetrievalSchemaVersion || retrieval.QueryRuleID != "cora.default.unmatched" {
		t.Fatalf("retrieval contract=%+v", retrieval)
	}
	if len(retrieval.Cases) != 2 {
		t.Fatalf("retrieved cases=%+v", retrieval.Cases)
	}
	if retrieval.Cases[0].Case.RootCause != "payment gateway timed out" ||
		retrieval.Cases[0].Score <= retrieval.Cases[1].Score ||
		!containsReason(retrieval.Cases[0].MatchReasons, "same_normalized_message") ||
		!containsReason(retrieval.Cases[0].MatchReasons, "same_root_cause_key") {
		t.Fatalf("ranked cases=%+v", retrieval.Cases)
	}
	for _, item := range retrieval.Cases {
		if item.Case.ProductLine != "payments" || !item.Case.Handled || item.Case.RootCause == "investigation incomplete" {
			t.Fatalf("retrieval leaked ineligible case=%+v", item)
		}
	}

	matched := Event{ProductLine: "payments", Service: "ledger", Logger: "DatabaseClient", ExceptionType: "DatabaseUnavailable"}
	if err := store.Record(ctx, matched); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetrieveCases(ctx, "payments", "ledger", Fingerprint(matched), "", 5); !errors.Is(err, ErrCaseRetrievalRequiresUnmatched) {
		t.Fatalf("matched rule retrieval error=%v", err)
	}
}

func TestMCPRetrieveCasesReturnsCaseLevelOutcome(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStoreWithCora(t.TempDir()+"/cora.db", testRuleCore(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	historical := Event{
		ProductLine: "payments", Service: "checkout", Logger: "LegacyPaymentProcessor",
		Method: "charge", ExceptionType: "GatewayTimeout", Message: "gateway timeout for order 111",
		Stacktrace: "at com.example.LegacyPaymentProcessor.charge(Payment.java:41)",
	}
	if err := store.Record(ctx, historical); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordOutcome(ctx, Outcome{
		ProductLine: "payments", Service: "checkout", Fingerprint: Fingerprint(historical),
		Actor: "codex", IsRealProblem: true, Handled: true,
		RootCause: "payment gateway timed out", Action: "retry with bounded backoff",
	}); err != nil {
		t.Fatal(err)
	}
	target := historical
	target.Logger = "PaymentProcessor"
	target.Message = "gateway timeout for order 999"
	target.Stacktrace = "at com.example.PaymentProcessor.charge(Payment.java:81)"
	if err := store.Record(ctx, target); err != nil {
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

	result := callMCPTool(t, ctx, session, "cora_retrieve_cases", map[string]any{
		"product_line": "payments", "service": "checkout", "fingerprint": Fingerprint(target),
	})
	var output retrieveCasesOutput
	decodeStructuredOutput(t, result, &output)
	if len(output.Retrieval.Cases) != 1 ||
		output.Retrieval.Cases[0].Case.RootCause != "payment gateway timed out" ||
		output.Retrieval.Cases[0].Case.Action != "retry with bounded backoff" {
		t.Fatalf("MCP retrieval=%+v", output.Retrieval)
	}
}
