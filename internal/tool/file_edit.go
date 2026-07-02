package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// FileEditTool edits a file by replacing exact string matches.
type FileEditTool struct {
	approval bool
}

func NewFileEditTool(requiresApproval bool) *FileEditTool {
	return &FileEditTool{approval: requiresApproval}
}

func (t *FileEditTool) Name() string { return "file_edit" }
func (t *FileEditTool) Description() string {
	return "Edit a file by replacing exact string matches. Use old_string to specify the text to find and new_string for the replacement."
}
func (t *FileEditTool) RequiresApproval() bool { return t.approval }
func (t *FileEditTool) IsReadOnly() bool       { return false }

func (t *FileEditTool) Capabilities() ToolCapabilities {
	return ToolCapabilities{
		IsReadOnly:      false,
		IsDestructive:   false,
		RequiresNetwork: false,
		ApprovalMode:    "auto",
		ParallelSafety:  ParallelPathScoped,
		Reversibility:   "reversible",
	}
}

// ExtractPaths returns the target file path for concurrent edit conflict detection.
func (t *FileEditTool) ExtractPaths(input []byte) ([]string, error) {
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

func (t *FileEditTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file to edit",
			},
			"old_string": map[string]any{
				"type":        "string",
				"description": "The exact string to find in the file",
			},
			"new_string": map[string]any{
				"type":        "string",
				"description": "The replacement string",
			},
			"replace_all": map[string]any{
				"type":        "boolean",
				"description": "Replace all occurrences (default: false, replaces only the first occurrence when unique)",
			},
		},
		"required": []string{"path", "old_string", "new_string"},
	}
}

type fileEditInput struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

func (t *FileEditTool) Execute(ctx context.Context, input []byte) (Result, error) {
	var in fileEditInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Error: "invalid input: " + err.Error()}, nil
	}
	if in.Path == "" {
		return Result{Error: "path is required"}, nil
	}
	if in.OldString == in.NewString {
		return Result{Error: "old_string and new_string are identical"}, nil
	}

	path, err := ResolveWorkPath(ctx, in.Path)
	if err != nil {
		return Result{Error: err.Error()}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{Error: err.Error()}, nil
	}
	content := string(data)

	count := strings.Count(content, in.OldString)
	if count == 0 {
		return Result{Error: "old_string not found in file"}, nil
	}
	if count > 1 && !in.ReplaceAll {
		return Result{Error: fmt.Sprintf("old_string found %d times — provide more context to make it unique, or set replace_all=true", count)}, nil
	}

	var newContent string
	replacements := count
	if in.ReplaceAll {
		newContent = strings.ReplaceAll(content, in.OldString, in.NewString)
	} else {
		newContent = strings.Replace(content, in.OldString, in.NewString, 1)
		replacements = 1
	}

	if err := os.WriteFile(path, []byte(newContent), 0o644); err != nil {
		return Result{Error: err.Error()}, nil
	}

	return Result{Output: fmt.Sprintf("edited %s: %d replacement(s) made", path, replacements)}, nil
}
