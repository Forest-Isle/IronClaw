package action

import (
	"encoding/json"
	"strings"

	"github.com/Forest-Isle/daimon/internal/tool"
)

// Classifier assigns a reversibility Class to a tool call. The governed flag is
// false for read-only tools, which carry no autonomy implications and are not
// recorded in the trust ledger.
type Classifier interface {
	Classify(call *tool.ToolCall) (class Class, governed bool)
	ContextKey(call *tool.ToolCall) string
}

type defaultClassifier struct{}
type holdAwareClassifier struct {
	defaultClassifier
}

// NewClassifier returns the default reversibility classifier.
func NewClassifier() Classifier { return defaultClassifier{} }

// NewClassifierWithCompensableHTTP returns the default classifier plus the hold
// loop's read-operation exceptions for mutating tools with harmless reads.
func NewClassifierWithCompensableHTTP() Classifier { return holdAwareClassifier{} }

func (defaultClassifier) Classify(call *tool.ToolCall) (Class, bool) {
	if call == nil {
		return Reversible, false
	}
	if call.Capabilities.IsReadOnly {
		return Reversible, false
	}
	if class, ok := classifyDynamicTool(call); ok {
		return class, true
	}
	return classifyDeclaredOrFailClosed(call)
}

func (defaultClassifier) ContextKey(call *tool.ToolCall) string {
	if call == nil {
		return ""
	}
	return call.ToolName
}

func (c holdAwareClassifier) Classify(call *tool.ToolCall) (Class, bool) {
	if call == nil {
		return Reversible, false
	}
	if call.Capabilities.IsReadOnly {
		return Reversible, false
	}
	if class, ok := classifyDynamicTool(call); ok {
		return class, true
	}
	switch call.ToolName {
	case "memory":
		// Read operations carry no side effects, so they are ungoverned and
		// safe to run autonomously. Writes (save/delete) and any malformed or
		// unknown op fall through to the tool's declared irreversible class.
		if isReadOnlyToolOp(call.Input, "search", "list") {
			return Reversible, false
		}
	case "values":
		// values.list is a harmless read; values.record mints a value, which
		// is a permission source for autonomous actions - it must stay governed
		// so the agent can never self-authorize (constitution #4). Only "list"
		// is ungoverned; record/unknown fall through to governed.
		if isReadOnlyToolOp(call.Input, "list") {
			return Reversible, false
		}
	}
	return classifyDeclaredOrFailClosed(call)
}

func classifyDynamicTool(call *tool.ToolCall) (Class, bool) {
	switch call.ToolName {
	case "bash":
		// bash is classified by its command, since a single tool spans the full
		// reversibility range. The permission engine still does the real gating;
		// this heuristic only feeds the trust record.
		return classifyBash(call.Input), true
	case "http":
		if class, ok := classifyHTTP(call.Input); ok {
			return class, true
		}
	}
	return Reversible, false
}

func classifyDeclaredOrFailClosed(call *tool.ToolCall) (Class, bool) {
	if call.Capabilities.Reversibility == "" {
		return Irreversible, true
	}
	class, err := ParseClass(call.Capabilities.Reversibility)
	if err != nil {
		return Irreversible, true
	}
	return class, true
}

// classifyBash decides whether a bash tool call earns the cautious Irreversible
// classification for trust accounting. It parses the command's "command" field
// into a shell AST (see classifyBashCommand); a malformed tool input is treated
// as Irreversible. This is not a security boundary (the permission policy is).
func classifyBash(input string) Class {
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(input), &in); err != nil {
		return Irreversible
	}
	return classifyBashCommand(in.Command)
}

// classifyHTTP marks mutating HTTP methods as compensable. GET and malformed
// inputs deliberately fall back to the default classifier.
func classifyHTTP(input string) (Class, bool) {
	var in struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal([]byte(input), &in); err != nil {
		return Reversible, false
	}
	switch strings.ToUpper(in.Method) {
	case "POST", "PUT", "PATCH", "DELETE":
		return Compensable, true
	case "GET":
		return Reversible, false
	default:
		return Reversible, false
	}
}

// isReadOnlyToolOp reports whether the call's "operation" field is one of the
// given read-only operation names. A malformed input matches nothing (fail
// closed: an undecodable op is never treated as read-only).
func isReadOnlyToolOp(input string, readOps ...string) bool {
	var in struct {
		Operation string `json:"operation"`
	}
	if err := json.Unmarshal([]byte(input), &in); err != nil {
		return false
	}
	for _, op := range readOps {
		if in.Operation == op {
			return true
		}
	}
	return false
}
