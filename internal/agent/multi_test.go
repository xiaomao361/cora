package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/claracore/cora/internal/cora"
)

func TestRunMultiTailsTwoFilesWithSharedPositions(t *testing.T) {
	received := make(chan cora.Event, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Events []cora.Event `json:"events"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
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
	positions := filepath.Join(directory, "positions.yml")
	firstPath, secondPath := filepath.Join(directory, "auth.log"), filepath.Join(directory, "order.log")
	for _, path := range []string{firstPath, secondPath} {
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	first := testConfig(firstPath, positions, server.URL)
	first.Service = "identity"
	first.Labels = map[string]string{"server": "backup", "job": "auth"}
	second := testConfig(secondPath, positions, server.URL)
	second.Service = "checkout"
	second.Labels = map[string]string{"server": "backup", "job": "order"}
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	healthPort := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunMulti(ctx, RuntimeConfig{
			Health:  HealthConfig{Address: "127.0.0.1", Port: healthPort},
			Targets: []Config{first, second},
		})
	}()
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/healthz", healthPort)
	waitFor(t, func() bool {
		response, err := http.Get(healthURL)
		if err != nil {
			return false
		}
		defer response.Body.Close()
		var body struct {
			Targets int `json:"targets"`
		}
		return json.NewDecoder(response.Body).Decode(&body) == nil && body.Targets == 2
	})
	readyURL := fmt.Sprintf("http://127.0.0.1:%d/readyz", healthPort)
	waitFor(t, func() bool {
		response, err := http.Get(readyURL)
		if err != nil {
			return false
		}
		defer response.Body.Close()
		return response.StatusCode == http.StatusOK
	})
	response, err := http.Get(readyURL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var readiness struct {
		Status          string `json:"status"`
		ReadableTargets int    `json:"readable_targets"`
	}
	if err := json.NewDecoder(response.Body).Decode(&readiness); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || readiness.Status != "ready" || readiness.ReadableTargets != 2 {
		t.Fatalf("status=%d readiness=%+v", response.StatusCode, readiness)
	}
	appendLog(t, firstPath, errorLine("auth-failed"))
	appendLog(t, secondPath, errorLine("order-failed"))
	services := map[string]bool{}
	for range 2 {
		select {
		case event := <-received:
			services[event.Service] = true
			if event.Labels["server"] != "backup" {
				t.Fatalf("labels=%v", event.Labels)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("multi-target event not delivered")
		}
	}
	if !services["identity"] || !services["checkout"] {
		t.Fatalf("services=%v", services)
	}
	waitFor(t, func() bool {
		loaded, err := loadPositions(positions)
		return err == nil && len(loaded.Positions) == 2
	})
	waitFor(t, func() bool {
		response, err := http.Get(healthURL)
		if err != nil {
			return false
		}
		defer response.Body.Close()
		var body struct {
			TargetStatuses []TargetStatus `json:"target_statuses"`
		}
		if json.NewDecoder(response.Body).Decode(&body) != nil || len(body.TargetStatuses) != 2 {
			return false
		}
		for _, status := range body.TargetStatuses {
			if !status.Running || !status.Readable || status.SentEvents != 1 || status.LagBytes != 0 {
				return false
			}
		}
		return true
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
