package cora

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestCoraShadowEvalProfilesLeakageAndLabelConflicts(t *testing.T) {
	csv := `id,source_file,line_number,timestamp,trace_id,class_name,method,message,exception,rule_id,label,category
1,ledger.log,10,2026-04-01 10:00:00.000,,DatabaseClient,query,database unavailable,,attention.database-unavailable,attention,database
2,ledger.log,11,2026-04-02 10:00:00.000,,DatabaseClient,query,database unavailable retry,,attention.database-unavailable,attention,database
3,checkout.log,20,28:51.7,,CheckoutHandler,handle,client cancelled,,ignore.client-cancelled,ignore,cancelled
4,checkout.log,21,29:22.6,,CheckoutHandler,handle,client cancelled again,,manual,attention,review
`
	report, err := EvaluateCoraCSV(context.Background(), strings.NewReader(csv), "payments", testRuleCore(t))
	if err != nil {
		t.Fatal(err)
	}
	if report.Model != "cora" || report.ModelVersion != "0.1.0" || report.ExperienceVersion != "payments-example-v1" {
		t.Fatalf("model identity=%s@%s experience=%s", report.Model, report.ModelVersion, report.ExperienceVersion)
	}
	if report.DataQuality.Rows != 4 || report.DataQuality.ParsedTimestamps != 2 || report.DataQuality.TimeSplitAvailable {
		t.Fatalf("data quality=%+v", report.DataQuality)
	}
	if report.Signature.Unique != 2 || report.Signature.DuplicateRows != 2 || report.Signature.LabelConflictSignatures != 1 {
		t.Fatalf("signature metrics=%+v", report.Signature)
	}
	if report.RowMetrics.AttentionToIgnore != 1 || report.RowMetrics.IgnoreToAttention != 0 ||
		report.RowMetrics.DecisionCounts[DecisionAttention] != 2 ||
		report.RowMetrics.DecisionCounts[DecisionIgnore] != 2 {
		t.Fatalf("row metrics=%+v", report.RowMetrics)
	}
	if len(report.Disagreements) != 1 || report.Disagreements[0].Fingerprint == "" {
		t.Fatalf("disagreements=%+v", report.Disagreements)
	}
	var markdown bytes.Buffer
	if err := WriteShadowEvalMarkdown(&markdown, report); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(markdown.String(), "client cancelled again") || !strings.Contains(markdown.String(), "Time-based validation is blocked") {
		t.Fatalf("unsafe or incomplete report:\n%s", markdown.String())
	}
}

func TestCoraShadowEvalRecordsLenientCSVFallback(t *testing.T) {
	csv := "id,source_file,line_number,timestamp,trace_id,class_name,method,message,exception,rule_id,label,category\n" +
		"1,ledger.log,10,28:51.7,,DatabaseClient,query,bare \" quote,,attention.database-unavailable,attention,database\n"
	report, err := EvaluateCoraCSV(context.Background(), strings.NewReader(csv), "payments", testRuleCore(t))
	if err != nil {
		t.Fatal(err)
	}
	if !report.DataQuality.LenientCSVParsing || report.DataQuality.CSVParseWarning == "" || report.DataQuality.Rows != 1 {
		t.Fatalf("data quality=%+v", report.DataQuality)
	}
}
