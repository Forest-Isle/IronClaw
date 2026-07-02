package bench

import (
	"fmt"
	"io"
	"text/tabwriter"
)

// WriteReport writes a human-readable behavior bench report.
func WriteReport(w io.Writer, results []CaseResult) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "CASE\tPASS\tCHECKS\tSTATUS\tTOKENS(in+out)")
	passed := 0
	for _, result := range results {
		if result.Passed {
			passed++
		}
		writeResultRow(tw, result)
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintf(w, "%d/%d cases passed\n", passed, len(results))
}

func writeResultRow(w io.Writer, result CaseResult) {
	status := result.Status
	if status == "" {
		status = "-"
	}
	_, _ = fmt.Fprintf(w, "%s\t%s\t%d/%d\t%s\t%d+%d\n",
		result.Name, passLabel(result.Passed), passedChecks(result.Checks),
		len(result.Checks), status, result.InputTokens, result.OutputTokens)
	if result.Passed {
		return
	}
	for _, check := range result.Checks {
		if !check.Passed {
			_, _ = fmt.Fprintf(w, "  check\tFAIL\t%s %s\t-\t%s\n",
				check.Check.Type, check.Check.Path, check.Detail)
		}
	}
	if result.Err != nil {
		_, _ = fmt.Fprintf(w, "  error\tFAIL\t-\t-\t%s\n", result.Err)
	}
}

func passLabel(passed bool) string {
	if passed {
		return "PASS"
	}
	return "FAIL"
}

func passedChecks(checks []CheckResult) int {
	passed := 0
	for _, check := range checks {
		if check.Passed {
			passed++
		}
	}
	return passed
}
