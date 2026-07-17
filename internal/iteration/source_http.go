package iteration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/claracore/cora/internal/buildinfo"
	"github.com/claracore/cora/internal/cora"
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

type HTTPSource struct {
	baseURL string
	client  *http.Client
	session *mcpsdk.ClientSession
}

func NewHTTPSource(ctx context.Context, serverURL, token string) (*HTTPSource, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if baseURL == "" || strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("server URL and bearer token are required")
	}
	client := &http.Client{
		Timeout:   20 * time.Second,
		Transport: bearerTransport{token: token, base: http.DefaultTransport},
	}
	mcpClient := mcpsdk.NewClient(&mcpsdk.Implementation{
		Name: "cora-iterate", Version: buildinfo.Current().Version,
	}, nil)
	session, err := mcpClient.Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint: baseURL + "/mcp", HTTPClient: client, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect Cora MCP: %w", err)
	}
	return &HTTPSource{baseURL: baseURL, client: client, session: session}, nil
}

func (source *HTTPSource) Close() error {
	if source.session == nil {
		return nil
	}
	return source.session.Close()
}

func (source *HTTPSource) Health(ctx context.Context) (ServerSnapshot, error) {
	var response struct {
		Build   buildinfo.Info   `json:"build"`
		Storage cora.StoreHealth `json:"storage"`
	}
	if err := source.getJSON(ctx, "/healthz", nil, &response, false); err != nil {
		return ServerSnapshot{}, err
	}
	return ServerSnapshot{Build: response.Build, Storage: response.Storage}, nil
}

func (source *HTTPSource) Attention(ctx context.Context, productLine string, limit int) ([]cora.AttentionItem, error) {
	result, err := source.call(ctx, "cora_list_attention", map[string]any{
		"product_line": productLine, "limit": limit,
	})
	if err != nil {
		return nil, err
	}
	var response struct {
		Attention []cora.AttentionItem `json:"attention"`
	}
	if err := decodeStructured(result.StructuredContent, &response); err != nil {
		return nil, err
	}
	return response.Attention, nil
}

func (source *HTTPSource) IterationSnapshot(ctx context.Context, productLine, businessDate, timezone string, baselineDays, limit int) (cora.IterationSnapshot, error) {
	result, err := source.call(ctx, "cora_iteration_snapshot", map[string]any{
		"product_line": productLine, "business_date": businessDate, "timezone": timezone,
		"baseline_days": baselineDays, "limit": limit,
	})
	if err != nil {
		return cora.IterationSnapshot{}, err
	}
	var response struct {
		Snapshot cora.IterationSnapshot `json:"snapshot"`
	}
	if err := decodeStructured(result.StructuredContent, &response); err != nil {
		return cora.IterationSnapshot{}, err
	}
	return response.Snapshot, nil
}

func (source *HTTPSource) Problem(ctx context.Context, productLine, service, fingerprint, rootCauseKey string) (cora.ProblemDetail, error) {
	result, err := source.call(ctx, "cora_get_problem", map[string]any{
		"product_line": productLine, "service": service, "fingerprint": fingerprint,
		"root_cause_key": rootCauseKey,
	})
	if err != nil {
		return cora.ProblemDetail{}, err
	}
	var response struct {
		Detail cora.ProblemDetail `json:"detail"`
	}
	if err := decodeStructured(result.StructuredContent, &response); err != nil {
		return cora.ProblemDetail{}, err
	}
	return response.Detail, nil
}

func (source *HTTPSource) ExportCases(ctx context.Context, productLine string, afterCaseID, throughCaseID int64, limit int) (cora.CaseExportPage, error) {
	result, err := source.call(ctx, "cora_export_cases", map[string]any{
		"product_line": productLine, "after_case_id": afterCaseID,
		"through_case_id": throughCaseID, "limit": limit,
	})
	if err != nil {
		return cora.CaseExportPage{}, err
	}
	var response struct {
		Export cora.CaseExportPage `json:"export"`
	}
	if err := decodeStructured(result.StructuredContent, &response); err != nil {
		return cora.CaseExportPage{}, err
	}
	return response.Export, nil
}

func (source *HTTPSource) call(ctx context.Context, name string, arguments map[string]any) (*mcpsdk.CallToolResult, error) {
	result, err := source.session.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", name, err)
	}
	if result.IsError {
		return nil, fmt.Errorf("call %s: %w", name, result.GetError())
	}
	return result, nil
}

func (source *HTTPSource) getJSON(ctx context.Context, path string, query url.Values, target any, authenticated bool) error {
	endpoint := source.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if !authenticated {
		request.Header.Del("Authorization")
	}
	response, err := source.client.Do(request)
	if err != nil {
		return fmt.Errorf("GET %s: %w", endpoint, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("GET %s: status=%d body=%s", endpoint, response.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("decode GET %s: %w", endpoint, err)
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
