package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var unifiedDiffHeaderRE = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// FilePatchTool applies unified diff patches to files.
type FilePatchTool struct {
	workingDir string
}

func NewFilePatchTool(workingDir string) *FilePatchTool {
	return &FilePatchTool{workingDir: workingDir}
}

func (t *FilePatchTool) Name() string { return "file_patch" }
func (t *FilePatchTool) Description() string {
	return "Apply a unified diff patch to a file with verification and dry-run support."
}
func (t *FilePatchTool) RequiresApproval() bool { return false }
func (t *FilePatchTool) IsReadOnly() bool       { return false }

func (t *FilePatchTool) Capabilities() ToolCapabilities {
	return ToolCapabilities{
		IsReadOnly:      false,
		IsDestructive:   false,
		RequiresNetwork: false,
		ApprovalMode:    "auto",
		ParallelSafety:  ParallelPathScoped,
		Reversibility:   "reversible",
	}
}

func (t *FilePatchTool) ExtractPaths(input []byte) ([]string, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	path := in.Path
	if path != "" {
		resolved, err := resolvePathInRoot(t.workingDir, path)
		if err != nil {
			return nil, err
		}
		path = resolved
	}
	if p := CanonicalizePath(path); p != "" {
		return []string{p}, nil
	}
	return nil, nil
}

func (t *FilePatchTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Relative or absolute file path to patch",
			},
			"patch": map[string]any{
				"type":        "string",
				"description": "Unified diff format patch content",
			},
			"dry_run": map[string]any{
				"type":        "boolean",
				"description": "Preview changes without writing the file (default: false)",
			},
		},
		"required": []string{"path", "patch"},
	}
}

type filePatchInput struct {
	Path   string `json:"path"`
	Patch  string `json:"patch"`
	DryRun bool   `json:"dry_run"`
}

type patchHunk struct {
	OldStart int
	OldCount int
	NewStart int
	NewCount int
	Lines    []patchLine
	Header   string
}

type patchLine struct {
	Kind rune
	Text string
}

type patchPreview struct {
	Header    string `json:"header"`
	OldStart  int    `json:"old_start"`
	NewStart  int    `json:"new_start"`
	LineCount int    `json:"line_count"`
}

func (t *FilePatchTool) Execute(ctx context.Context, input []byte) (Result, error) {
	var in filePatchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Error: "invalid input: " + err.Error()}, nil
	}
	if in.Path == "" {
		return Result{Error: "path is required"}, nil
	}
	if in.Patch == "" {
		return Result{
			Output: "no changes applied",
			Metadata: map[string]any{
				"success":  true,
				"warnings": []string{"empty patch"},
			},
		}, nil
	}

	resolvedPath, err := resolvePathInRoot(t.workingDir, in.Path)
	if err != nil {
		return Result{Error: err.Error()}, nil
	}

	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return Result{Error: err.Error()}, nil
	}

	hunks, warnings, err := parseUnifiedDiff(in.Patch)
	if err != nil {
		return Result{Error: err.Error()}, nil
	}
	if len(hunks) == 0 {
		warnings = append(warnings, "patch contained no hunks")
		return Result{
			Output: "no changes applied",
			Metadata: map[string]any{
				"success":  true,
				"warnings": warnings,
			},
		}, nil
	}

	originalLines, trailingNewline := splitLines(string(data))
	patchedLines, previews, applyWarnings, err := applyPatchHunks(originalLines, hunks)
	if err != nil {
		return Result{Error: err.Error()}, nil
	}
	warnings = append(warnings, applyWarnings...)

	diffSummary := fmt.Sprintf("%d hunk(s) matched", len(previews))
	if in.DryRun {
		return Result{
			Output: fmt.Sprintf("dry run: %s for %s", diffSummary, resolvedPath),
			Metadata: map[string]any{
				"success":      true,
				"diff_summary": diffSummary,
				"warnings":     warnings,
				"hunks":        previews,
			},
		}, nil
	}

	patchedContent := joinLines(patchedLines, trailingNewline)
	if err := os.WriteFile(resolvedPath, []byte(patchedContent), 0o644); err != nil {
		return Result{Error: err.Error()}, nil
	}

	gitSummary, gitWarning := t.gitDiffStat(ctx, resolvedPath)
	if gitSummary != "" {
		diffSummary = gitSummary
	}
	if gitWarning != "" {
		warnings = append(warnings, gitWarning)
	}

	return Result{
		Output: fmt.Sprintf("patched %s\n%s", resolvedPath, diffSummary),
		Metadata: map[string]any{
			"success":      true,
			"diff_summary": diffSummary,
			"warnings":     warnings,
			"hunks":        previews,
		},
	}, nil
}

