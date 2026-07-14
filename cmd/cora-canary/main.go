package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/claracore/cora/internal/auth"
	"github.com/claracore/cora/internal/buildinfo"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (transport bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header.Set("Authorization", "Bearer "+transport.token)
	return transport.base.RoundTrip(clone)
}

type attentionItem struct {
	ProductLine string `json:"product_line"`
	Service     string `json:"service"`
	Fingerprint string `json:"fingerprint"`
	State       string `json:"state"`
}

func main() {
	serverURL := flag.String("server-url", "", "Cora Server base URL, for example http://10.0.0.10:8080")
	tokenFile := flag.String("auth-token-file", "", "file containing the Server bearer token")
	productLine := flag.String("product-line", "", "explicit product line to query")
	showVersion := flag.Bool("version", false, "print build identity and exit")
	flag.Parse()
	if *showVersion {
		_ = json.NewEncoder(os.Stdout).Encode(buildinfo.Current())
		return
	}
	if *serverURL == "" || *tokenFile == "" || *productLine == "" {
		fatal(errors.New("server-url, auth-token-file, and product-line are required"))
	}
	token, err := auth.LoadBearerTokenFile(*tokenFile)
	if err != nil {
		fatal(err)
	}
	baseURL := strings.TrimRight(*serverURL, "/")
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: bearerTransport{token: token, base: http.DefaultTransport},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := checkHTTP(ctx, client, baseURL+"/healthz", false); err != nil {
		fatal(err)
	}
	if err := checkHTTP(ctx, client, baseURL+"/readyz", true); err != nil {
		fatal(err)
	}

	mcpClient := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "cora-canary", Version: buildinfo.Current().Version}, nil)
	session, err := mcpClient.Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint: baseURL + "/mcp", HTTPClient: client, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		fatal(fmt.Errorf("connect MCP: %w", err))
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		fatal(fmt.Errorf("list MCP tools: %w", err))
	}
	expected := map[string]bool{
		"cora_list_attention": false, "cora_get_problem": false, "cora_record_outcome": false,
		"cora_export_cases": false,
	}
	toolNames := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		toolNames = append(toolNames, tool.Name)
		if _, ok := expected[tool.Name]; ok {
			expected[tool.Name] = true
		}
	}
	for name, found := range expected {
		if !found {
			fatal(fmt.Errorf("MCP tool %s is missing", name))
		}
	}
	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "cora_list_attention", Arguments: map[string]any{"product_line": *productLine, "limit": 10},
	})
	if err != nil {
		fatal(fmt.Errorf("call cora_list_attention: %w", err))
	}
	if result.IsError {
		fatal(fmt.Errorf("call cora_list_attention: %w", result.GetError()))
	}
	var list struct {
		Attention []attentionItem `json:"attention"`
	}
	if err := decodeStructured(result.StructuredContent, &list); err != nil {
		fatal(err)
	}
	inspected := false
	if len(list.Attention) > 0 {
		item := list.Attention[0]
		result, err = session.CallTool(ctx, &mcpsdk.CallToolParams{
			Name: "cora_get_problem", Arguments: map[string]any{
				"product_line": item.ProductLine, "service": item.Service, "fingerprint": item.Fingerprint,
			},
		})
		if err != nil {
			fatal(fmt.Errorf("call cora_get_problem: %w", err))
		}
		if result.IsError {
			fatal(fmt.Errorf("call cora_get_problem: %w", result.GetError()))
		}
		inspected = true
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"status": "ok", "server_url": baseURL, "product_line": *productLine,
		"mcp_tools": toolNames, "attention_count": len(list.Attention),
		"first_problem_inspected": inspected, "build": buildinfo.Current(),
	})
}

func checkHTTP(ctx context.Context, client *http.Client, endpoint string, authenticated bool) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if !authenticated {
		request.Header.Del("Authorization")
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("GET %s: %w", endpoint, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("GET %s: status=%d body=%s", endpoint, response.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func decodeStructured(value, target any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode MCP output: %w", err)
	}
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
