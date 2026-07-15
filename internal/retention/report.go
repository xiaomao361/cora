package retention

import (
	"fmt"
	"strings"
)

func renderMarkdown(audit Audit) string {
	var output strings.Builder
	fmt.Fprintf(&output, "# Cora retention audit\n\n")
	fmt.Fprintf(&output, "- Audit run: `%s`\n", audit.AuditRunID)
	fmt.Fprintf(&output, "- Product line: `%s`\n", audit.ProductLine)
	fmt.Fprintf(&output, "- Backup capture identity: `%s`\n", audit.CapturedAt.Format("2006-01-02T15:04:05.999999999Z07:00"))
	fmt.Fprintf(&output, "- Database: `%s` (%d bytes, schema v%d)\n", audit.Database.SHA256, audit.Database.SizeBytes, audit.Database.SchemaVersion)
	fmt.Fprintf(&output, "- Cora build: `%s`\n", audit.CoraBuild.Version)
	fmt.Fprintf(&output, "- Cora source digest: `%s`\n", audit.CoraBuild.SourceDigest)
	fmt.Fprintf(&output, "- Eligible / total Problems: %d / %d\n\n", audit.Summary.EligibleProblems, audit.Summary.ProblemCount)

	output.WriteString("## Storage\n\n")
	output.WriteString("| Metric | Value |\n|---|---:|\n")
	fmt.Fprintf(&output, "| Page count | %d |\n", audit.Storage.PageCount)
	fmt.Fprintf(&output, "| Page size | %d bytes |\n", audit.Storage.PageSizeBytes)
	fmt.Fprintf(&output, "| Freelist pages | %d |\n", audit.Storage.FreelistCount)
	fmt.Fprintf(&output, "| Reusable freelist bytes | %d |\n", audit.Storage.ReusableFreelistBytes)
	fmt.Fprintf(&output, "| WAL / SHM sidecars | %d / %d bytes |\n", audit.Storage.WALSizeBytes, audit.Storage.SHMSizeBytes)
	fmt.Fprintf(&output, "| SQLite quick_check | `%s` |\n", audit.Storage.QuickCheck)
	fmt.Fprintf(&output, "| Physical database file | %d bytes |\n\n", audit.Database.SizeBytes)
	output.WriteString("Freelist pages are reusable inside SQLite; they are not bytes already removed from the physical file.\n\n")

	output.WriteString("## Product-line table facts\n\n")
	output.WriteString("| Table | Rows | Earliest | Latest |\n|---|---:|---|---|\n")
	for _, table := range audit.Tables {
		fmt.Fprintf(&output, "| `%s` | %d | %s | %s |\n", table.Table, table.Rows, emptyDash(table.EarliestAt), emptyDash(table.LatestAt))
	}

	output.WriteString("\n## Problem breakdowns\n")
	renderBuckets(&output, "State", audit.Breakdowns.ByState)
	renderBuckets(&output, "Decision", audit.Breakdowns.ByDecision)
	renderBuckets(&output, "Handled evidence", audit.Breakdowns.ByHandled)
	renderBuckets(&output, "Case presence", audit.Breakdowns.ByCases)

	output.WriteString("\n## Eligibility decisions\n\n")
	output.WriteString("| Problem | State | Decision | Cases / handled | Eligible | Blocking reasons | Estimated rows / bytes |\n|---|---|---|---:|---|---|---:|\n")
	for _, item := range audit.ProblemDecisions {
		reasons := "—"
		if len(item.BlockingReasons) > 0 {
			reasons = "`" + strings.Join(item.BlockingReasons, "`, `") + "`"
		}
		fmt.Fprintf(&output, "| `%s/%s` | `%s` | `%s` | %d / %d | %t | %s | %d / %d |\n",
			item.Service, item.Fingerprint, item.State, item.Decision, item.CaseCount, item.HandledCaseCount,
			item.RetentionEligible, reasons, item.EstimatedReleasableRows, item.EstimatedReleasableBytes)
	}

	output.WriteString("\n## Logical release estimate\n\n")
	fmt.Fprintf(&output, "- Estimated removable fine-grained rows: %d\n", audit.LogicalRelease.EstimatedRows)
	fmt.Fprintf(&output, "- Estimated removable payload: %d bytes\n", audit.LogicalRelease.EstimatedBytes)
	fmt.Fprintf(&output, "- Method: %s.\n", audit.LogicalRelease.Method)
	fmt.Fprintf(&output, "- Caveat: %s\n", audit.LogicalRelease.PhysicalCaveat)

	output.WriteString("\n## Safety boundary\n\n")
	output.WriteString("This B0 report is read-only. It authorizes no migration, compact, DELETE, checkpoint, purge, or VACUUM operation. Missing or unverifiable evidence is always ineligible.\n")
	return output.String()
}

func renderBuckets(output *strings.Builder, title string, buckets []CountBucket) {
	fmt.Fprintf(output, "\n### %s\n\n", title)
	output.WriteString("| Key | Problems | Occurrences |\n|---|---:|---:|\n")
	for _, bucket := range buckets {
		fmt.Fprintf(output, "| `%s` | %d | %d |\n", bucket.Key, bucket.Problems, bucket.Occurrences)
	}
}

func emptyDash(value string) string {
	if value == "" {
		return "—"
	}
	return "`" + value + "`"
}
