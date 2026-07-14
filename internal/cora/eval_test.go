package cora

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestCoraShadowEvalProfilesLeakageAndLabelConflicts(t *testing.T) {
	csv := `id,source_file,line_number,timestamp,trace_id,class_name,method,message,exception,rule_id,label,category
1,statement.log,10,2026-04-01 10:00:00.000,,DruidDataSource,handleFatalError,CommunicationsException,,at_01,attention,database
2,statement.log,11,2026-04-02 10:00:00.000,,DruidDataSource,handleFatalError,CommunicationsException retry,,at_01,attention,database
3,gateway.log,20,28:51.7,,RouterServiceImpl,checkAccess,expired,,ig_03,ignore,token
4,gateway.log,21,29:22.6,,RouterServiceImpl,checkAccess,expired again,,manual,attention,review
`
	report, err := EvaluateCoraCSV(context.Background(), strings.NewReader(csv), "gbjk-zhifu")
	if err != nil {
		t.Fatal(err)
	}
	if report.Model != "cora" || report.ModelVersion != "0.1.0" || report.ExperienceVersion != "cora-gbjk-v0.1.0" {
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
	if strings.Contains(markdown.String(), "expired again") || !strings.Contains(markdown.String(), "Time-based validation is blocked") {
		t.Fatalf("unsafe or incomplete report:\n%s", markdown.String())
	}
}

func TestCoraShadowEvalRecordsLenientCSVFallback(t *testing.T) {
	csv := "id,source_file,line_number,timestamp,trace_id,class_name,method,message,exception,rule_id,label,category\n" +
		"1,statement.log,10,28:51.7,,DruidDataSource,handleFatalError,bare \" quote,,at_01,attention,database\n"
	report, err := EvaluateCoraCSV(context.Background(), strings.NewReader(csv), "gbjk-zhifu")
	if err != nil {
		t.Fatal(err)
	}
	if !report.DataQuality.LenientCSVParsing || report.DataQuality.CSVParseWarning == "" || report.DataQuality.Rows != 1 {
		t.Fatalf("data quality=%+v", report.DataQuality)
	}
}
