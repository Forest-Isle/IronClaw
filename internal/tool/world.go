package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Forest-Isle/daimon/internal/vcs"
	"github.com/Forest-Isle/daimon/internal/world"
	"github.com/google/uuid"
)

var (
	validCommitmentKinds  = map[string]struct{}{"project": {}, "promise": {}, "deadline": {}, "watch": {}, "routine": {}}
	validCommitmentStates = map[string]struct{}{"active": {}, "waiting": {}, "done": {}, "dropped": {}}
)

// WorldReadTool exposes identity, commitment, and journal world state.
type WorldReadTool struct {
	store    *world.Store
	identity world.Identity
}

func NewWorldReadTool(store *world.Store, identity world.Identity) *WorldReadTool {
	return &WorldReadTool{store: store, identity: identity}
}

func (t *WorldReadTool) Name() string { return "world_read" }
func (t *WorldReadTool) Description() string {
	return "Read world state: identity digest, active commitments digest, or recent journal entries."
}
func (t *WorldReadTool) RequiresApproval() bool { return false }
func (t *WorldReadTool) IsReadOnly() bool       { return true }
func (t *WorldReadTool) Capabilities() ToolCapabilities {
	return ToolCapabilities{
		IsReadOnly:      true,
		IsDestructive:   false,
		RequiresNetwork: false,
		ApprovalMode:    "never",
		ParallelSafety:  ParallelSafe,
	}
}

func (t *WorldReadTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"section": map[string]any{
				"type":        "string",
				"enum":        []string{"identity", "commitments", "journal"},
				"description": "World section to read (default: commitments)",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum journal entries to return (journal only; default: 20)",
			},
			"due_within": map[string]any{
				"type":        "string",
				"description": "Only include commitments due at or before this timestamp (commitments only)",
			},
		},
	}
}

type worldReadInput struct {
	Section   string `json:"section"`
	Limit     int    `json:"limit"`
	DueWithin string `json:"due_within"`
}

func (t *WorldReadTool) Execute(ctx context.Context, input []byte) (Result, error) {
	var in worldReadInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Error: "world_read: invalid input: " + err.Error()}, nil
	}
	section := strings.TrimSpace(in.Section)
	if section == "" {
		section = "commitments"
	}

	switch section {
	case "identity":
		return Result{Output: t.identity.Digest()}, nil
	case "commitments":
		out, err := t.store.CommitmentsDigest(ctx, in.DueWithin)
		if err != nil {
			return Result{Error: "world_read commitments: " + err.Error()}, nil
		}
		return Result{Output: out}, nil
	case "journal":
		entries, err := t.store.ListJournal(ctx, "", in.Limit)
		if err != nil {
			return Result{Error: "world_read journal: " + err.Error()}, nil
		}
		return Result{Output: formatJournalEntries(entries)}, nil
	default:
		return Result{Error: fmt.Sprintf("world_read: unknown section %q (valid: identity, commitments, journal)", section)}, nil
	}
}

// CommitmentTool creates, updates, and lists commitments.
type CommitmentTool struct {
	store *world.Store
}

func NewCommitmentTool(store *world.Store) *CommitmentTool {
	return &CommitmentTool{store: store}
}

func (t *CommitmentTool) Name() string { return "commitment" }
func (t *CommitmentTool) Description() string {
	return "Create, update, or list commitments in the world model."
}
func (t *CommitmentTool) RequiresApproval() bool { return false }
func (t *CommitmentTool) IsReadOnly() bool       { return false }
func (t *CommitmentTool) Capabilities() ToolCapabilities {
	return ToolCapabilities{
		IsReadOnly:      false,
		IsDestructive:   false,
		RequiresNetwork: false,
		ApprovalMode:    "auto",
		ParallelSafety:  ParallelNever,
		Reversibility:   "reversible",
	}
}

