package hook

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
)

// --- Event Types ---

// PreToolUseEvent is fired before a tool executes.
type PreToolUseEvent struct {
	ToolName     string
	Input        string
	Capabilities map[string]bool // IsReadOnly, IsDestructive, etc.
}

// PreToolUseResult is the outcome of a PreToolUse handler.
type PreToolUseResult struct {
	Action   string // "allow", "deny", "ask", "passthrough"
	Reason   string
	Modified *json.RawMessage // optionally modify tool input
}

// PostToolUseEvent is fired after a tool executes.
type PostToolUseEvent struct {
	ToolName   string
	Input      string
	Output     string
	Error      string
	Status     string // "success", "error", "denied"
	DurationMs int64
	// Permission decision metadata
	SessionID        string
	PermissionAction string // "allow", "deny", "ask_approved", "ask_denied"
	PermissionReason string // "rule_match", "hook_deny", "user_approval", etc.
	PermissionRule   string // matched rule description
}

// PostToolUseResult allows handlers to modify the tool output.
type PostToolUseResult struct {
	ModifiedOutput *string // if non-nil, replaces the output
}

// OnUserMessageEvent is fired when a user message is received.
type OnUserMessageEvent struct {
	Channel   string
	ChannelID string
	UserID    string
	Text      string
}

// OnUserMessageResult allows handlers to inject context or modify the message.
type OnUserMessageResult struct {
	InjectedContext []string // context strings to append to system prompt
	ModifiedText    *string  // if non-nil, replaces the user message
}

// --- Handler Interfaces ---

// PreToolUseHandler handles events before tool execution.
type PreToolUseHandler interface {
	OnPreToolUse(ctx context.Context, event PreToolUseEvent) (PreToolUseResult, error)
}

// PostToolUseHandler handles events after tool execution.
type PostToolUseHandler interface {
	OnPostToolUse(ctx context.Context, event PostToolUseEvent) (PostToolUseResult, error)
}

// OnUserMessageHandler handles events when a user message arrives.
type OnUserMessageHandler interface {
	OnUserMessage(ctx context.Context, event OnUserMessageEvent) (OnUserMessageResult, error)
}

// --- Hook Manager ---

// Manager dispatches lifecycle events to registered handlers.
type Manager struct {
	preToolUse    []PreToolUseHandler
	postToolUse   []PostToolUseHandler
	onUserMessage []OnUserMessageHandler
}

// NewManager creates an empty hook manager.
func NewManager() *Manager {
	return &Manager{}
}

// RegisterPreToolUse adds a handler for PreToolUse events.
func (m *Manager) RegisterPreToolUse(h PreToolUseHandler) {
	m.preToolUse = append(m.preToolUse, h)
}

// RegisterPostToolUse adds a handler for PostToolUse events.
func (m *Manager) RegisterPostToolUse(h PostToolUseHandler) {
	m.postToolUse = append(m.postToolUse, h)
}

// RegisterOnUserMessage adds a handler for OnUserMessage events.
func (m *Manager) RegisterOnUserMessage(h OnUserMessageHandler) {
	m.onUserMessage = append(m.onUserMessage, h)
}

// FirePreToolUse dispatches a PreToolUse event to all handlers.
// First non-passthrough result wins. If all pass through, returns "passthrough".
func (m *Manager) FirePreToolUse(ctx context.Context, event PreToolUseEvent) (PreToolUseResult, error) {
	for _, h := range m.preToolUse {
		result, err := h.OnPreToolUse(ctx, event)
		if err != nil {
			slog.Warn("hook: PreToolUse handler error", "err", err)
			continue
		}
		if result.Action != "passthrough" {
			return result, nil
		}
	}
	return PreToolUseResult{Action: "passthrough"}, nil
}

// FirePostToolUse dispatches a PostToolUse event to ALL handlers.
// All handlers are called; the last non-nil ModifiedOutput wins.
func (m *Manager) FirePostToolUse(ctx context.Context, event PostToolUseEvent) (PostToolUseResult, error) {
	var finalResult PostToolUseResult
	var errs []error
	for _, h := range m.postToolUse {
		result, err := h.OnPostToolUse(ctx, event)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if result.ModifiedOutput != nil {
			finalResult.ModifiedOutput = result.ModifiedOutput
		}
	}
	return finalResult, errors.Join(errs...)
}

// FireOnUserMessage dispatches an OnUserMessage event to ALL handlers.
// Context from all handlers is aggregated.
func (m *Manager) FireOnUserMessage(ctx context.Context, event OnUserMessageEvent) (OnUserMessageResult, error) {
	var combined OnUserMessageResult
	for _, h := range m.onUserMessage {
		result, err := h.OnUserMessage(ctx, event)
		if err != nil {
			slog.Warn("hook: OnUserMessage handler error", "err", err)
			continue
		}
		combined.InjectedContext = append(combined.InjectedContext, result.InjectedContext...)
		if result.ModifiedText != nil {
			combined.ModifiedText = result.ModifiedText
		}
	}
	return combined, nil
}

// HasPreToolUseHandlers returns true if any PreToolUse handlers are registered.
func (m *Manager) HasPreToolUseHandlers() bool { return len(m.preToolUse) > 0 }

// HasPostToolUseHandlers returns true if any PostToolUse handlers are registered.
func (m *Manager) HasPostToolUseHandlers() bool { return len(m.postToolUse) > 0 }

// HasOnUserMessageHandlers returns true if any OnUserMessage handlers are registered.
func (m *Manager) HasOnUserMessageHandlers() bool { return len(m.onUserMessage) > 0 }
