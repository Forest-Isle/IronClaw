package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Forest-Isle/daimon/internal/memory"
)

// MemoryTool is the single LLM-facing memory tool.
// Operations: save, search, delete, list.
type MemoryTool struct {
	store memory.Store
	lcMgr *memory.LifecycleManager
}

// MemoryInput is the JSON schema for the tool call.
type MemoryInput struct {
	Operation string `json:"operation"` // "save", "search", "delete", "list"
	Content   string `json:"content"`   // for save: the fact text; for search: the query text
	Category  string `json:"category"`  // optional: preference, knowledge, identity, plan
	MemoryID  string `json:"memory_id"` // required for delete
	Limit     int    `json:"limit"`     // optional: max results for search/list (default 10)
	Scope     string `json:"scope"`     // optional: "session" | "user" | "global" (default "user")
}

// NewMemoryTool creates a new MemoryTool.
func NewMemoryTool(store memory.Store, lcMgr *memory.LifecycleManager) *MemoryTool {
	return &MemoryTool{store: store, lcMgr: lcMgr}
}

func (t *MemoryTool) Name() string { return "memory" }
func (t *MemoryTool) Description() string {
	return "Manage persistent memory. Operations: save (store a fact), search (find relevant memories), delete (remove by ID), list (list recent entries)."
}
func (t *MemoryTool) RequiresApproval() bool { return true }
func (t *MemoryTool) Capabilities() ToolCapabilities {
	return ToolCapabilities{
		IsDestructive: true,
		Reversibility: "irreversible",
	}
}

func (t *MemoryTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"operation": map[string]any{
				"type":        "string",
				"enum":        []string{"save", "search", "delete", "list"},
				"description": "The operation to perform",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "For save: the fact to remember. For search: the query text.",
			},
			"category": map[string]any{
				"type":        "string",
				"description": "Optional category: preference, knowledge, identity, plan",
			},
			"memory_id": map[string]any{
				"type":        "string",
				"description": "Required for delete: the ID of the memory to remove",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Max results for search/list (default 10)",
			},
			"scope": map[string]any{
				"type":        "string",
				"enum":        []string{"session", "user", "global"},
				"description": "Memory scope (default: user)",
			},
		},
		"required": []string{"operation"},
	}
}

func (t *MemoryTool) Execute(ctx context.Context, input []byte) (Result, error) {
	var in MemoryInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Error: fmt.Sprintf("invalid input: %v", err)}, nil
	}

	switch in.Operation {
	case "save":
		return t.doSave(ctx, in)
	case "search":
		return t.doSearch(ctx, in)
	case "delete":
		return t.doDelete(ctx, in)
	case "list":
		return t.doList(ctx, in)
	default:
		return Result{Error: fmt.Sprintf("unknown operation: %s (use save, search, delete, or list)", in.Operation)}, nil
	}
}

func (t *MemoryTool) doSave(ctx context.Context, in MemoryInput) (Result, error) {
	if in.Content == "" {
		return Result{Error: "content is required for save operation"}, nil
	}

	scope := memory.ScopeUser
	if in.Scope != "" {
		scope = memory.MemoryScope(in.Scope)
	}

	meta := map[string]string{}
	if in.Category != "" {
		meta["category"] = strings.ToLower(in.Category)
	}

	now := time.Now()
	entry := memory.Entry{
		ID:        fmt.Sprintf("mem_%d", now.UnixNano()),
		Scope:     scope,
		Content:   in.Content,
		Metadata:  meta,
		CreatedAt: now,
		UpdatedAt: now,
	}

	// If lifecycle manager is available, use it for dedup/update decisions.
	if t.lcMgr != nil {
		fact := memory.ExtractedFact{
			Content:    in.Content,
			Category:   strings.ToLower(in.Category),
			Type:       "semantic",
			Importance: 5,
			Emotion:    "neutral",
		}
		result, err := t.lcMgr.Process(ctx, fact, "", "", scope)
		if err != nil {
			return Result{Error: fmt.Sprintf("lifecycle process failed: %v", err)}, nil
		}
		return Result{Output: fmt.Sprintf("Memory %s: %s — %s", result.Action, result.MemoryID, result.Reason)}, nil
	}

	if err := t.store.Save(ctx, entry); err != nil {
		return Result{Error: fmt.Sprintf("save failed: %v", err)}, nil
	}
	return Result{Output: fmt.Sprintf("Saved: %s", entry.ID)}, nil
}

func (t *MemoryTool) doSearch(ctx context.Context, in MemoryInput) (Result, error) {
	if in.Content == "" {
		return Result{Error: "content (query text) is required for search operation"}, nil
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}

	results, err := t.store.Search(ctx, memory.SearchQuery{
		Text:  in.Content,
		Limit: limit,
	})
	if err != nil {
		return Result{Error: fmt.Sprintf("search failed: %v", err)}, nil
	}

	if len(results) == 0 {
		return Result{Output: "No matching memories found."}, nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d memories:\n", len(results)))
	for i, r := range results {
		content := r.Entry.Content
		if len(content) > 160 {
			content = content[:157] + "..."
		}
		sb.WriteString(fmt.Sprintf("%d. [%s] %s (score: %.2f)\n", i+1, r.Entry.ID[:12], content, r.Score))
	}
	return Result{Output: sb.String()}, nil
}

func (t *MemoryTool) doDelete(ctx context.Context, in MemoryInput) (Result, error) {
	if in.MemoryID == "" {
		return Result{Error: "memory_id is required for delete operation"}, nil
	}

	if err := t.store.Delete(ctx, in.MemoryID); err != nil {
		return Result{Error: fmt.Sprintf("delete failed: %v", err)}, nil
	}
	return Result{Output: fmt.Sprintf("Deleted: %s", in.MemoryID)}, nil
}

func (t *MemoryTool) doList(ctx context.Context, in MemoryInput) (Result, error) {
	scope := memory.ScopeUser
	if in.Scope != "" {
		scope = memory.MemoryScope(in.Scope)
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}

	entries, err := t.store.ListByScope(ctx, scope, "")
	if err != nil {
		return Result{Error: fmt.Sprintf("list failed: %v", err)}, nil
	}

	if len(entries) == 0 {
		return Result{Output: fmt.Sprintf("No memories in %s scope.", scope)}, nil
	}

	if len(entries) > limit {
		entries = entries[:limit]
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Recent %s memories (%d):\n", scope, len(entries)))
	for i, e := range entries {
		content := e.Content
		if len(content) > 120 {
			content = content[:117] + "..."
		}
		sb.WriteString(fmt.Sprintf("%d. [%s] %s\n", i+1, e.ID[:12], content))
	}
	return Result{Output: sb.String()}, nil
}
