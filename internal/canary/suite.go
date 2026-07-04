package canary

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed tasks/*.yaml
var defaultSuiteFS embed.FS

var taskNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// LoadSuiteDir loads every *.yaml task file in dir, non-recursively and sorted
// by filename.
func LoadSuiteDir(dir string) ([]Task, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("canary: suite read dir %q: %w", dir, err)
	}

	var files []suiteFile
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		files = append(files, suiteFile{
			name: entry.Name(),
			read: func(name string) ([]byte, error) {
				path := filepath.Join(dir, name)
				data, err := os.ReadFile(path)
				if err != nil {
					return nil, fmt.Errorf("read %q: %w", path, err)
				}
				return data, nil
			},
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })

	tasks, err := loadSuiteFiles(files)
	if err != nil {
		return nil, fmt.Errorf("canary: suite dir %q: %w", dir, err)
	}
	return tasks, nil
}

// DefaultSuite returns the embedded golden task suite v1.
func DefaultSuite() ([]Task, error) {
	entries, err := fs.ReadDir(defaultSuiteFS, "tasks")
	if err != nil {
		return nil, fmt.Errorf("canary: suite default read tasks: %w", err)
	}

	var files []suiteFile
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		files = append(files, suiteFile{
			name: entry.Name(),
			read: func(name string) ([]byte, error) {
				path := filepath.ToSlash(filepath.Join("tasks", name))
				data, err := defaultSuiteFS.ReadFile(path)
				if err != nil {
					return nil, fmt.Errorf("read embedded %q: %w", path, err)
				}
				return data, nil
			},
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })

	tasks, err := loadSuiteFiles(files)
	if err != nil {
		return nil, fmt.Errorf("canary: suite default: %w", err)
	}
	return tasks, nil
}

type suiteFile struct {
	name string
	read func(name string) ([]byte, error)
}

func loadSuiteFiles(files []suiteFile) ([]Task, error) {
	tasks := make([]Task, 0, len(files))
	seen := map[string]string{}
	for _, file := range files {
		data, err := file.read(file.name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file.name, err)
		}

		task, err := decodeTask(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file.name, err)
		}
		if err := validateTask(task); err != nil {
			return nil, fmt.Errorf("%s: %w", file.name, err)
		}
		if previous, ok := seen[task.Name]; ok {
			return nil, fmt.Errorf("%s: task %q duplicates %s", file.name, task.Name, previous)
		}
		seen[task.Name] = file.name
		tasks = append(tasks, task)
	}
	return tasks, nil
}

func decodeTask(data []byte) (Task, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var task Task
	if err := dec.Decode(&task); err != nil {
		return Task{}, fmt.Errorf("decode YAML: %w", err)
	}
	return task, nil
}

func validateTask(t Task) error {
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("task name is required")
	}
	if !taskNamePattern.MatchString(t.Name) {
		return fmt.Errorf("task %q name must be kebab-case", t.Name)
	}
	if strings.TrimSpace(t.Goal) == "" {
		return fmt.Errorf("task %q goal is required", t.Name)
	}
	for path := range t.Seeds {
		if err := validateSeedPath(path); err != nil {
			return fmt.Errorf("task %q seed path %q: %w", t.Name, path, err)
		}
	}
	if len(t.Checks) == 0 {
		return fmt.Errorf("task %q must define at least one check", t.Name)
	}
	for i, check := range t.Checks {
		if err := validateCheck(check); err != nil {
			return fmt.Errorf("task %q check %d: %w", t.Name, i, err)
		}
	}
	return nil
}

func validateCheck(check Check) error {
	switch check.Kind {
	case "file_exists", "file_absent":
		if strings.TrimSpace(check.Path) == "" {
			return fmt.Errorf("%s check requires path", check.Kind)
		}
	case "file_contains":
		if strings.TrimSpace(check.Path) == "" {
			return fmt.Errorf("file_contains check requires path")
		}
		if check.Contains == "" {
			return fmt.Errorf("file_contains check requires contains")
		}
	case "outcome_status":
		if strings.TrimSpace(check.Status) == "" {
			return fmt.Errorf("outcome_status check requires status")
		}
	default:
		return fmt.Errorf("unknown check kind %q", check.Kind)
	}
	return nil
}

func validateSeedPath(path string) error {
	if path == "" {
		return fmt.Errorf("path is required")
	}
	if filepath.IsAbs(path) || strings.HasPrefix(path, "/") {
		return fmt.Errorf("must be relative")
	}
	for _, part := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return fmt.Errorf("must not contain .. segment")
		}
	}
	return nil
}
