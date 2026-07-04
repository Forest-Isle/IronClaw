package mind

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

// TestClaudeProvider_ParseUsage locks in the per-call Usage mapping (slice C1):
// both the streamed (parseStreamedMessage) and non-streamed (parseResponse)
// paths must surface Anthropic's per-response token counts onto the neutral
// Usage, keeping cache-read and cache-creation distinct from fresh input.
func TestClaudeProvider_ParseUsage(t *testing.T) {
	msg := anthropic.Message{
		Usage: anthropic.Usage{
			InputTokens:              100,
			OutputTokens:             40,
			CacheReadInputTokens:     25,
			CacheCreationInputTokens: 10,
		},
	}
	want := Usage{InputTokens: 100, OutputTokens: 40, CacheReadTokens: 25, CacheCreationTokens: 10}

	if got := parseStreamedMessage(&msg).Usage; got != want {
		t.Errorf("parseStreamedMessage usage = %+v, want %+v", got, want)
	}
	p := NewClaudeProvider("", "claude-sonnet-4-6", "")
	if got := p.parseResponse(&msg).Usage; got != want {
		t.Errorf("parseResponse usage = %+v, want %+v", got, want)
	}
	if got := usageFromAnthropic(anthropic.Usage{}); got != (Usage{}) {
		t.Errorf("zero usage = %+v, want zero", got)
	}
}

// TestClaudeProvider_BuildParams_ToolResultShape locks in the Claude side of the
// internal tool-result contract: BuildMessages emits a tool result as
// CompletionMessage{Role:"user", ToolUseID:set}, and the Claude adapter must
// serialize that into an Anthropic tool_result block carrying the tool_use_id.
// This is the symmetric guard to the OpenAI adapter tests — the two providers
// must agree on the same internal shape, which is the root-cause class behind
// the P0/P1 bugs. Asserting on the marshaled wire JSON avoids coupling to
// SDK-internal Go field names.
func TestClaudeProvider_BuildParams_ToolResultShape(t *testing.T) {
	p := NewClaudeProvider("", "claude-sonnet-4-6", "")
	req := CompletionRequest{
		Messages: []CompletionMessage{
			{Role: "user", Content: "run the tool"},
			{Role: "assistant", Content: "", ToolBlocks: []ToolUseBlock{
				{ID: "call_99", Name: "bash", Input: `{"cmd":"ls"}`},
			}},
			// Exactly what BuildMessages emits for a tool result:
			{Role: "user", Content: "file1\nfile2", ToolUseID: "call_99"},
		},
	}

	params := p.buildParams(req)

	if len(params.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(params.Messages))
	}

	data, err := json.Marshal(params.Messages)
	if err != nil {
		t.Fatalf("marshal messages: %v", err)
	}
	wire := string(data)

	// The tool result must serialize as an Anthropic tool_result block that
	// references the originating tool_use id.
	if !strings.Contains(wire, "tool_result") {
		t.Errorf("expected a tool_result block in wire JSON:\n%s", wire)
	}
	if !strings.Contains(wire, "tool_use_id") {
		t.Errorf("expected tool_use_id in wire JSON:\n%s", wire)
	}
	if !strings.Contains(wire, "call_99") {
		t.Errorf("tool_use id call_99 missing from wire JSON:\n%s", wire)
	}
	// The assistant turn must serialize its tool call as a tool_use block.
	if !strings.Contains(wire, "tool_use") {
		t.Errorf("expected a tool_use block for the assistant turn:\n%s", wire)
	}
	if !strings.Contains(wire, "bash") {
		t.Errorf("tool name bash missing from wire JSON:\n%s", wire)
	}
}

// TestClaudeProvider_BuildParams_PlainUserMessage verifies a user message with
// no ToolUseID serializes as an ordinary text block, not a tool result.
func TestClaudeProvider_BuildParams_PlainUserMessage(t *testing.T) {
	p := NewClaudeProvider("", "claude-sonnet-4-6", "")
	req := CompletionRequest{
		Messages: []CompletionMessage{{Role: "user", Content: "just text"}},
	}

	params := p.buildParams(req)
	data, err := json.Marshal(params.Messages)
	if err != nil {
		t.Fatalf("marshal messages: %v", err)
	}
	wire := string(data)

	if strings.Contains(wire, "tool_result") {
		t.Errorf("plain user message must not produce a tool_result block:\n%s", wire)
	}
	if !strings.Contains(wire, "just text") {
		t.Errorf("user text missing from wire JSON:\n%s", wire)
	}
}

