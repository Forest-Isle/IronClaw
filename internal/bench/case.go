package bench

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const caseFileName = "case.yaml"

// Case is one behavior bench scenario loaded from a case directory.
type Case struct {
	Name    string
	Goal    string
	SeedDir string
	Checks  []Check
}

type caseFile struct {
	Name   string  `yaml:"name"`
	Goal   string  `yaml:"goal"`
	Checks []Check `yaml:"checks"`
}

// LoadCases loads behavior bench cases from direct child directories of dir.
func LoadCases(dir string) ([]Case, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read cases directory: %w", err)
	}
	cases := make([]Case, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		c, ok, err := loadCaseDir(filepath.Join(dir, entry.Name()), entry.Name())
		if err != nil {
			return nil, err
		}
		if ok {
			cases = append(cases, c)
		}
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("no behavior bench cases found in %s: expected direct child directories containing case.yaml, optionally with seed/", dir)
	}
	return cases, nil
}

func loadCaseDir(path, dirName string) (Case, bool, error) {
	data, err := os.ReadFile(filepath.Join(path, caseFileName))
	if os.IsNotExist(err) {
		return Case{}, false, nil
	}
	if err != nil {
		return Case{}, false, fmt.Errorf("read case %s: %w", dirName, err)
	}
	var raw caseFile
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Case{}, false, fmt.Errorf("parse case %s: %w", dirName, err)
	}
	name := strings.TrimSpace(raw.Name)
	if name == "" {
		name = dirName
	}
	c := Case{Name: name, Goal: raw.Goal, Checks: raw.Checks}
	if err := fillSeedDir(path, &c); err != nil {
		return Case{}, false, fmt.Errorf("load case %s: %w", name, err)
	}
	if err := validateCase(c); err != nil {
		return Case{}, false, fmt.Errorf("load case %s: %w", name, err)
	}
	return c, true, nil
}

func fillSeedDir(caseDir string, c *Case) error {
	seed := filepath.Join(caseDir, "seed")
	info, err := os.Stat(seed)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat seed dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("seed is not a directory")
	}
	abs, err := filepath.Abs(seed)
	if err != nil {
		return fmt.Errorf("resolve seed dir: %w", err)
	}
	c.SeedDir = abs
	return nil
}

func validateCase(c Case) error {
	if strings.TrimSpace(c.Goal) == "" {
		return fmt.Errorf("goal must not be empty")
	}
	if len(c.Checks) == 0 {
		return fmt.Errorf("checks must not be empty")
	}
	for i, check := range c.Checks {
		if err := validateCheck(check); err != nil {
			return fmt.Errorf("check %d: %w", i, err)
		}
	}
	return nil
}

func validateCheck(check Check) error {
	switch check.Type {
	case "file_exists", "file_absent":
	case "content_equals", "content_contains":
		if check.Value == "" {
			return fmt.Errorf("%s requires value", check.Type)
		}
	default:
		return fmt.Errorf("unsupported type %q", check.Type)
	}
	if err := validateCheckPath(check.Path); err != nil {
		return fmt.Errorf("path: %w", err)
	}
	return nil
}

func validateCheckPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("path must not be empty")
	}
	if filepath.IsAbs(path) {
		return fmt.Errorf("path %q must be relative", path)
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return fmt.Errorf("path %q must not contain ..", path)
		}
	}
	return nil
}
