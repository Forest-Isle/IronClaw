package canary

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Forest-Isle/daimon/internal/agent"
	"github.com/Forest-Isle/daimon/internal/tool"
)

// Task is one golden task: a goal the kernel must achieve inside the sandbox,
// judged afterwards by deterministic checks.
type Task struct {
	// Name is the stable task identifier used in canary triggers and reports.
	Name string
	// Goal is the instruction executed by the episode kernel.
	Goal string
	// Seeds are relative sandbox paths and their initial contents.
	Seeds map[string]string
	// Checks are deterministic assertions evaluated after the trajectory.
	Checks []Check
}

// Check is one deterministic post-trial assertion.
type Check struct {
	// Kind is file_exists, file_absent, file_contains, or outcome_status.
	Kind string
	// Path is the relative sandbox path for file checks.
	Path string
	// Contains is the required substring for file_contains.
	Contains string
	// Status is the exact CognitiveOutcome.Status required by outcome_status.
	Status string
}

// CheckResult is the result of one deterministic post-trial assertion.
type CheckResult struct {
	// Check is the assertion that was evaluated.
	Check Check
	// Passed reports whether Check succeeded.
	Passed bool
	// Detail explains a failed check in actionable terms.
	Detail string
}

// TrialResult is the outcome and deterministic check result for one sandboxed
// trajectory.
type TrialResult struct {
	// Outcome is the CognitiveOutcome returned by the episode kernel.
	Outcome agent.CognitiveOutcome
	// Checks are all deterministic assertion results for this trial.
	Checks []CheckResult
	// Passed is true only when Execute succeeded and every check passed.
	Passed bool
	// Err records an episode execution error without aborting the whole task.
	Err string
	// InputTokens is the total input token count reported by the provider.
	InputTokens int64
	// OutputTokens is the total output token count reported by the provider.
	OutputTokens int64
}

// TaskResult summarizes all trials for one canary task.
type TaskResult struct {
	// Task is the task name.
	Task string
	// Trials is the number of completed trials.
	Trials int
	// Passes is the number of trials whose checks all passed.
	Passes int
	// PassRate is Passes divided by Trials, or 0 when no trials completed.
	PassRate float64
	// InputTokens is the total input token count reported by all trials.
	InputTokens int64
	// OutputTokens is the total output token count reported by all trials.
	OutputTokens int64
	// Results contains one entry for each completed trial.
	Results []TrialResult
}

func evaluateChecks(root string, outcome agent.CognitiveOutcome, checks []Check) []CheckResult {
	results := make([]CheckResult, 0, len(checks))
	for _, check := range checks {
		results = append(results, evaluateCheck(root, outcome, check))
	}
	return results
}

func evaluateCheck(root string, outcome agent.CognitiveOutcome, check Check) CheckResult {
	switch check.Kind {
	case "file_exists":
		path, ok, detail := resolveCheckPath(root, check.Path)
		if !ok {
			return failedCheck(check, detail)
		}
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				return failedCheck(check, fmt.Sprintf("file %q does not exist", check.Path))
			}
			return failedCheck(check, fmt.Sprintf("stat %q: %v", check.Path, err))
		}
		if info.IsDir() {
			return failedCheck(check, fmt.Sprintf("path %q is a directory, not a file", check.Path))
		}
		return passedCheck(check)

	case "file_absent":
		path, ok, detail := resolveCheckPath(root, check.Path)
		if !ok {
			return failedCheck(check, detail)
		}
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				return passedCheck(check)
			}
			return failedCheck(check, fmt.Sprintf("stat %q: %v", check.Path, err))
		}
		return failedCheck(check, fmt.Sprintf("file %q exists", check.Path))

	case "file_contains":
		path, ok, detail := resolveCheckPath(root, check.Path)
		if !ok {
			return failedCheck(check, detail)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return failedCheck(check, fmt.Sprintf("read %q: %v", check.Path, err))
		}
		if !strings.Contains(string(data), check.Contains) {
			return failedCheck(check, fmt.Sprintf("file %q does not contain %q", check.Path, check.Contains))
		}
		return passedCheck(check)

	case "outcome_status":
		if outcome.Status != check.Status {
			return failedCheck(check, fmt.Sprintf("outcome status = %q, want %q", outcome.Status, check.Status))
		}
		return passedCheck(check)

	default:
		return failedCheck(check, fmt.Sprintf("unknown check kind %q", check.Kind))
	}
}

func resolveCheckPath(root, path string) (string, bool, string) {
	resolved, err := tool.ResolveWorkPath(tool.WithWorkDir(context.Background(), root), path)
	if err != nil {
		return "", false, fmt.Sprintf("path %q escapes sandbox root: %v", path, err)
	}
	return resolved, true, ""
}

func passedCheck(check Check) CheckResult {
	return CheckResult{Check: check, Passed: true}
}

func failedCheck(check Check, detail string) CheckResult {
	return CheckResult{Check: check, Passed: false, Detail: detail}
}

func checksPassed(results []CheckResult) bool {
	for _, result := range results {
		if !result.Passed {
			return false
		}
	}
	return true
}
