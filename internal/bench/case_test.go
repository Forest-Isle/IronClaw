package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadCasesValidatesAndDefaultsName(t *testing.T) {
	root := t.TempDir()
	caseDir := filepath.Join(root, "rename-config")
	writeTestFile(t, filepath.Join(caseDir, "case.yaml"), `
goal: do the thing
checks:
  - type: file_exists
    path: settings.yaml
`)
	writeTestFile(t, filepath.Join(caseDir, "seed", "config.yaml"), "port: 8080\n")
	if err := os.Mkdir(filepath.Join(root, "ignored"), 0o755); err != nil {
		t.Fatal(err)
	}

	cases, err := LoadCases(root)
	if err != nil {
		t.Fatalf("LoadCases() error = %v", err)
	}
	if len(cases) != 1 {
		t.Fatalf("cases = %d, want 1", len(cases))
	}
	if cases[0].Name != "rename-config" {
		t.Fatalf("name = %q, want directory fallback", cases[0].Name)
	}
	if !filepath.IsAbs(cases[0].SeedDir) {
		t.Fatalf("SeedDir = %q, want absolute path", cases[0].SeedDir)
	}
}

func TestLoadCasesZeroCasesActionableError(t *testing.T) {
	_, err := LoadCases(t.TempDir())
	if err == nil {
		t.Fatal("LoadCases() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "direct child directories containing case.yaml") {
		t.Fatalf("error = %q, want actionable layout", err)
	}
}

func TestLoadCasesValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "missing-goal",
			body: "checks:\n  - type: file_exists\n    path: x\n",
			want: "goal must not be empty",
		},
		{
			name: "missing-checks",
			body: "goal: do it\n",
			want: "checks must not be empty",
		},
		{
			name: "bad-type",
			body: "goal: do it\nchecks:\n  - type: shell\n    path: x\n",
			want: `unsupported type "shell"`,
		},
		{
			name: "bad-path",
			body: "goal: do it\nchecks:\n  - type: file_exists\n    path: ../x\n",
			want: "must not contain ..",
		},
		{
			name: "absolute-path",
			body: "goal: do it\nchecks:\n  - type: file_exists\n    path: /tmp/x\n",
			want: "must be relative",
		},
		{
			name: "missing-value",
			body: "goal: do it\nchecks:\n  - type: content_contains\n    path: x\n",
			want: "requires value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "case", "case.yaml"), tt.body)
			_, err := LoadCases(root)
			if err == nil {
				t.Fatal("LoadCases() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want containing %q", err, tt.want)
			}
		})
	}
}
