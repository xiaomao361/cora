package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/claracore/cora/internal/cora"
)

func TestAgentRetriesAndCommitsAcknowledgedPosition(t *testing.T) {
	var attempts atomic.Int32
	received := make(chan cora.Event, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer agent-test-token" {
			t.Errorf("authorization=%q", request.Header.Get("Authorization"))
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body struct {
			Events []cora.Event `json:"events"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		if attempts.Add(1) == 1 {
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		for _, event := range body.Events {
			received <- event
		}
		writer.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	directory := t.TempDir()
	logPath := filepath.Join(directory, "all.log")
	contents := "2026-07-13 14:30:12.000 trace_id: skip [main] INFO  com.example.App - [run,1] - ready\n" +
		"2026-07-13 14:30:13.000 trace_id: trace-1 [main] ERROR com.example.OrderService - [submit,42] - failed\n" +
		"java.lang.IllegalStateException: boom\n\tat com.example.OrderService.submit(OrderService.java:42)\n"
	if err := os.WriteFile(logPath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	positionsPath := filepath.Join(directory, "positions.json")
	cfg := testConfig(logPath, positionsPath, server.URL)
	cfg.BearerToken = "agent-test-token"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()

	select {
	case event := <-received:
		if event.TraceID != "trace-1" || event.ExceptionType != "java.lang.IllegalStateException" {
			t.Fatalf("event=%+v", event)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("event not delivered")
	}
	absolute, _ := filepath.Abs(logPath)
	waitFor(t, func() bool {
		positions, err := loadPositions(positionsPath)
		return err == nil && positions.Positions[absolute].Offset == int64(len(contents))
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts=%d", attempts.Load())
	}
	positions, err := loadPositions(positionsPath)
	if err != nil {
		t.Fatal(err)
	}
	if positions.Positions[absolute].Offset != int64(len(contents)) {
		t.Fatalf("position=%+v size=%d", positions.Positions[absolute], len(contents))
	}

	restartCtx, restartCancel := context.WithCancel(context.Background())
	restarted := make(chan error, 1)
	go func() { restarted <- Run(restartCtx, cfg) }()
	time.Sleep(100 * time.Millisecond)
	if attempts.Load() != 2 {
		t.Fatalf("acknowledged event was resent; attempts=%d", attempts.Load())
	}
	restartCancel()
	if err := <-restarted; err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryStatusShowsRetryAndRecovery(t *testing.T) {
	var logs bytes.Buffer
	previousWriter, previousFlags, previousPrefix := log.Writer(), log.Flags(), log.Prefix()
	log.SetOutput(&logs)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	})
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		writer.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	cfg := testConfig("unused.log", t.TempDir()+"/positions.json", server.URL)
	runtime := newTargetRuntime(cfg)
	runtime.setRunning(true)
	runtime.observeFile(100, 50)
	events := []cora.Event{{
		ProductLine: "line", Service: "api", ExceptionType: "Timeout",
		Message: "private-event-message-must-not-be-logged",
	}}
	if err := sendBatch(context.Background(), server.Client(), cfg, events, runtime); err != nil {
		t.Fatal(err)
	}
	status := runtime.snapshot()
	if status.RetryAttempts != 1 || status.SentEvents != 1 || status.DeliveryFailures != 0 ||
		status.DeliveryFailing || status.LastDeliveryAt == nil || status.LastDeliveryAt.IsZero() || status.LagBytes != 50 {
		t.Fatalf("delivery status=%+v", status)
	}
	ready, reasons := runtimeReadiness([]*targetRuntime{runtime})
	if !ready || len(reasons) != 0 {
		t.Fatalf("readiness ready=%v reasons=%v", ready, reasons)
	}
	text := logs.String()
	if !strings.Contains(text, "Cora Agent batch retry") || !strings.Contains(text, "Cora Agent batch delivered") {
		t.Fatalf("operational delivery logs missing: %s", text)
	}
	if strings.Contains(text, "private-event-message-must-not-be-logged") {
		t.Fatalf("event content leaked into operational logs: %s", text)
	}
}

func TestAgentAttachesBoundedBreadcrumbsAndRedactsBeforeUpload(t *testing.T) {
	received := make(chan cora.Event, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Events []cora.Event `json:"events"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, event := range body.Events {
			received <- event
		}
		writer.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	directory := t.TempDir()
	logPath := filepath.Join(directory, "all.log")
	contents := "2026-07-13 14:30:10.000 trace_id: trace-a [worker-1] INFO  com.example.OrderService - [load,40] - Authorization: Bearer crumb-secret phone=13800138000 url=https://bucket.oss.example/a?OSSAccessKeyId=oss-key&Expires=1720000000&Signature=oss-signature\n" +
		"2026-07-13 14:30:11.000 trace_id: trace-b [worker-1] INFO  com.example.OrderService - [load,41] - other-trace-must-not-attach\n" +
		"2026-07-13 14:30:12.000 trace_id: trace-a [worker-1] ERROR com.example.OrderService - [submit,42] - token=event-secret id=11010519491231002X\n" +
		"java.lang.IllegalStateException: password=stack-secret\n\tat com.example.OrderService.submit(OrderService.java:42)\n"
	if err := os.WriteFile(logPath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(logPath, filepath.Join(directory, "positions.json"), server.URL)
	cfg.Labels = map[string]string{"token": "label-secret", "node": "service01"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()

	var event cora.Event
	select {
	case event = <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("event not delivered")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if event.Thread != "worker-1" || event.Method != "submit" || event.Line != "42" {
		t.Fatalf("event context=%+v", event)
	}
	if len(event.Breadcrumbs) != 1 || event.Breadcrumbs[0].Method != "load" {
		t.Fatalf("breadcrumbs=%+v", event.Breadcrumbs)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, secret := range []string{"crumb-secret", "13800138000", "other-trace-must-not-attach", "event-secret", "11010519491231002X", "stack-secret", "label-secret", "oss-key", "1720000000", "oss-signature"} {
		if strings.Contains(text, secret) {
			t.Fatalf("secret or wrong-trace context %q leaked in %s", secret, text)
		}
	}
	if !strings.Contains(text, "[REDACTED") {
		t.Fatalf("redaction markers missing: %s", text)
	}
}

func TestAgentServerPersistsRedactedRepresentativeBreadcrumbs(t *testing.T) {
	store, err := cora.OpenStore(t.TempDir() + "/cora.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	aggregator := cora.NewAggregator(store, 10)
	server := httptest.NewServer(cora.HandlerWithOptions(store,
		cora.HandlerOptions{BearerToken: "server-test-token"}, aggregator))
	defer server.Close()

	directory := t.TempDir()
	logPath := filepath.Join(directory, "all.log")
	contents := "2026-07-13 14:30:10.000 trace_id: trace-a [worker-1] INFO  com.example.OrderService - [load,40] - token=breadcrumb-secret\n" +
		"2026-07-13 14:30:12.000 trace_id: trace-a [worker-1] ERROR com.example.OrderService - [submit,42] - password=event-secret\n" +
		"java.lang.IllegalStateException: boom\n\tat com.example.OrderService.submit(OrderService.java:42)\n"
	if err := os.WriteFile(logPath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(logPath, filepath.Join(directory, "positions.json"), server.URL+"/v1/events:batch")
	cfg.BearerToken = "server-test-token"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	waitFor(t, func() bool { return aggregator.Stats().PendingFingerprints == 1 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := aggregator.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	problems, err := store.Problems(context.Background(), "payments")
	if err != nil || len(problems) != 1 {
		t.Fatalf("problems=%v err=%v", problems, err)
	}
	if strings.Contains(problems[0].LatestSample, "breadcrumb-secret") || strings.Contains(problems[0].LatestSample, "event-secret") {
		t.Fatalf("representative sample leaked raw values: %s", problems[0].LatestSample)
	}
	var sample cora.Event
	if err := json.Unmarshal([]byte(problems[0].LatestSample), &sample); err != nil {
		t.Fatal(err)
	}
	if len(sample.Breadcrumbs) != 1 || !strings.Contains(sample.Breadcrumbs[0].Message, "[REDACTED]") {
		t.Fatalf("stored breadcrumbs=%+v", sample.Breadcrumbs)
	}
}

func TestNextBatchHonorsEncodedByteLimit(t *testing.T) {
	pending := []queuedEvent{
		{event: cora.Event{Message: strings.Repeat("x", 900)}, end: 1},
		{event: cora.Event{Message: strings.Repeat("x", 900)}, end: 2},
	}
	count, events, err := nextBatch(pending, 10, 1500)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || len(events) != 1 {
		t.Fatalf("count=%d events=%d", count, len(events))
	}
}

func TestAgentReopensRotatedFile(t *testing.T) {
	received := make(chan cora.Event, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Events []cora.Event `json:"events"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, event := range body.Events {
			received <- event
		}
		writer.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	directory := t.TempDir()
	logPath := filepath.Join(directory, "all.log")
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(logPath, filepath.Join(directory, "positions.json"), server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()

	appendLog(t, logPath, errorLine("first"))
	waitEvent(t, received, "first")
	if err := os.Rename(logPath, logPath+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte(errorLine("second")), 0o644); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, received, "second")
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func testConfig(logPath, positionsPath, endpoint string) Config {
	return Config{
		Path: logPath, PositionsPath: positionsPath, Endpoint: endpoint,
		ProductLine: "payments", Service: "checkout", Environment: "test",
		Timezone: "Asia/Shanghai", StartAtBeginning: true, BatchSize: 10,
		MaxEventBytes: 256 << 10, MaxBatchBytes: 1536 << 10,
		BatchWait: 30 * time.Millisecond, PollInterval: 10 * time.Millisecond,
		RequestTimeout: time.Second, MaxRetries: 2, MinBackoff: 10 * time.Millisecond,
		MaxBackoff: 20 * time.Millisecond,
	}
}

func appendLog(t *testing.T, path, content string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(content); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func errorLine(message string) string {
	return "2026-07-13 14:30:13.000 trace_id: trace [main] ERROR com.example.OrderService - [submit,42] - " + message + "\n"
}

func waitEvent(t *testing.T, events <-chan cora.Event, message string) {
	t.Helper()
	select {
	case event := <-events:
		if event.Message != message {
			t.Fatalf("message=%q want=%q", event.Message, message)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("event %q not delivered", message)
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !condition() {
		t.Fatal("condition not reached before timeout")
	}
}