func (t *CommitmentTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"create", "update", "list"},
				"description": "Commitment action to perform",
			},
			"kind": map[string]any{
				"type":        "string",
				"enum":        []string{"project", "promise", "deadline", "watch", "routine"},
				"description": "Commitment kind (required for create)",
			},
			"title": map[string]any{
				"type":        "string",
				"description": "Commitment title (required for create; optional for update)",
			},
			"body": map[string]any{
				"type":        "string",
				"description": "Commitment body/details",
			},
			"due_at": map[string]any{
				"type":        "string",
				"description": "Due timestamp, or empty string to clear on update",
			},
			"horizon": map[string]any{
				"type":        "string",
				"description": "Planning horizon",
			},
			"id": map[string]any{
				"type":        "string",
				"description": "Commitment ID (required for update)",
			},
			"state": map[string]any{
				"type":        "string",
				"enum":        []string{"active", "waiting", "done", "dropped"},
				"description": "Commitment state (update only)",
			},
			"states": map[string]any{
				"type":        "array",
				"description": "States to include when listing commitments",
				"items": map[string]any{
					"type": "string",
					"enum": []string{"active", "waiting", "done", "dropped"},
				},
			},
		},
		"required": []string{"action"},
	}
}

type commitmentInput struct {
	Action  string   `json:"action"`
	Kind    string   `json:"kind"`
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	DueAt   string   `json:"due_at"`
	Horizon string   `json:"horizon"`
	ID      string   `json:"id"`
	State   string   `json:"state"`
	States  []string `json:"states"`
}

func (t *CommitmentTool) Execute(ctx context.Context, input []byte) (Result, error) {
	var in commitmentInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Error: "commitment: invalid input: " + err.Error()}, nil
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(input, &raw); err != nil {
		return Result{Error: "commitment: invalid input: " + err.Error()}, nil
	}

	switch strings.TrimSpace(in.Action) {
	case "create":
		return t.handleCreate(ctx, in)
	case "update":
		return t.handleUpdate(ctx, in, raw)
	case "list":
		return t.handleList(ctx, in)
	default:
		return Result{Error: fmt.Sprintf("commitment: unknown action %q (valid: create, update, list)", in.Action)}, nil
	}
}

func (t *CommitmentTool) handleCreate(ctx context.Context, in commitmentInput) (Result, error) {
	kind, err := normalizeCommitmentKind(in.Kind, "commitment create")
	if err != nil {
		return Result{Error: err.Error()}, nil
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return Result{Error: "commitment create: title is required"}, nil
	}
	id := "commitment_" + uuid.NewString()
	if err := t.store.CreateCommitment(ctx, world.Commitment{
		ID:      id,
		Kind:    kind,
		Title:   title,
		Body:    in.Body,
		DueAt:   in.DueAt,
		Horizon: in.Horizon,
	}); err != nil {
		return Result{Error: "commitment create: " + err.Error()}, nil
	}
	return Result{Output: fmt.Sprintf("commitment created: %s", id)}, nil
}

func (t *CommitmentTool) handleUpdate(ctx context.Context, in commitmentInput, raw map[string]json.RawMessage) (Result, error) {
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return Result{Error: "commitment update: id is required"}, nil
	}

	set := make(map[string]any)
	if _, ok := raw["title"]; ok {
		set["title"] = in.Title
	}
	if _, ok := raw["body"]; ok {
		set["body"] = in.Body
	}
	if _, ok := raw["state"]; ok {
		state, err := normalizeCommitmentState(in.State, "commitment update")
		if err != nil {
			return Result{Error: err.Error()}, nil
		}
		set["state"] = state
	}
	if _, ok := raw["due_at"]; ok {
		set["due_at"] = in.DueAt
	}
	if _, ok := raw["horizon"]; ok {
		set["horizon"] = in.Horizon
	}

	if err := t.store.UpdateCommitment(ctx, id, set); err != nil {
		return Result{Error: "commitment update: " + err.Error()}, nil
	}
	return Result{Output: "commitment updated: " + id}, nil
}

func (t *CommitmentTool) handleList(ctx context.Context, in commitmentInput) (Result, error) {
	states := make([]string, 0, len(in.States))
	for _, state := range in.States {
		normalized, err := normalizeCommitmentState(state, "commitment list")
		if err != nil {
			return Result{Error: err.Error()}, nil
		}
		states = append(states, normalized)
	}
	commitments, err := t.store.ListCommitments(ctx, states, "")
	if err != nil {
		return Result{Error: "commitment list: " + err.Error()}, nil
	}
	return Result{Output: formatCommitments(commitments)}, nil
}

// WorldEditTool writes identity files inside the fenced world identity root.
type WorldEditTool struct {
	identity world.Identity
}

