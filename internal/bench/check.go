package bench

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Check describes one declarative assertion against a bench fork directory.
type Check struct {
	Type  string `yaml:"type"`
	Path  string `yaml:"path"`
	Value string `yaml:"value"`
}

// CheckResult is the outcome of evaluating one Check.
type CheckResult struct {
	Check  Check
	Passed bool
	Detail string
}

func evaluateChecks(forkDir string, checks []Check) []CheckResult {
	results := make([]CheckResult, 0, len(checks))
	for _, check := range checks {
		results = append(results, evaluateCheck(forkDir, check))
	}
	return results
}

func evaluateCheck(forkDir string, check Check) CheckResult {
	path := filepath.Join(forkDir, check.Path)
	switch check.Type {
	case "file_exists":
		return checkFileExists(check, path)
	case "file_absent":
		return checkFileAbsent(check, path)
	case "content_equals":
		return checkContentEquals(check, path)
	case "content_contains":
		return checkContentContains(check, path)
	default:
		return CheckResult{Check: check, Detail: "unsupported check type"}
	}
}

func checkFileExists(check Check, path string) CheckResult {
	info, err := os.Stat(path)
	if err != nil {
		return CheckResult{Check: check, Detail: fmt.Sprintf("stat failed: %v", err)}
	}
	if !info.Mode().IsRegular() {
		return CheckResult{Check: check, Detail: "path exists but is not a regular file"}
	}
	return CheckResult{Check: check, Passed: true}
}

func checkFileAbsent(check Check, path string) CheckResult {
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return CheckResult{Check: check, Passed: true}
	}
	if err != nil {
		return CheckResult{Check: check, Detail: fmt.Sprintf("stat failed: %v", err)}
	}
	return CheckResult{Check: check, Detail: "path exists"}
}

func checkContentEquals(check Check, path string) CheckResult {
	data, err := os.ReadFile(path)
	if err != nil {
		return CheckResult{Check: check, Detail: fmt.Sprintf("read failed: %v", err)}
	}
	got := strings.TrimRight(string(data), "\n")
	want := strings.TrimRight(check.Value, "\n")
	if got != want {
		return CheckResult{Check: check, Detail: "content did not equal expected value"}
	}
	return CheckResult{Check: check, Passed: true}
}

func checkContentContains(check Check, path string) CheckResult {
	data, err := os.ReadFile(path)
	if err != nil {
		return CheckResult{Check: check, Detail: fmt.Sprintf("read failed: %v", err)}
	}
	if !strings.Contains(string(data), check.Value) {
		return CheckResult{Check: check, Detail: "content did not contain expected value"}
	}
	return CheckResult{Check: check, Passed: true}
}
