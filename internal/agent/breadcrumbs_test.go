package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/claracore/cora/internal/cora"
)

func TestBreadcrumbBufferPrefersTraceAndEnforcesLimits(t *testing.T) {
	buffer := newBreadcrumbBuffer(defaultBreadcrumbMaxBytes)
	base := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	for index := 0; index < 25; index++ {
		buffer.add(parsedRecord{
			occurredAt: base.Add(time.Duration(index) * time.Second), traceID: "trace-a",
			thread: "worker", level: "INFO", logger: "logger", message: "trace message",
		})
	}
	buffer.add(parsedRecord{occurredAt: base.Add(25 * time.Second), traceID: "trace-b",
		thread: "worker", level: "INFO", logger: "logger", message: "same thread, other trace"})
	selected := buffer.selectFor(parsedRecord{
		occurredAt: base.Add(26 * time.Second), traceID: "trace-a", thread: "worker", level: "ERROR",
	})
	if len(selected) != traceBreadcrumbLimit {
		t.Fatalf("selected=%d, want %d", len(selected), traceBreadcrumbLimit)
	}
	for _, item := range selected {
		if item.Message != "trace message" {
			t.Fatalf("trace selection leaked another trace: %+v", item)
		}
	}
}

func TestBreadcrumbBufferFallsBackToRecentThread(t *testing.T) {
	buffer := newBreadcrumbBuffer(defaultBreadcrumbMaxBytes)
	base := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	buffer.add(parsedRecord{occurredAt: base, thread: "worker", level: "INFO", message: "too old"})
	for index := 1; index <= 7; index++ {
		buffer.add(parsedRecord{occurredAt: base.Add(time.Duration(index) * time.Second),
			thread: "worker", level: "INFO", message: "recent"})
	}
	buffer.add(parsedRecord{occurredAt: base.Add(7 * time.Second), thread: "other", level: "INFO", message: "other"})
	selected := buffer.selectFor(parsedRecord{occurredAt: base.Add(8 * time.Second), thread: "worker", level: "ERROR"})
	if len(selected) != threadBreadcrumbLimit {
		t.Fatalf("selected=%d, want %d", len(selected), threadBreadcrumbLimit)
	}
	for _, item := range selected {
		if item.Message != "recent" {
			t.Fatalf("thread fallback selected wrong item: %+v", item)
		}
	}
}

func TestBreadcrumbBufferAndPayloadStayByteBounded(t *testing.T) {
	buffer := newBreadcrumbBuffer(512)
	for index := 0; index < 20; index++ {
		buffer.add(parsedRecord{occurredAt: time.Now(), traceID: "trace", thread: "worker",
			level: "INFO", logger: "logger", message: strings.Repeat("x", 400)})
		if size := buffer.encodedSize(); size > 512 {
			t.Fatalf("ring size=%d exceeds bound", size)
		}
	}
	items := []cora.Breadcrumb{{Message: strings.Repeat(`"`, defaultBreadcrumbMaxBytes)}}
	items = boundBreadcrumbs(items, defaultBreadcrumbMaxBytes)
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > defaultBreadcrumbMaxBytes {
		t.Fatalf("payload size=%d exceeds bound", len(encoded))
	}
}

func TestRedactEventCoversKeysAndSensitiveNumberPatterns(t *testing.T) {
	event := cora.Event{
		Message:     `Authorization: Bearer top-secret token=abc password=hunter2 cardNo=6222020202020202 phones=13800138000 13900139000 id=11010519491231002X`,
		Stacktrace:  `java.lang.IllegalStateException: refresh_token=refresh-secret`,
		Labels:      map[string]string{"token": "label-secret", "customer": "13800138000"},
		Breadcrumbs: []cora.Breadcrumb{{Message: `access_token=crumb-secret pwd=crumb-pass`}},
	}
	redacted := redactEvent(event)
	encoded, err := json.Marshal(redacted)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, secret := range []string{"top-secret", "abc", "hunter2", "6222020202020202", "13800138000", "13900139000", "11010519491231002X", "refresh-secret", "label-secret", "crumb-secret", "crumb-pass"} {
		if strings.Contains(text, secret) {
			t.Fatalf("secret %q leaked in %s", secret, text)
		}
	}
	if !strings.Contains(text, "[REDACTED") {
		t.Fatalf("redaction markers missing: %s", text)
	}
}

func TestBreadcrumbsExcludeRequestResponseBodiesContainingErrorText(t *testing.T) {
	cfg := Config{Timezone: "Asia/Shanghai"}
	response, ok := parseLogbackRecord("2026-07-16 16:33:44.228 trace_id: trace-response [http-nio-9090-exec-2] INFO  c.g.c.s.c.RequestResponseLoggingFilter - [logResponse,194] - After request [ 200 OK | 耗时: 13毫秒\n"+
		`{"msg":"操作成功","data":{"remark":"图片目标识别错误，请确保图片中包含对应的内容"}}`, cfg)
	if !ok {
		t.Fatal("response record did not parse")
	}
	if _, emitted := eventFromRecord(response, "supervisor.log", cfg); emitted {
		t.Fatal("INFO response must not be emitted as an event")
	}

	business, ok := parseLogbackRecord("2026-07-16 16:33:45.000 trace_id: trace-response [worker-1] INFO  com.example.PolicyService - [prepare,40] - processing invoice", cfg)
	if !ok {
		t.Fatal("business breadcrumb did not parse")
	}
	errorRecord, ok := parseLogbackRecord("2026-07-16 16:33:46.000 trace_id: trace-response [worker-1] ERROR com.example.PolicyService - [submit,42] - actual failure", cfg)
	if !ok {
		t.Fatal("error record did not parse")
	}

	buffer := newBreadcrumbBuffer(defaultBreadcrumbMaxBytes)
	buffer.add(response)
	buffer.add(business)
	got := buffer.selectFor(errorRecord)
	if len(got) != 1 || got[0].Method != "prepare" {
		t.Fatalf("breadcrumbs=%+v", got)
	}
	for _, breadcrumb := range got {
		if strings.Contains(breadcrumb.Message, "图片目标识别错误") || breadcrumb.Method == "logResponse" {
			t.Fatalf("response content leaked into breadcrumbs: %+v", got)
		}
	}
}

func TestBreadcrumbsKeepRealBusinessErrorContext(t *testing.T) {
	buffer := newBreadcrumbBuffer(defaultBreadcrumbMaxBytes)
	base := time.Date(2026, 7, 16, 16, 43, 23, 0, time.FixedZone("CST", 8*60*60))
	buffer.add(parsedRecord{occurredAt: base, traceID: "trace-policy", thread: "task-29", level: "INFO",
		logger: "com.example.PolicyService", method: "prepare", message: "invoice prepared"})
	errorRecord := parsedRecord{occurredAt: base.Add(time.Second), traceID: "trace-policy", thread: "task-29", level: "ERROR",
		logger: "com.example.InvoiceApplyUploadProcessServiceImpl", method: "lambda$asyncProcess$1", message: "业务处理失败"}

	got := buffer.selectFor(errorRecord)
	if len(got) != 1 || got[0].Message != "invoice prepared" {
		t.Fatalf("business context should remain available: %+v", got)
	}
}
