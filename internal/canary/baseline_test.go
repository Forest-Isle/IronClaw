package canary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReportWriteLoadRoundTrip(t *testing.T) {
	report := NewReport("model-a", []TaskResult{
		{Task: "write-file", Trials: 3, Passes: 2, PassRate: 2.0 / 3.0, InputTokens: 100, OutputTokens: 20},
		{Task: "edit-file", Trials: 3, Passes: 3, PassRate: 1.0, InputTokens: 200, OutputTokens: 40},
	})
	path := filepath.Join(t.TempDir(), "nested", "report.json")

	if err := WriteReport(path, report); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat report: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("mode = %o, want 0644", got)
	}

	got, err := LoadReport(path)
	if err != nil {
		t.Fatalf("LoadReport: %v", err)
	}
	if got.Model != report.Model || len(got.Results) != 2 {
		t.Fatalf("loaded report = %+v, want %+v", got, report)
	}
	if got.Results[0] != report.Results[0] || got.Results[1] != report.Results[1] {
		t.Fatalf("loaded results = %+v, want %+v", got.Results, report.Results)
	}
}

func TestCompareBaselineDetectsRegressions(t *testing.T) {
	baseline := Report{Results: []TaskReport{
		{Task: "stable", PassRate: 1.00},
		{Task: "regressed", PassRate: 1.00},
	}}
	current := Report{Results: []TaskReport{
		{Task: "stable", PassRate: 0.67},
		{Task: "regressed", PassRate: 0.33},
	}}

	regressions := CompareBaseline(baseline, current, 0.34)

	if len(regressions) != 1 {
		t.Fatalf("regressions = %#v, want one", regressions)
	}
	if !strings.Contains(regressions[0], "regressed: baseline 1.00 -> current 0.33") {
		t.Fatalf("regression = %q, want actionable task delta", regressions[0])
	}
}

func TestCompareBaselineMissingCurrentTaskFailsClosed(t *testing.T) {
	baseline := Report{Results: []TaskReport{{Task: "must-run", PassRate: 0.75}}}
	current := Report{Results: []TaskReport{{Task: "other-task", PassRate: 1.00}}}

	regressions := CompareBaseline(baseline, current, 0.10)

	if len(regressions) != 1 {
		t.Fatalf("regressions = %#v, want missing task regression", regressions)
	}
	if !strings.Contains(regressions[0], "must-run") || !strings.Contains(regressions[0], "missing") {
		t.Fatalf("regression = %q, want missing-task detail", regressions[0])
	}
}

func TestCompareBaselineIgnoresNewCurrentTasks(t *testing.T) {
	baseline := Report{Results: []TaskReport{{Task: "known", PassRate: 0.50}}}
	current := Report{Results: []TaskReport{
		{Task: "known", PassRate: 0.50},
		{Task: "new-task", PassRate: 0.00},
	}}

	regressions := CompareBaseline(baseline, current, 0.00)

	if len(regressions) != 0 {
		t.Fatalf("regressions = %#v, want none", regressions)
	}
}

func TestCompareBaselineToleranceBoundaryIsNotRegression(t *testing.T) {
	baseline := Report{Results: []TaskReport{{Task: "boundary", PassRate: 1.00}}}
	current := Report{Results: []TaskReport{{Task: "boundary", PassRate: 0.66}}}

	regressions := CompareBaseline(baseline, current, 0.34)

	if len(regressions) != 0 {
		t.Fatalf("regressions = %#v, want none at exact tolerance boundary", regressions)
	}
}