func parseUnifiedDiff(patch string) ([]patchHunk, []string, error) {
	lines := strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n")
	hunks := make([]patchHunk, 0)
	warnings := make([]string, 0)
	var current *patchHunk

	flush := func() {
		if current != nil {
			hunks = append(hunks, *current)
			current = nil
		}
	}

	for i, line := range lines {
		if line == "" && i == len(lines)-1 {
			continue
		}
		if strings.HasPrefix(line, "@@ ") || strings.HasPrefix(line, "@@-") || strings.HasPrefix(line, "@@") {
			flush()
			hunk, err := parseHunkHeader(line)
			if err != nil {
				return nil, nil, err
			}
			current = &hunk
			continue
		}
		if current == nil {
			if line != "" && !strings.HasPrefix(line, "--- ") && !strings.HasPrefix(line, "+++ ") && !strings.HasPrefix(line, "diff ") && !strings.HasPrefix(line, "index ") {
				warnings = append(warnings, fmt.Sprintf("ignored non-hunk line: %s", line))
			}
			continue
		}
		if line == `\ No newline at end of file` {
			continue
		}
		if line == "" {
			current.Lines = append(current.Lines, patchLine{Kind: ' ', Text: ""})
			continue
		}
		kind := rune(line[0])
		if kind != ' ' && kind != '+' && kind != '-' {
			return nil, nil, fmt.Errorf("invalid hunk line %q in %s", line, current.Header)
		}
		current.Lines = append(current.Lines, patchLine{Kind: kind, Text: line[1:]})
	}
	flush()

	return hunks, warnings, nil
}

func parseHunkHeader(line string) (patchHunk, error) {
	matches := unifiedDiffHeaderRE.FindStringSubmatch(line)
	if matches == nil {
		return patchHunk{}, fmt.Errorf("invalid unified diff hunk header: %s", line)
	}
	oldStart, _ := strconv.Atoi(matches[1])
	oldCount := 1
	if matches[2] != "" {
		oldCount, _ = strconv.Atoi(matches[2])
	}
	newStart, _ := strconv.Atoi(matches[3])
	newCount := 1
	if matches[4] != "" {
		newCount, _ = strconv.Atoi(matches[4])
	}
	return patchHunk{
		OldStart: oldStart,
		OldCount: oldCount,
		NewStart: newStart,
		NewCount: newCount,
		Header:   line,
	}, nil
}

func applyPatchHunks(lines []string, hunks []patchHunk) ([]string, []patchPreview, []string, error) {
	current := append([]string(nil), lines...)
	previews := make([]patchPreview, 0, len(hunks))
	warnings := make([]string, 0)
	lineDelta := 0

	for _, hunk := range hunks {
		originalSeq, replacementSeq := hunkSequences(hunk)
		baseIndex := hunk.OldStart - 1 + lineDelta
		pos, offsetUsed, err := locateHunk(current, baseIndex, originalSeq)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to apply hunk %s: %w\nexpected lines:\n%s", hunk.Header, err, strings.Join(originalSeq, "\n"))
		}
		if offsetUsed != 0 {
			warnings = append(warnings, fmt.Sprintf("%s applied with offset %+d", hunk.Header, offsetUsed))
		}
		next := make([]string, 0, len(current)-len(originalSeq)+len(replacementSeq))
		next = append(next, current[:pos]...)
		next = append(next, replacementSeq...)
		next = append(next, current[pos+len(originalSeq):]...)
		current = next
		lineDelta += len(replacementSeq) - len(originalSeq)

		previews = append(previews, patchPreview{
			Header:    hunk.Header,
			OldStart:  hunk.OldStart,
			NewStart:  hunk.NewStart,
			LineCount: len(hunk.Lines),
		})
	}

	return current, previews, warnings, nil
}

func hunkSequences(h patchHunk) ([]string, []string) {
	original := make([]string, 0, h.OldCount)
	replacement := make([]string, 0, h.NewCount)
	for _, line := range h.Lines {
		switch line.Kind {
		case ' ':
			original = append(original, line.Text)
			replacement = append(replacement, line.Text)
		case '-':
			original = append(original, line.Text)
		case '+':
			replacement = append(replacement, line.Text)
		}
	}
	return original, replacement
}

func locateHunk(lines []string, baseIndex int, original []string) (int, int, error) {
	if len(original) == 0 {
		pos := clamp(baseIndex, 0, len(lines))
		return pos, pos - baseIndex, nil
	}

	candidates := []int{baseIndex, baseIndex - 1, baseIndex + 1}
	seen := make(map[int]struct{}, len(candidates))
	for _, candidate := range candidates {
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		if candidate < 0 || candidate+len(original) > len(lines) {
			continue
		}
		if matchesAt(lines, candidate, original) {
			return candidate, candidate - baseIndex, nil
		}
	}
	return 0, 0, fmt.Errorf("context mismatch near line %d", baseIndex+1)
}

func matchesAt(lines []string, idx int, original []string) bool {
	if idx < 0 || idx+len(original) > len(lines) {
		return false
	}
	for i := range original {
		if lines[idx+i] != original[i] {
			return false
		}
	}
	return true
}

func splitLines(content string) ([]string, bool) {
	if content == "" {
		return nil, false
	}
	trailingNewline := strings.HasSuffix(content, "\n")
	parts := strings.Split(content, "\n")
	if trailingNewline {
		parts = parts[:len(parts)-1]
	}
	return parts, trailingNewline
}

func joinLines(lines []string, trailingNewline bool) string {
	if len(lines) == 0 {
		return ""
	}
	content := strings.Join(lines, "\n")
	if trailingNewline {
		content += "\n"
	}
	return content
}

func clamp(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func (t *FilePatchTool) gitDiffStat(ctx context.Context, resolvedPath string) (string, string) {
	diffPath := resolvedPath
	if rel, err := filepath.Rel(t.workingDir, resolvedPath); err == nil {
		diffPath = rel
	}
	cmd := exec.CommandContext(ctx, "git", "diff", "--stat", "--", diffPath)
	cmd.Dir = t.workingDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", "git diff --stat verification failed: " + msg
	}
	return strings.TrimSpace(string(out)), ""
}
