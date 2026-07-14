package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPromtailStyleMultiTargetConfig(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "agent.yml")
	tokenPath := filepath.Join(directory, "token")
	if err := os.WriteFile(tokenPath, []byte("config-test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	contents := `server:
  http_listen_address: 127.0.0.1
  http_listen_port: 9088
  grpc_listen_port: 0
positions:
  filename: /tmp/cora-positions.yml
clients:
  - url: http://cora.internal:8080/v1/events:batch
    bearer_token_file: ` + tokenPath + `
defaults:
  product_line: qikang-zhifu
  timezone: Asia/Shanghai
agent:
  start_at: end
  batch_wait: 2s
scrape_configs:
  - job_name: gb-auth_log_push
    static_configs:
      - targets: [localhost]
        labels:
          app: gb-auth
          server: backup
          ip: 172.16.0.229
          env: prod
          group: platform
          __path__: /logs/gb-auth/supervisor.log
  - job_name: gb-order_log_push
    static_configs:
      - targets: [localhost]
        labels:
          app: gb-order
          env: test
          product_line: qikang-shop
          __path__: /logs/gb-order/supervisor.log
`
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Health.Port != 9088 || len(runtime.Targets) != 2 {
		t.Fatalf("runtime=%+v", runtime)
	}
	auth := runtime.Targets[0]
	if auth.ProductLine != "qikang-zhifu" || auth.Service != "gb-auth" || auth.Environment != "prod" ||
		auth.Labels["ip"] != "172.16.0.229" || auth.Labels["job"] != "gb-auth_log_push" ||
		auth.BearerToken != "config-test-token" {
		t.Fatalf("auth target=%+v", auth)
	}
	order := runtime.Targets[1]
	if order.ProductLine != "qikang-shop" || order.Environment != "test" || order.BatchWait.String() != "2s" {
		t.Fatalf("order target=%+v", order)
	}
}

func TestLoadConfigExpandsEnvironment(t *testing.T) {
	t.Setenv("TEST_CORA_ENDPOINT", "http://127.0.0.1:8080/v1/events:batch")
	t.Setenv("TEST_CORA_LOG", "/logs/service.log")
	directory := t.TempDir()
	path := filepath.Join(directory, "agent.yml")
	contents := `positions: {filename: /tmp/positions.yml}
clients: [{url: "${TEST_CORA_ENDPOINT}"}]
defaults: {product_line: line}
scrape_configs:
  - job_name: service
    static_configs:
      - labels: {app: service, __path__: "${TEST_CORA_LOG}"}
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Targets[0].Endpoint != "http://127.0.0.1:8080/v1/events:batch" || runtime.Targets[0].Path != "/logs/service.log" {
		t.Fatalf("target=%+v", runtime.Targets[0])
	}
}

func TestConfigRejectsLokiEndpointAndDuplicatePaths(t *testing.T) {
	source := fileConfig{
		Positions: positionsConfig{Filename: "/tmp/positions.yml"},
		Clients:   []clientConfig{{URL: "http://loki:3100/loki/api/v1/push"}},
		Defaults:  defaultsConfig{ProductLine: "line"},
	}
	if _, err := source.runtime(); err == nil || !strings.Contains(err.Error(), "/v1/events:batch") {
		t.Fatalf("error=%v", err)
	}
	source.Clients[0].URL = "http://cora:8080/v1/events:batch"
	source.ScrapeConfigs = []scrapeConfig{
		{JobName: "one", StaticConfigs: []staticConfig{{Labels: map[string]string{"app": "a", "__path__": "/same.log"}}}},
		{JobName: "two", StaticConfigs: []staticConfig{{Labels: map[string]string{"app": "b", "__path__": "/same.log"}}}},
	}
	if _, err := source.runtime(); err == nil || !strings.Contains(err.Error(), "same path") {
		t.Fatalf("error=%v", err)
	}
}

func TestCanaryExampleDefinesAuthenticatedNodeBoundary(t *testing.T) {
	data, err := os.ReadFile("../../config/cora-agent-canary.example.yml")
	if err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("canary-test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "/etc/cora/auth.token", tokenPath, 1))
	configPath := filepath.Join(t.TempDir(), "agent.yml")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.Targets) != 1 || runtime.Targets[0].BearerToken != "canary-test-token" ||
		runtime.Targets[0].Labels["node"] != "service01" ||
		runtime.Targets[0].Labels["deployment_group"] != "service" {
		t.Fatalf("runtime=%+v", runtime)
	}
}
