package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEvaluateChecksSemantics(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "present.txt"), "hello\nworld\n")
	writeTestFile(t, filepath.Join(dir, "exact.txt"), "same\n")
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}

	checks := []Check{
		{Type: "file_exists", Path: "present.txt"},
		{Type: "file_exists", Path: "missing.txt"},
		{Type: "file_exists", Path: "folder"},
		{Type: "file_absent", Path: "missing.txt"},
		{Type: "file_absent", Path: "present.txt"},
		{Type: "content_equals", Path: "exact.txt", Value: "same"},
		{Type: "content_equals", Path: "exact.txt", Value: "different"},
		{Type: "content_contains", Path: "present.txt", Value: "world"},
		{Type: "content_contains", Path: "present.txt", Value: "absent"},
		{Type: "content_contains", Path: "folder", Value: "x"},
	}

	results := evaluateChecks(dir, checks)
	want := []bool{true, false, false, true, false, true, false, true, false, false}
	if len(results) != len(want) {
		t.Fatalf("results = %d, want %d", len(results), len(want))
	}
	for i, result := range results {
		if result.Passed != want[i] {
			t.Fatalf("check %d passed = %t, want %t (%+v)", i, result.Passed, want[i], result)
		}
		if !result.Passed && strings.TrimSpace(result.Detail) == "" {
			t.Fatalf("check %d failed without detail", i)
		}
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
