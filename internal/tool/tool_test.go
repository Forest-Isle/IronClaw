package tool

import (
	"context"
	"testing"
	"time"

	"github.com/Forest-Isle/daimon/internal/world"
)

// mockReadOnlyTool implements both Tool and ReadOnlyTool.
type mockReadOnlyTool struct {
	readOnly bool
}

func (m *mockReadOnlyTool) Name() string                                        { return "mock_readonly" }
func (m *mockReadOnlyTool) Description() string                                 { return "mock" }
func (m *mockReadOnlyTool) InputSchema() map[string]any                         { return nil }
func (m *mockReadOnlyTool) Execute(_ context.Context, _ []byte) (Result, error) { return Result{}, nil }
func (m *mockReadOnlyTool) RequiresApproval() bool                              { return false }
func (m *mockReadOnlyTool) IsReadOnly() bool                                    { return m.readOnly }

// mockBasicTool implements only Tool (no ReadOnlyTool).
type mockBasicTool struct{}

func (m *mockBasicTool) Name() string                                        { return "mock_basic" }
func (m *mockBasicTool) Description() string                                 { return "mock" }
func (m *mockBasicTool) InputSchema() map[string]any                         { return nil }
func (m *mockBasicTool) Execute(_ context.Context, _ []byte) (Result, error) { return Result{}, nil }
func (m *mockBasicTool) RequiresApproval() bool                              { return false }

type stubSkillProvider struct{}

func (stubSkillProvider) GetContent(string) (string, error) { return "", nil }
func (stubSkillProvider) ListNames() []string               { return nil }

