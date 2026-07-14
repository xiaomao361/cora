package cora

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/claracore/cora/internal/buildinfo"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	mcpBreadcrumbLimit        = 8
	mcpBreadcrumbMessageBytes = 768
)

type listAttentionInput struct {
	ProductLine string `json:"product_line" jsonschema:"product line to query; cases never cross this boundary"`
	Limit       int    `json:"limit,omitempty" jsonschema:"maximum results from 1 to 200; defaults to 50"`
}

type listAttentionOutput struct {
	Attention []AttentionItem `json:"attention"`
}

type getProblemInput struct {
	ProductLine string `json:"product_line" jsonschema:"product line owning the problem"`
	Service     string `json:"service" jsonschema:"service owning the problem"`
	Fingerprint string `json:"fingerprint" jsonschema:"Cora problem fingerprint"`
}

type getProblemOutput struct {
	Detail ProblemDetail `json:"detail"`
}

type recordOutcomeInput struct {
	ProductLine   string `json:"product_line" jsonschema:"product line owning the problem"`
	Service       string `json:"service" jsonschema:"service owning the problem"`
	Fingerprint   string `json:"fingerprint" jsonschema:"Cora problem fingerprint"`
	Actor         string `json:"actor" jsonschema:"agent or engineer recording the result"`
	IsRealProblem bool   `json:"is_real_problem" jsonschema:"whether investigation confirmed a real problem"`
	Handled       bool   `json:"handled" jsonschema:"whether the problem was handled or intentionally closed"`
	RootCause     string `json:"root_cause" jsonschema:"one-line root cause or reason it is noise"`
	Action        string `json:"action" jsonschema:"one-line action taken or next action"`
}

type recordOutcomeOutput struct {
	Case ProblemCase `json:"case"`
}

type exportCasesInput struct {
	ProductLine   string `json:"product_line" jsonschema:"product line to export; cases never cross this boundary"`
	AfterCaseID   int64  `json:"after_case_id,omitempty" jsonschema:"last case id from the previous page; zero starts a snapshot"`
	ThroughCaseID int64  `json:"through_case_id,omitempty" jsonschema:"frozen snapshot high-water case id returned by the first page"`
	Limit         int    `json:"limit,omitempty" jsonschema:"maximum cases from 1 to 200; defaults to 100"`
}

type exportCasesOutput struct {
	Export CaseExportPage `json:"export"`
}

func NewMCPHandler(store *Store) http.Handler {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "cora", Version: buildinfo.Current().Version}, nil)
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "cora_list_attention",
		Description: "List current new or recurring Cora problems for one explicit product line.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, input listAttentionInput) (*mcpsdk.CallToolResult, listAttentionOutput, error) {
		items, err := store.CurrentAttention(ctx, input.ProductLine, input.Limit)
		return nil, listAttentionOutput{Attention: items}, err
	})
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "cora_get_problem",
		Description: "Get one service-scoped problem with bounded samples, trends, nodes, related problems sharing representative trace IDs, decision, and immutable cases.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, input getProblemInput) (*mcpsdk.CallToolResult, getProblemOutput, error) {
		detail, err := store.GetProblem(ctx, input.ProductLine, input.Service, input.Fingerprint)
		if errors.Is(err, sql.ErrNoRows) {
			err = fmt.Errorf("problem not found in product line %q and service %q", input.ProductLine, input.Service)
		}
		if err == nil {
			detail = boundProblemDetailForMCP(detail)
		}
		return nil, getProblemOutput{Detail: detail}, err
	})
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "cora_record_outcome",
		Description: "Record an investigation outcome as an immutable product-line case and update problem state.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, input recordOutcomeInput) (*mcpsdk.CallToolResult, recordOutcomeOutput, error) {
		item, err := store.RecordOutcome(ctx, Outcome{
			ProductLine: input.ProductLine, Service: input.Service, Fingerprint: input.Fingerprint,
			Actor: input.Actor, IsRealProblem: input.IsRealProblem, Handled: input.Handled,
			RootCause: input.RootCause, Action: input.Action,
		})
		if errors.Is(err, sql.ErrNoRows) {
			err = fmt.Errorf("problem not found in product line %q and service %q", input.ProductLine, input.Service)
		}
		return nil, recordOutcomeOutput{Case: item}, err
	})
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "cora_export_cases",
		Description: "Export one product line's immutable cases in stable pages for local persistence and offline Core iteration. Start with zero cursors, then reuse snapshot_through_case_id and next_after_case_id until has_more is false.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, input exportCasesInput) (*mcpsdk.CallToolResult, exportCasesOutput, error) {
		page, err := store.ExportCases(ctx, input.ProductLine, input.AfterCaseID, input.ThroughCaseID, input.Limit)
		return nil, exportCasesOutput{Export: page}, err
	})

	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server {
		return server
	}, &mcpsdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return http.NewCrossOriginProtection().Handler(handler)
}

func boundProblemDetailForMCP(detail ProblemDetail) ProblemDetail {
	detail.Problem = boundProblemForMCP(detail.Problem)
	for index := range detail.Cases {
		detail.Cases[index].ContextSnapshot.Problem = boundProblemForMCP(
			detail.Cases[index].ContextSnapshot.Problem,
		)
	}
	return detail
}

func boundProblemForMCP(problem Problem) Problem {
	problem.FirstSample = boundSampleForMCP(problem.FirstSample)
	problem.LatestSample = boundSampleForMCP(problem.LatestSample)
	return problem
}

func boundSampleForMCP(sample string) string {
	var event Event
	if err := json.Unmarshal([]byte(sample), &event); err != nil {
		return sample
	}
	if len(event.Breadcrumbs) > mcpBreadcrumbLimit {
		bounded := make([]Breadcrumb, 0, mcpBreadcrumbLimit)
		bounded = append(bounded, event.Breadcrumbs[:2]...)
		bounded = append(bounded, event.Breadcrumbs[len(event.Breadcrumbs)-6:]...)
		event.Breadcrumbs = bounded
	}
	for index := range event.Breadcrumbs {
		event.Breadcrumbs[index].Message = truncateMCPText(
			event.Breadcrumbs[index].Message,
			mcpBreadcrumbMessageBytes,
		)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return sample
	}
	return string(encoded)
}

func truncateMCPText(value string, maximumBytes int) string {
	if len(value) <= maximumBytes {
		return value
	}
	const marker = " … [truncated]"
	limit := maximumBytes - len(marker)
	if limit <= 0 {
		return marker[:maximumBytes]
	}
	var builder strings.Builder
	for _, character := range value {
		if builder.Len()+len(string(character)) > limit {
			break
		}
		builder.WriteRune(character)
	}
	return builder.String() + marker
}
