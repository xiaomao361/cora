package iteration

import (
	"fmt"
	"sort"
	"strings"
)

func renderShadowMarkdown(report ShadowEval) string {
	var output strings.Builder
	fmt.Fprintf(&output, "# Cora shadow evaluation\n\n")
	fmt.Fprintf(&output, "- Iteration run: `%s`\n", report.IterationRunID)
	fmt.Fprintf(&output, "- Product line: `%s`\n", report.ProductLine)
	fmt.Fprintf(&output, "- Case snapshot: `%s`\n", report.CaseSnapshotID)
	fmt.Fprintf(&output, "- Candidates: %d\n", report.CandidateCount)
	fmt.Fprintf(&output, "- Daily Problems / occurrences: %d / %d\n", report.DailyProblems, report.DailyOccurrences)
	fmt.Fprintf(&output, "- Known real-problem recall: %d/%d (%.2f)\n", report.KnownRealProblemRecall.Attention,
		report.KnownRealProblemRecall.Total, report.KnownRealProblemRecall.Recall)
	fmt.Fprintf(&output, "- Known noise escalated to attention: %d\n\n", report.KnownNoiseEscalated)

	output.WriteString("## Decision transitions\n\n")
	output.WriteString("| Transition | Problems | Occurrences |\n|---|---:|---:|\n")
	keys := make([]string, 0, len(report.ProblemTransitions))
	for key := range report.ProblemTransitions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&output, "| `%s` | %d | %d |\n", key, report.ProblemTransitions[key], report.OccurrenceTransitions[key])
	}

	output.WriteString("\n## Ignore frequency escalation review\n\n")
	if len(report.FrequencyEscalations) == 0 {
		output.WriteString("No ignore rule crossed the configured frequency threshold.\n")
	} else {
		output.WriteString("| Service | Rule | Window count | Prior daily average | Ratio |\n|---|---|---:|---:|---:|\n")
		for _, item := range report.FrequencyEscalations {
			ratio := "new"
			if item.FrequencyRatio != nil {
				ratio = fmt.Sprintf("%.2fx", *item.FrequencyRatio)
			}
			fmt.Fprintf(&output, "| `%s` | `%s` | %d | %.2f | %s |\n", item.Service, item.RuleID,
				item.WindowCount, item.PriorDailyAverage, ratio)
		}
		output.WriteString("\nThese entries require business and code/release review; they are not promoted automatically.\n")
	}

	output.WriteString("\n## Safety boundary\n\n")
	for _, note := range report.Notes {
		fmt.Fprintf(&output, "- %s\n", note)
	}
	return output.String()
}