func NewWorldEditTool(identity world.Identity) *WorldEditTool {
	return &WorldEditTool{identity: identity}
}

func (t *WorldEditTool) Name() string { return "world_edit" }
func (t *WorldEditTool) Description() string {
	return "Create or update an identity file inside the world identity directory. Content replaces the file unless append is true."
}
func (t *WorldEditTool) RequiresApproval() bool { return false }
func (t *WorldEditTool) IsReadOnly() bool       { return false }
func (t *WorldEditTool) Capabilities() ToolCapabilities {
	return ToolCapabilities{
		IsReadOnly:      false,
		IsDestructive:   false,
		RequiresNetwork: false,
		ApprovalMode:    "auto",
		ParallelSafety:  ParallelPathScoped,
		Reversibility:   "reversible",
	}
}

func (t *WorldEditTool) ExtractPaths(input []byte) ([]string, error) {
	var in struct {
		File string `json:"file"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	path, _, err := t.resolveIdentityPath(in.File)
	if err != nil || path == "" {
		return nil, err
	}
	return []string{path}, nil
}

func (t *WorldEditTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file": map[string]any{
				"type":        "string",
				"description": "Relative identity file path, for example preferences/coding.md or profile.md",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Full replacement content, or content to append when append=true",
			},
			"append": map[string]any{
				"type":        "boolean",
				"description": "Append content instead of replacing the file",
			},
		},
		"required": []string{"file", "content"},
	}
}

type worldEditInput struct {
	File    string `json:"file"`
	Content string `json:"content"`
	Append  bool   `json:"append"`
}

func (t *WorldEditTool) Execute(ctx context.Context, input []byte) (Result, error) {
	var in worldEditInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Error: "world_edit: invalid input: " + err.Error()}, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(input, &raw); err != nil {
		return Result{Error: "world_edit: invalid input: " + err.Error()}, nil
	}
	if _, ok := raw["content"]; !ok {
		return Result{Error: "world_edit: content is required"}, nil
	}

	path, displayPath, err := t.resolveIdentityPath(in.File)
	if err != nil {
		return Result{Error: err.Error()}, nil
	}
	if err := ensureWorldEditParent(path, t.identity.Dir); err != nil {
		return Result{Error: err.Error()}, nil
	}

	flags := os.O_CREATE | os.O_WRONLY
	action := "written"
	if in.Append {
		flags |= os.O_APPEND
		action = "appended"
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return Result{Error: "world_edit: " + err.Error()}, nil
	}
	if _, err := f.WriteString(in.Content); err != nil {
		_ = f.Close()
		return Result{Error: "world_edit: " + err.Error()}, nil
	}
	if err := f.Close(); err != nil {
		return Result{Error: "world_edit: " + err.Error()}, nil
	}
	if err := vcs.EnsureRepo(ctx, t.identity.Dir); err != nil {
		slog.Warn("world_edit: ensure identity repo failed", "err", err)
	} else if _, _, err := vcs.Commit(ctx, t.identity.Dir, "world_edit: "+displayPath, displayPath); err != nil {
		slog.Warn("world_edit: commit identity change failed", "err", err)
	}
	return Result{Output: fmt.Sprintf("world identity file %s: %s", action, displayPath)}, nil
}

func (t *WorldEditTool) resolveIdentityPath(file string) (string, string, error) {
	file = strings.TrimSpace(file)
	if file == "" {
		return "", "", fmt.Errorf("world_edit: file is required")
	}
	if filepath.IsAbs(file) {
		return "", "", fmt.Errorf("world_edit: file must be a relative path inside the identity root")
	}
	clean := filepath.Clean(file)
	if clean == "." || clean == string(filepath.Separator) {
		return "", "", fmt.Errorf("world_edit: file must name a file inside the identity root")
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("world_edit: path %q escapes identity root", file)
	}

	root, err := filepath.Abs(filepath.Clean(t.identity.Dir))
	if err != nil {
		return "", "", fmt.Errorf("world_edit: resolve identity root: %w", err)
	}
	target := filepath.Join(root, clean)
	if !pathWithinRoot(root, target) {
		return "", "", fmt.Errorf("world_edit: path %q escapes identity root", file)
	}
	return target, clean, nil
}

func ensureWorldEditParent(target, root string) error {
	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return fmt.Errorf("world_edit: resolve identity root: %w", err)
	}
	if err := os.MkdirAll(rootAbs, 0o755); err != nil {
		return fmt.Errorf("world_edit: ensure identity root: %w", err)
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return fmt.Errorf("world_edit: resolve identity root symlinks: %w", err)
	}

	if err := rejectExistingSymlinkEscape(rootAbs, rootReal, target); err != nil {
		return err
	}

	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("world_edit: create parent dirs: %w", err)
	}
	parentReal, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return fmt.Errorf("world_edit: resolve parent symlinks: %w", err)
	}
	if !pathWithinRoot(rootReal, parentReal) {
		return fmt.Errorf("world_edit: symlink target %q escapes identity root", parent)
	}
	if _, err := os.Lstat(target); err == nil {
		targetReal, evalErr := filepath.EvalSymlinks(target)
		if evalErr != nil {
			return fmt.Errorf("world_edit: resolve target symlinks: %w", evalErr)
		}
		if !pathWithinRoot(rootReal, targetReal) {
			return fmt.Errorf("world_edit: symlink target %q escapes identity root", target)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("world_edit: inspect target: %w", err)
	}
	return nil
}

func rejectExistingSymlinkEscape(rootAbs, rootReal, target string) error {
	rel, err := filepath.Rel(rootAbs, target)
	if err != nil {
		return fmt.Errorf("world_edit: resolve relative target: %w", err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("world_edit: path %q escapes identity root", target)
	}

	parts := strings.Split(rel, string(filepath.Separator))
	current := rootAbs
	for _, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("world_edit: inspect path: %w", err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		realPath, err := filepath.EvalSymlinks(current)
		if err != nil {
			return fmt.Errorf("world_edit: resolve symlink: %w", err)
		}
		if !pathWithinRoot(rootReal, realPath) {
			return fmt.Errorf("world_edit: symlink target %q escapes identity root", current)
		}
	}
	return nil
}

func pathWithinRoot(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

func normalizeCommitmentKind(kind, op string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(kind))
	if normalized == "" {
		return "", fmt.Errorf("%s: kind is required (valid: %s)", op, validCommitmentKindList())
	}
	if _, ok := validCommitmentKinds[normalized]; !ok {
		return "", fmt.Errorf("%s: unknown kind %q (valid: %s)", op, kind, validCommitmentKindList())
	}
	return normalized, nil
}

func normalizeCommitmentState(state, op string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(state))
	if normalized == "" {
		return "", fmt.Errorf("%s: state is required (valid: %s)", op, validCommitmentStateList())
	}
	if _, ok := validCommitmentStates[normalized]; !ok {
		return "", fmt.Errorf("%s: unknown state %q (valid: %s)", op, state, validCommitmentStateList())
	}
	return normalized, nil
}

func validCommitmentKindList() string {
	return sortedKeys(validCommitmentKinds)
}

func validCommitmentStateList() string {
	return sortedKeys(validCommitmentStates)
}

func sortedKeys(m map[string]struct{}) string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

func formatCommitments(commitments []world.Commitment) string {
	if len(commitments) == 0 {
		return "No commitments found."
	}
	var b strings.Builder
	for _, c := range commitments {
		due := c.DueAt
		if due == "" {
			due = "no due"
		}
		fmt.Fprintf(&b, "%s [%s/%s] %s (due: %s", c.ID, c.Kind, c.State, compactWorldLine(c.Title), due)
		if c.Horizon != "" {
			fmt.Fprintf(&b, ", horizon: %s", compactWorldLine(c.Horizon))
		}
		b.WriteString(")")
		if c.Body != "" {
			fmt.Fprintf(&b, " - %s", compactWorldLine(c.Body))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatJournalEntries(entries []world.JournalEntry) string {
	if len(entries) == 0 {
		return "No journal entries found."
	}
	var b strings.Builder
	for _, entry := range entries {
		fmt.Fprintf(&b, "%s [%s] %s", entry.OccurredAt, entry.Kind, compactWorldLine(entry.Summary))
		if entry.Detail != "" {
			fmt.Fprintf(&b, " - %s", compactWorldLine(entry.Detail))
		}
		if entry.ID != "" {
			fmt.Fprintf(&b, " (%s)", entry.ID)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func compactWorldLine(s string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}