func TestIsToolReadOnly(t *testing.T) {
	tests := []struct {
		name     string
		tool     Tool
		expected bool
	}{
		{"read-only tool returning true", &mockReadOnlyTool{readOnly: true}, true},
		{"read-only tool returning false", &mockReadOnlyTool{readOnly: false}, false},
		{"basic tool without ReadOnlyTool interface", &mockBasicTool{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsToolReadOnly(tt.tool)
			if got != tt.expected {
				t.Errorf("IsToolReadOnly() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestResultDefaults(t *testing.T) {
	// Zero-value Result should behave like current behavior
	r := Result{Output: "hello", Error: ""}
	if r.Type != "" {
		t.Errorf("default Type should be empty string (zero value), got %q", r.Type)
	}
	if r.IsPartial {
		t.Error("default IsPartial should be false")
	}
	if r.Metadata != nil {
		t.Error("default Metadata should be nil")
	}
	if r.FilePath != "" {
		t.Error("default FilePath should be empty")
	}
}

func TestResultWithMetadata(t *testing.T) {
	r := Result{
		Output:   "data",
		Type:     ResultFile,
		FilePath: "/tmp/test.txt",
		Metadata: map[string]any{"size": 1024},
	}
	if r.Type != ResultFile {
		t.Errorf("Type = %q, want %q", r.Type, ResultFile)
	}
	if r.FilePath != "/tmp/test.txt" {
		t.Errorf("FilePath = %q, want /tmp/test.txt", r.FilePath)
	}
	if r.Metadata["size"] != 1024 {
		t.Errorf("Metadata[size] = %v, want 1024", r.Metadata["size"])
	}
}

func TestGetCapabilities(t *testing.T) {
	// Tool with CapableTool interface
	bash := NewBashTool(0, false, NewPolicy(nil))
	caps := GetCapabilities(bash)
	if !caps.IsDestructive {
		t.Error("BashTool should be destructive")
	}
	if caps.IsReadOnly {
		t.Error("BashTool should not be read-only")
	}

	// Tool without any optional interface
	basic := &mockBasicTool{}
	caps = GetCapabilities(basic)
	if caps.IsReadOnly {
		t.Error("basic tool should not be read-only")
	}
	if caps.ApprovalMode != "auto" {
		t.Errorf("default ApprovalMode = %q, want auto", caps.ApprovalMode)
	}
}

func TestBuiltinToolReversibilityCoverage(t *testing.T) {
	dynamicClassified := map[string]bool{
		"bash": true,
		"http": true,
	}
	validReversibility := map[string]bool{
		"reversible":   true,
		"compensable":  true,
		"irreversible": true,
	}
	tools := []Tool{
		NewFileReadTool(),
		NewFileListTool(),
		NewFileWriteTool(false),
		NewFileEditTool(false),
		NewFilePatchTool("."),
		NewWorldReadTool(nil, world.Identity{}),
		NewWorldEditTool(world.Identity{}),
		NewCommitmentTool(nil),
		NewSendEmailTool("smtp.example.com", 587, "agent@example.com", "password", "", false),
		NewMemoryTool(nil, nil),
		NewValuesTool(nil),
		NewBashTool(time.Second, false, NewPolicy(nil)),
		NewHTTPTool(time.Second, false),
		NewSkillTool(stubSkillProvider{}),
		NewTestRunTool("."),
	}

	for _, tt := range tools {
		t.Run(tt.Name(), func(t *testing.T) {
			caps := GetCapabilities(tt)
			if caps.IsReadOnly || dynamicClassified[tt.Name()] {
				return
			}
			if !validReversibility[caps.Reversibility] {
				t.Fatalf("%s reversibility = %q, want one of reversible/compensable/irreversible", tt.Name(), caps.Reversibility)
			}
		})
	}
}

// mockAvailableTool implements Tool + AvailableTool.
type mockAvailableTool struct {
	available bool
	name      string
}

func (m *mockAvailableTool) Name() string                { return m.name }
func (m *mockAvailableTool) Description() string         { return "mock" }
func (m *mockAvailableTool) InputSchema() map[string]any { return nil }
func (m *mockAvailableTool) Execute(_ context.Context, _ []byte) (Result, error) {
	return Result{}, nil
}
func (m *mockAvailableTool) RequiresApproval() bool { return false }
func (m *mockAvailableTool) Available() bool        { return m.available }

func TestIsToolAvailable(t *testing.T) {
	tests := []struct {
		name     string
		tool     Tool
		expected bool
	}{
		{"available tool", &mockAvailableTool{available: true, name: "a"}, true},
		{"unavailable tool", &mockAvailableTool{available: false, name: "b"}, false},
		{"tool without AvailableTool interface", &mockBasicTool{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsToolAvailable(tt.tool)
			if got != tt.expected {
				t.Errorf("IsToolAvailable() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestRegistryFiltersUnavailable(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&mockAvailableTool{available: true, name: "tool_a"})
	reg.Register(&mockAvailableTool{available: false, name: "tool_b"})
	reg.Register(&mockBasicTool{})

	tools := reg.All()
	// tool_b should be excluded, tool_a and mock_basic should be present
	if len(tools) != 2 {
		t.Fatalf("expected 2 available tools, got %d", len(tools))
	}

	// Get should fail for unavailable tool
	_, err := reg.Get("tool_b")
	if err == nil {
		t.Error("expected error for unavailable tool")
	}

	// Get should succeed for available tool
	_, err = reg.Get("tool_a")
	if err != nil {
		t.Errorf("unexpected error for available tool: %v", err)
	}
}

func TestBuiltinToolsReadOnly(t *testing.T) {
	// BashTool should not be read-only
	bash := NewBashTool(0, false, NewPolicy(nil))
	if IsToolReadOnly(bash) {
		t.Error("BashTool should not be read-only")
	}

	// FileWriteTool should not be read-only
	file := NewFileWriteTool(false)
	if IsToolReadOnly(file) {
		t.Error("FileWriteTool should not be read-only")
	}

	// FileReadTool should be read-only
	fileRead := NewFileReadTool()
	if !IsToolReadOnly(fileRead) {
		t.Error("FileReadTool should be read-only")
	}

	// HTTPTool should not be read-only
	httpTool := NewHTTPTool(0, false)
	if IsToolReadOnly(httpTool) {
		t.Error("HTTPTool should not be read-only")
	}
}

func TestPathScopedToolImplementation(t *testing.T) {
	// file_write, file_edit, and file_patch should implement PathScopedTool
	writeT := NewFileWriteTool(false)
	editT := NewFileEditTool(false)
	patchT := NewFilePatchTool(".")

	// Verify they implement the interface
	var _ PathScopedTool = writeT
	var _ PathScopedTool = editT
	var _ PathScopedTool = patchT

	// Verify Capabilities return ParallelPathScoped
	if caps := GetCapabilities(writeT); caps.ParallelSafety != ParallelPathScoped {
		t.Errorf("FileWriteTool.ParallelSafety = %q, want %q", caps.ParallelSafety, ParallelPathScoped)
	}
	if caps := GetCapabilities(editT); caps.ParallelSafety != ParallelPathScoped {
		t.Errorf("FileEditTool.ParallelSafety = %q, want %q", caps.ParallelSafety, ParallelPathScoped)
	}
	if caps := GetCapabilities(patchT); caps.ParallelSafety != ParallelPathScoped {
		t.Errorf("FilePatchTool.ParallelSafety = %q, want %q", caps.ParallelSafety, ParallelPathScoped)
	}

	// Verify ExtractPaths works correctly
	input := []byte(`{"path": "/tmp/test.go", "content": "hello"}`)
	paths, err := writeT.ExtractPaths(input)
	if err != nil {
		t.Fatalf("FileWriteTool.ExtractPaths error: %v", err)
	}
	if len(paths) != 1 || paths[0] != "/tmp/test.go" {
		t.Errorf("FileWriteTool.ExtractPaths = %v, want [/tmp/test.go]", paths)
	}

	// Verify path canonicalization (relative → absolute)
	relInput := []byte(`{"path": "relative/file.go", "old_string": "a", "new_string": "b"}`)
	paths, err = editT.ExtractPaths(relInput)
	if err != nil {
		t.Fatalf("FileEditTool.ExtractPaths error: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("expected 1 path, got %d", len(paths))
	}
	// Should be absolute
	if paths[0] == "relative/file.go" {
		t.Error("ExtractPaths should return canonical absolute path, got relative path")
	}

	patchInput := []byte(`{"path": "relative/test.go", "patch": "@@ -1 +1 @@\n-a\n+b"}`)
	paths, err = patchT.ExtractPaths(patchInput)
	if err != nil {
		t.Fatalf("FilePatchTool.ExtractPaths error: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("expected 1 patch path, got %d", len(paths))
	}
	if paths[0] == "relative/test.go" {
		t.Errorf("FilePatchTool.ExtractPaths should canonicalize relative paths, got %v", paths)
	}

	// Empty path should return nil
	emptyInput := []byte(`{"path": ""}`)
	paths, err = writeT.ExtractPaths(emptyInput)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if paths != nil {
		t.Errorf("expected nil for empty path, got %v", paths)
	}

	// Invalid JSON should return error
	_, err = writeT.ExtractPaths([]byte(`{invalid}`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestReadOnlyToolsNotPathScoped(t *testing.T) {
	// file_read and file_list should remain ParallelSafe (read-only, no conflict possible)
	readT := NewFileReadTool()
	listT := NewFileListTool()

	if caps := GetCapabilities(readT); caps.ParallelSafety != ParallelSafe {
		t.Errorf("FileReadTool.ParallelSafety = %q, want %q", caps.ParallelSafety, ParallelSafe)
	}
	if caps := GetCapabilities(listT); caps.ParallelSafety != ParallelSafe {
		t.Errorf("FileListTool.ParallelSafety = %q, want %q", caps.ParallelSafety, ParallelSafe)
	}

	// They should NOT implement PathScopedTool
	if _, ok := Tool(readT).(PathScopedTool); ok {
		t.Error("FileReadTool should NOT implement PathScopedTool")
	}
	if _, ok := Tool(listT).(PathScopedTool); ok {
		t.Error("FileListTool should NOT implement PathScopedTool")
	}
}

func TestCanonicalizePath(t *testing.T) {
	// Empty string
	if got := CanonicalizePath(""); got != "" {
		t.Errorf("CanonicalizePath(\"\") = %q, want \"\"", got)
	}

	// Absolute path stays absolute
	if got := CanonicalizePath("/tmp/test.go"); got != "/tmp/test.go" {
		t.Errorf("CanonicalizePath(\"/tmp/test.go\") = %q, want \"/tmp/test.go\"", got)
	}

	// Dot-dot resolution
	got := CanonicalizePath("/tmp/foo/../test.go")
	if got != "/tmp/test.go" {
		t.Errorf("CanonicalizePath(\"/tmp/foo/../test.go\") = %q, want \"/tmp/test.go\"", got)
	}
}
