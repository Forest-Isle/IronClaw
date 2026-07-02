package agent

import (
	"context"

	"github.com/Forest-Isle/daimon/internal/mind"
)

// CognitiveKernel is a pluggable cognitive execution strategy invoked by
// HandleMessage in place of the legacy LinearLoop. The episode kernel
// implements it. The interface is declared here, at the use site, so the agent
// can delegate without importing the episode package (which imports agent).
type CognitiveKernel interface {
	Execute(ctx context.Context, req CognitiveRequest) (CognitiveOutcome, error)
}

// ToolInvokeFunc runs one tool call through the agent's full security pipeline
// (interceptor chain, approval, hooks) and event recording, returning the tool
// output and whether it failed. The kernel uses this instead of touching the
// tool registry directly, so episode tool calls get the same governance as the
// legacy path.
type ToolInvokeFunc func(ctx context.Context, iteration int, call mind.ToolUseBlock) (output string, isError bool)

// CognitiveRequest carries everything the kernel needs for one turn. The
// runtime-owned context (persona, rules, retrieved memories, transcript) is
// pre-assembled by the agent so the kernel stays free of subsystem wiring.
type CognitiveRequest struct {
	SessionID string
	// EpisodeID, when set, is a deterministic idempotency key for the episode
	// (e.g. the triggering heart event id). The kernel uses it as the episode id
	// and skips execution if an outcome already exists for it, so a re-delivered
	// trigger does not run the episode twice. Empty ⇒ the kernel generates a
	// fresh id (chat turns and ad-hoc episodes).
	EpisodeID string
	// ParentEpisodeID, when set, is the episode id of the episode that spawned
	// this one (a sub-agent episode's parent). Empty for top-level chat/heart
	// episodes. The kernel records it on the outcome so the parent→child episode
	// tree is recoverable from the journal (§4.3). It never affects execution.
	ParentEpisodeID string
	Goal            string
	Trigger         string
	Persona         string
	Rules           string
	Memories        string
	Model           string
	Provider        string
	// MaxTokens optionally caps model output for kernels that support it. Zero
	// leaves the provider/backend default unchanged.
	MaxTokens int
	// ActivityClass labels the kind of work this episode does, for cost
	// accounting by class (blueprint §4.11 ROI-by-class): "chat" for a user turn,
	// the triggering event kind (e.g. internal.heartbeat) for an autonomous
	// episode. Empty ⇒ the cost row is recorded unclassified; it never affects
	// execution.
	ActivityClass string
	Transcript    []mind.CompletionMessage
	ToolDefs      []mind.ToolDefinition
	Invoke        ToolInvokeFunc
}

// CognitiveOutcome is what the kernel returns. Reply is the user-facing text;
// Summary is the durable journal record. Status "failed" marks the governed
// turn failed; only an explicit legacy-fallback config may re-run chat outside
// the kernel.
type CognitiveOutcome struct {
	Status  string
	Reply   string
	Summary string
}

// SetKernel wires a cognitive kernel and toggles whether HandleMessage routes
// through it. Passing nil or enabled=false keeps the legacy LinearLoop.
func (a *Agent) SetKernel(kernel CognitiveKernel, enabled bool) {
	a.kernel = kernel
	a.kernelEnabled = enabled
}
