package agent

import (
	"strings"
	"testing"
	"time"
)

func TestParseProductionLogbackMultilineError(t *testing.T) {
	cfg := Config{ProductLine: "payments", Service: "checkout", Environment: "prod", Timezone: "Asia/Shanghai"}
	record := "2026-07-13 14:30:12.345 trace_id: abc123 [http-nio-8080-exec-1] ERROR c.g.o.c.s.i.SettleClaimCasesServiceImpl - [addClaimCases,417] - 案件推送失败异常信息\n" +
		"java.lang.IllegalStateException: downstream failed\n" +
		"\tat com.example.order.cases.service.impl.SettleClaimCasesServiceImpl.addClaimCases(SettleClaimCasesServiceImpl.java:417)"

	event, ok := parseRecord(record, "/var/log/checkout/all.log", cfg)
	if !ok {
		t.Fatal("expected ERROR record")
	}
	if event.TraceID != "abc123" || event.Source != "all.log" || event.ExceptionType != "java.lang.IllegalStateException" {
		t.Fatalf("event=%+v", event)
	}
	if event.Thread != "http-nio-8080-exec-1" || event.Method != "addClaimCases" || event.Line != "417" {
		t.Fatalf("logback context missing: event=%+v", event)
	}
	if !strings.Contains(event.Stacktrace, "addClaimCases") || event.Message != "案件推送失败异常信息" {
		t.Fatalf("event=%+v", event)
	}
	want := time.Date(2026, 7, 13, 14, 30, 12, 345000000, time.FixedZone("CST", 8*60*60))
	if !event.OccurredAt.Equal(want) {
		t.Fatalf("occurred_at=%s want=%s", event.OccurredAt, want)
	}
}

func TestParseErrorWithoutThrowableUsesCallerFrame(t *testing.T) {
	cfg := Config{ProductLine: "line", Service: "service", Environment: "test"}
	record := "2026-07-13 14:30:12.345 trace_id:  [main] ERROR com.example.OrderService - [submit,42] - failed"
	event, ok := parseRecord(record, "all.log", cfg)
	if !ok || event.ExceptionType != "logback.ERROR" || event.Stacktrace != "at com.example.OrderService.submit(Unknown Source)" {
		t.Fatalf("event=%+v ok=%t", event, ok)
	}
}

func TestParseIgnoresNonError(t *testing.T) {
	cfg := Config{ProductLine: "line", Service: "service", Environment: "test"}
	_, ok := parseRecord("2026-07-13 14:30:12.345 trace_id: abc [main] INFO  com.example.OrderService - [submit,42] - ok", "all.log", cfg)
	if ok {
		t.Fatal("INFO record should be ignored")
	}
}
