package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
)

// FileWriteTool creates or overwrites a file with the given content.
type FileWriteTool struct {
	approval bool
}

func NewFileWriteTool(requiresApproval bool) *FileWriteTool {
	return &FileWriteTool{approval: requiresApproval}
}

func (t *FileWriteTool) Name() string { return "file_write" }
func (t *FileWriteTool) Description() string {
	return "Create or overwrite a file with the given content."
}
func (t *FileWriteTool) RequiresApproval() bool { return t.approval }
func (t *FileWriteTool) IsReadOnly() bool       { return false }

func (t *FileWriteTool) Capabilities() ToolCapabilities {
	return ToolCapabilities{
		IsReadOnly:      false,
		IsDestructive:   false,
		RequiresNetwork: false,
		ApprovalMode:    "auto",
		ParallelSafety:  ParallelPathScoped,
		Reversibility:   "reversible",
	}
}

// ExtractPaths returns the target file path for concurrent write conflict detection.
func (t *FileWriteTool) ExtractPaths(input []byte) ([]string, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	if p := CanonicalizePath(in.Path); p != "" {
		return []string{p}, nil
	}
	return nil, nil
}

func (t *FileWriteTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file to write",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Content to write to the file",
			},
		},
		"required": []string{"path", "content"},
	}
}

type fileWriteInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (t *FileWriteTool) Execute(ctx context.Context, input []byte) (Result, error) {
	var in fileWriteInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Error: "invalid input: " + err.Error()}, nil
	}
	if in.Path == "" {
		return Result{Error: "path is required"}, nil
	}

	path, err := ResolveWorkPath(ctx, in.Path)
	if err != nil {
		return Result{Error: err.Error()}, nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{Error: err.Error()}, nil
	}

	if err := os.WriteFile(path, []byte(in.Content), 0o644); err != nil {
		return Result{Error: err.Error()}, nil
	}

	return Result{Output: "file written: " + path}, nil
}
