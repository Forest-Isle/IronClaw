package canary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultSuiteLoadsGoldenTasks(t *testing.T) {
	tasks, err := DefaultSuite()
	if err != nil {
		t.Fatalf("DefaultSuite: %v", err)
	}
	if len(tasks) != 12 {
		t.Fatalf("tasks = %d, want 12", len(tasks))
	}

	wantNames := []string{
		"copy-preserve",
		"edit-replace",
		"extract-json-field",
		"follow-instruction-file",
		"honest-blocked",
		"list-inventory",
		"merge-two-files",
		"multi-edit",
		"nested-path",
		"read-then-derive",
		"restraint",
		"write-file",
	}
	seen := map[string]bool{}
	for i, task := range tasks {
		if task.Name != wantNames[i] {
			t.Fatalf("task[%d].Name = %q, want %q", i, task.Name, wantNames[i])
		}
		if seen[task.Name] {
			t.Fatalf("duplicate task name %q", task.Name)
		}
		seen[task.Name] = true
		if err := validateTask(task); err != nil {
			t.Fatalf("validateTask(%q): %v", task.Name, err)
		}
		if got := task.Checks[len(task.Checks)-1]; got.Kind != "outcome_status" {
			t.Fatalf("%s final check = %+v, want outcome_status", task.Name, got)
		}
	}
}

func TestLoadSuiteDirParsesTask(t *testing.T) {
	dir := t.TempDir()
	writeSuiteFile(t, dir, "task.yaml", `
name: parse-task
goal: |
  Copy the marker from source.txt into result.txt.
seeds:
  source.txt: "zx-marker-parse-19q"
checks:
  - kind: file_contains
    path: result.txt
    contains: "zx-marker-parse-19q"
  - kind: outcome_status
    status: done
`)

	tasks, err := LoadSuiteDir(dir)
	if err != nil {
		t.Fatalf("LoadSuiteDir: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(tasks))
	}
	task := tasks[0]
	if task.Name != "parse-task" {
		t.Fatalf("Name = %q, want parse-task", task.Name)
	}
	if strings.TrimSpace(task.Goal) != "Copy the marker from source.txt into result.txt." {
		t.Fatalf("Goal = %q", task.Goal)
	}
	if task.Seeds["source.txt"] != "zx-marker-parse-19q" {
		t.Fatalf("Seeds = %#v", task.Seeds)
	}
	if len(task.Checks) != 2 || task.Checks[0].Contains != "zx-marker-parse-19q" || task.Checks[1].Status != "done" {
		t.Fatalf("Checks = %#v", task.Checks)
	}
}

func TestLoadSuiteDirValidationFailures(t *testing.T) {
	tests := []struct {
		name     string
		files    map[string]string
		contains []string
	}{
		{
			name: "duplicate name",
			files: map[string]string{
				"01.yaml": validSuiteYAML("dupe-task"),
				"02.yaml": validSuiteYAML("dupe-task"),
			},
			contains: []string{"02.yaml", "dupe-task"},
		},
		{
			name: "empty goal",
			files: map[string]string{
				"bad.yaml": `
name: empty-goal
goal: ""
checks:
  - kind: outcome_status
    status: done
`,
			},
			contains: []string{"bad.yaml", "empty-goal", "goal"},
		},
		{
			name: "unknown check kind",
			files: map[string]string{
				"bad.yaml": `
name: bad-check
goal: Do a thing.
checks:
  - kind: not_real
`,
			},
			contains: []string{"bad.yaml", "bad-check", "unknown check kind"},
		},
		{
			name: "unknown yaml field",
			files: map[string]string{
				"bad.yaml": `
name: unknown-field
goal: Do a thing.
unexpected: true
checks:
  - kind: outcome_status
    status: done
`,
			},
			contains: []string{"bad.yaml", "unexpected"},
		},
		{
			name: "seed traversal",
			files: map[string]string{
				"bad.yaml": `
name: bad-seed
goal: Do a thing.
seeds:
  ../escape.txt: "nope"
checks:
  - kind: outcome_status
    status: done
`,
			},
			contains: []string{"bad.yaml", "bad-seed", "seed", ".."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range tt.files {
				writeSuiteFile(t, dir, name, content)
			}

			_, err := LoadSuiteDir(dir)
			if err == nil {
				t.Fatalf("LoadSuiteDir returned nil error")
			}
			msg := err.Error()
			for _, want := range tt.contains {
				if !strings.Contains(msg, want) {
					t.Fatalf("error = %q, want substring %q", msg, want)
				}
			}
		})
	}
}

func TestLoadSuiteDirSortsByFilename(t *testing.T) {
	dir := t.TempDir()
	writeSuiteFile(t, dir, "20-second.yaml", validSuiteYAML("second-task"))
	writeSuiteFile(t, dir, "10-first.yaml", validSuiteYAML("first-task"))

	tasks, err := LoadSuiteDir(dir)
	if err != nil {
		t.Fatalf("LoadSuiteDir: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("tasks = %d, want 2", len(tasks))
	}
	if tasks[0].Name != "first-task" || tasks[1].Name != "second-task" {
		t.Fatalf("task order = [%s, %s], want filename order", tasks[0].Name, tasks[1].Name)
	}
}

func writeSuiteFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(strings.TrimLeft(content, "\n")), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func validSuiteYAML(name string) string {
	return `
name: ` + name + `
goal: Do the requested deterministic task.
checks:
  - kind: outcome_status
    status: done
`
}