// TestClaudeProvider_BuildParams_ThinkingRoundTrip locks in the extended-thinking
// replay contract: an assistant turn carrying a signed thinking block plus a
// tool_use must serialize the thinking block (with its signature) BEFORE the
// tool_use block. The API verifies the signature, so it must travel verbatim.
func TestClaudeProvider_BuildParams_ThinkingRoundTrip(t *testing.T) {
	p := NewClaudeProvider("", "claude-sonnet-4-6", "")
	req := CompletionRequest{
		Messages: []CompletionMessage{
			{Role: "user", Content: "think then act"},
			{
				Role:      "assistant",
				Content:   "",
				Thinking:  "let me reason about this",
				Signature: "sig-abc123",
				ToolBlocks: []ToolUseBlock{
					{ID: "call_1", Name: "bash", Input: `{"cmd":"ls"}`},
				},
			},
			{Role: "user", Content: "ok", ToolUseID: "call_1"},
		},
	}

	params := p.buildParams(req)
	data, err := json.Marshal(params.Messages)
	if err != nil {
		t.Fatalf("marshal messages: %v", err)
	}
	wire := string(data)

	if !strings.Contains(wire, "thinking") {
		t.Errorf("expected a thinking block in wire JSON:\n%s", wire)
	}
	if !strings.Contains(wire, "let me reason about this") {
		t.Errorf("thinking text missing from wire JSON:\n%s", wire)
	}
	if !strings.Contains(wire, "sig-abc123") {
		t.Errorf("thinking signature missing from wire JSON:\n%s", wire)
	}
	// Ordering: the thinking block must precede the tool_use block.
	if ti, ui := strings.Index(wire, "thinking"), strings.Index(wire, "tool_use"); ti < 0 || ui < 0 || ti > ui {
		t.Errorf("thinking block must precede tool_use (thinking@%d, tool_use@%d):\n%s", ti, ui, wire)
	}
}

// TestClaudeProvider_BuildParams_UnsignedThinkingDropped guards restart safety:
// an assistant turn whose thinking has no signature (e.g. lost across a restart
// where thinking is not persisted) must NOT be replayed, or the API rejects the
// request. The tool_use must still serialize normally.
func TestClaudeProvider_BuildParams_UnsignedThinkingDropped(t *testing.T) {
	p := NewClaudeProvider("", "claude-sonnet-4-6", "")
	req := CompletionRequest{
		Messages: []CompletionMessage{
			{Role: "user", Content: "act"},
			{
				Role:     "assistant",
				Thinking: "orphaned reasoning",
				// Signature intentionally empty.
				ToolBlocks: []ToolUseBlock{
					{ID: "call_2", Name: "bash", Input: `{"cmd":"pwd"}`},
				},
			},
			{Role: "user", Content: "ok", ToolUseID: "call_2"},
		},
	}

	params := p.buildParams(req)
	data, err := json.Marshal(params.Messages)
	if err != nil {
		t.Fatalf("marshal messages: %v", err)
	}
	wire := string(data)

	if strings.Contains(wire, "orphaned reasoning") {
		t.Errorf("unsigned thinking must not be replayed:\n%s", wire)
	}
	if !strings.Contains(wire, "tool_use") {
		t.Errorf("tool_use must still serialize:\n%s", wire)
	}
}

// TestClaudeProvider_BuildParams_ThinkingBudget verifies that a positive budget
// enables thinking (and bumps max_tokens above the budget), while budget=0 is a
// no-op — preserving zero behavior change when thinking is off.
func TestClaudeProvider_BuildParams_ThinkingBudget(t *testing.T) {
	p := NewClaudeProvider("", "claude-sonnet-4-6", "")

	// budget = 0: thinking disabled, default max_tokens, no temperature override.
	off := p.buildParams(CompletionRequest{
		Messages: []CompletionMessage{{Role: "user", Content: "hi"}},
	})
	if off.Thinking.OfEnabled != nil {
		t.Errorf("budget=0 must not enable thinking, got %+v", off.Thinking.OfEnabled)
	}
	if off.Temperature.Valid() {
		t.Errorf("budget=0 must not set temperature, got %v", off.Temperature)
	}

	// budget > max_tokens: thinking enabled, max_tokens bumped, temperature=1.
	on := p.buildParams(CompletionRequest{
		Messages:       []CompletionMessage{{Role: "user", Content: "hi"}},
		MaxTokens:      4096,
		ThinkingBudget: 10000,
	})
	if on.Thinking.OfEnabled == nil {
		t.Fatalf("budget>0 must enable thinking")
	}
	if on.Thinking.OfEnabled.BudgetTokens != 10000 {
		t.Errorf("budget tokens = %d, want 10000", on.Thinking.OfEnabled.BudgetTokens)
	}
	if on.MaxTokens <= 10000 {
		t.Errorf("max_tokens must exceed budget, got %d", on.MaxTokens)
	}
	if !on.Temperature.Valid() || on.Temperature.Value != 1 {
		t.Errorf("thinking requires temperature=1, got %v", on.Temperature)
	}
}

// TestClaudeProvider_ConfigKeySuppressesEnvAuthToken locks in credential
// precedence: when config supplies an api_key, an ambient ANTHROPIC_AUTH_TOKEN
// in the process environment (e.g. left over from a Claude Code shell) must NOT
// reach the wire as an Authorization bearer header — relays that prefer it
// would silently override the configured key.
func TestClaudeProvider_ConfigKeySuppressesEnvAuthToken(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "sk-ambient-env-token")

	var gotAuth, gotAPIKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("X-Api-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"m","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()

	p := NewClaudeProvider("sk-configured-key", "m", srv.URL)
	if _, err := p.Complete(context.Background(), CompletionRequest{
		Model:     "m",
		Messages:  []CompletionMessage{{Role: "user", Content: "hi"}},
		MaxTokens: 8,
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if gotAuth != "" {
		t.Errorf("Authorization header = %q, want empty (env auth token must not leak past configured api_key)", gotAuth)
	}
	if gotAPIKey != "sk-configured-key" {
		t.Errorf("X-Api-Key = %q, want configured key", gotAPIKey)
	}
}
