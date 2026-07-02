package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Forest-Isle/daimon/internal/values"
)

// ValuesTool records and lists user values — the durable, sourced principles
// that permit autonomous action. The agent calls it after the user answers an
// ask-once question so the same value tradeoff is never asked again.
type ValuesTool struct {
	store *values.Store
}

func NewValuesTool(store *values.Store) *ValuesTool {
	return &ValuesTool{store: store}
}

func (t *ValuesTool) Name() string { return "values" }
func (t *ValuesTool) Description() string {
	return "Record or list durable user values. Record a value ONLY after the user explicitly decides a tradeoff — it becomes the permission source for autonomous action, so recording requires the user's approval. List to review existing values. Domains are coarse (for example: bash, spending, travel, communication)."
}
func (t *ValuesTool) RequiresApproval() bool { return true }
func (t *ValuesTool) IsReadOnly() bool       { return false }

// Capabilities marks the values tool as requiring human sign-off. A recorded
// value is the permission source the action gate trusts to release autonomous
// actions, so the agent must never be able to mint one for itself: IsDestructive
// forces an approval prompt on interactive channels and denial on autonomous
// ones (no approver), closing the self-authorization loophole.
func (t *ValuesTool) Capabilities() ToolCapabilities {
	return ToolCapabilities{
		IsReadOnly:      false,
		IsDestructive:   true,
		RequiresNetwork: false,
		ApprovalMode:    "always",
		ParallelSafety:  ParallelNever,
		Reversibility:   "irreversible",
	}
}

func (t *ValuesTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"record", "list"},
				"description": "Value action to perform",
			},
			"domain": map[string]any{
				"type":        "string",
				"description": "Coarse domain the value governs (required for record), e.g. bash, spending, travel",
			},
			"statement": map[string]any{
				"type":        "string",
				"description": "The principle in the user's terms (required for record)",
			},
			"confidence": map[string]any{
				"type":        "number",
				"description": "Confidence 0..1 that this reflects the user's settled preference (default 0.8)",
			},
			"episode": map[string]any{
				"type":        "string",
				"description": "Episode id that surfaced this value (provenance)",
			},
			"quote": map[string]any{
				"type":        "string",
				"description": "The user's own words that established the value (provenance)",
			},
		},
		"required": []string{"action"},
	}
}

type valuesInput struct {
	Action     string  `json:"action"`
	Domain     string  `json:"domain"`
	Statement  string  `json:"statement"`
	Confidence float64 `json:"confidence"`
	Episode    string  `json:"episode"`
	Quote      string  `json:"quote"`
}

func (t *ValuesTool) Execute(ctx context.Context, input []byte) (Result, error) {
	if t.store == nil {
		return Result{Error: "values: store unavailable"}, nil
	}
	var in valuesInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Error: "values: invalid input: " + err.Error()}, nil
	}
	switch strings.TrimSpace(in.Action) {
	case "record":
		return t.handleRecord(ctx, in)
	case "list":
		return t.handleList()
	default:
		return Result{Error: fmt.Sprintf("values: unknown action %q (valid: record, list)", in.Action)}, nil
	}
}

func (t *ValuesTool) handleRecord(ctx context.Context, in valuesInput) (Result, error) {
	var prov []values.Provenance
	if strings.TrimSpace(in.Episode) != "" || strings.TrimSpace(in.Quote) != "" {
		prov = []values.Provenance{{Episode: strings.TrimSpace(in.Episode), Quote: strings.TrimSpace(in.Quote)}}
	}
	entry, err := t.store.Add(ctx, values.Entry{
		Domain:     in.Domain,
		Statement:  in.Statement,
		Confidence: in.Confidence,
		Provenance: prov,
	})
	if err != nil {
		return Result{Error: "values record: " + err.Error()}, nil
	}
	return Result{Output: fmt.Sprintf("value recorded: %s [%s] %s", entry.ID, entry.Domain, compactWorldLine(entry.Statement))}, nil
}

func (t *ValuesTool) handleList() (Result, error) {
	entries := t.store.List()
	if len(entries) == 0 {
		return Result{Output: "No values recorded."}, nil
	}
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "%s [%s/%s] %s (confidence %.2f)\n", e.ID, e.Domain, e.State, compactWorldLine(e.Statement), e.Confidence)
	}
	return Result{Output: strings.TrimRight(b.String(), "\n")}, nil
}
