package schemas

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestWorkflowSchemasAreStrictAndSelfContained(t *testing.T) {
	tests := map[string]struct {
		version  string
		required []string
	}{
		"cora-iteration-run.v1.schema.json": {
			version:  "cora.iteration-run.v1",
			required: []string{"schema_version", "iteration_run_id", "product_line", "case_snapshot", "status"},
		},
		"cora-iteration-snapshot.v1.schema.json": {
			version:  "cora.iteration-snapshot.v1",
			required: []string{"schema_version", "product_line", "business_date", "timezone", "window_start", "window_end", "baseline_start", "baseline_days", "generated_at", "summary", "problems"},
		},
		"cora-rule-candidate.v1.schema.json": {
			version:  "cora.rule-candidate.v1",
			required: []string{"schema_version", "candidate_id", "iteration_run_id", "product_line", "source_case_ids", "risk"},
		},
		"cora-closure-receipt.v1.schema.json": {
			version:  "cora.closure-receipt.v1",
			required: []string{"schema_version", "closure_receipt_id", "iteration_run_id", "product_line", "case_snapshot", "rule", "evaluation", "deployment", "observation", "retention_eligible"},
		},
		"cora-code-evidence.v1.schema.json": {
			version:  "cora.code-evidence.v1",
			required: []string{"schema_version", "evidence_id", "product_line", "service", "fingerprint", "source", "status", "references"},
		},
		"cora-retention-audit.v1.schema.json": {
			version:  "cora.retention-audit.v1",
			required: []string{"schema_version", "audit_run_id", "product_line", "database", "cora_build", "storage", "tables", "problem_decisions"},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			var schema map[string]any
			if err := json.Unmarshal(data, &schema); err != nil {
				t.Fatalf("parse schema: %v", err)
			}
			if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
				t.Fatalf("unexpected dialect: %v", schema["$schema"])
			}
			if schema["additionalProperties"] != false {
				t.Fatal("top-level schema must reject unknown fields")
			}
			properties := schema["properties"].(map[string]any)
			version := properties["schema_version"].(map[string]any)["const"]
			if version != test.version {
				t.Fatalf("schema_version const=%v, want %s", version, test.version)
			}
			gotRequired := stringSlice(schema["required"])
			for _, field := range test.required {
				if !contains(gotRequired, field) {
					t.Errorf("required does not contain %q", field)
				}
			}
			assertLocalReferencesResolve(t, schema, schema)
		})
	}
}

func TestAllSchemasAreValidJSON(t *testing.T) {
	paths, err := filepath.Glob("*.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	if len(paths) < 10 {
		t.Fatalf("found %d schemas, want at least 10", len(paths))
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
}

func assertLocalReferencesResolve(t *testing.T, root map[string]any, value any) {
	t.Helper()
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			if key == "$ref" {
				ref, ok := child.(string)
				if !ok || !strings.HasPrefix(ref, "#/$defs/") {
					t.Fatalf("workflow schemas may only use local $defs references, got %v", child)
				}
				name := strings.TrimPrefix(ref, "#/$defs/")
				if _, ok := root["$defs"].(map[string]any)[name]; !ok {
					t.Errorf("unresolved reference %s", ref)
				}
				continue
			}
			assertLocalReferencesResolve(t, root, child)
		}
	case []any:
		for _, child := range current {
			assertLocalReferencesResolve(t, root, child)
		}
	}
}

func stringSlice(value any) []string {
	items := reflect.ValueOf(value)
	result := make([]string, 0, items.Len())
	for index := 0; index < items.Len(); index++ {
		result = append(result, items.Index(index).Interface().(string))
	}
	return result
}

func contains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
