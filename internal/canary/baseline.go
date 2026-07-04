package canary

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Report is the serializable result of one canary suite run.
type Report struct {
	Model   string       `json:"model"`
	Results []TaskReport `json:"results"`
}

// TaskReport is the serializable summary of one canary task run.
type TaskReport struct {
	Task         string  `json:"task"`
	Trials       int     `json:"trials"`
	Passes       int     `json:"passes"`
	PassRate     float64 `json:"pass_rate"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
}

// NewReport converts in-memory canary task results into a serializable report.
func NewReport(model string, results []TaskResult) Report {
	report := Report{
		Model:   model,
		Results: make([]TaskReport, 0, len(results)),
	}
	for _, result := range results {
		report.Results = append(report.Results, TaskReport{
			Task:         result.Task,
			Trials:       result.Trials,
			Passes:       result.Passes,
			PassRate:     result.PassRate,
			InputTokens:  result.InputTokens,
			OutputTokens: result.OutputTokens,
		})
	}
	return report
}

// WriteReport writes a canary report as indented JSON.
func WriteReport(path string, r Report) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("canary: marshal report: %w", err)
	}
	data = append(data, '\n')
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("canary: create report dir %q: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("canary: write report %q: %w", path, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		return fmt.Errorf("canary: chmod report %q: %w", path, err)
	}
	return nil
}

// LoadReport reads a canary report from JSON.
func LoadReport(path string) (Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Report{}, fmt.Errorf("canary: read report %q: %w", path, err)
	}
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		return Report{}, fmt.Errorf("canary: decode report %q: %w", path, err)
	}
	return report, nil
}

// CompareBaseline returns per-task regressions: current pass_rate < baseline
// pass_rate - tolerance. Tasks missing from current run count as regressions
// (fail-closed); tasks new in current run are ignored.
func CompareBaseline(baseline, current Report, tolerance float64) []string {
	currentByTask := make(map[string]TaskReport, len(current.Results))
	for _, result := range current.Results {
		currentByTask[result.Task] = result
	}

	var regressions []string
	for _, base := range baseline.Results {
		cur, ok := currentByTask[base.Task]
		if !ok {
			regressions = append(regressions, fmt.Sprintf("%s: baseline %.2f -> current missing", base.Task, base.PassRate))
			continue
		}
		if cur.PassRate < base.PassRate-tolerance {
			regressions = append(regressions, fmt.Sprintf("%s: baseline %.2f -> current %.2f", base.Task, base.PassRate, cur.PassRate))
		}
	}
	return regressions
}
