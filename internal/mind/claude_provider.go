package mind

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
)

// DynamicContextMarker delimits the static (cacheable) prefix of a system prompt from
// its dynamic suffix. The prompt-framing layer inserts it; this provider splits on it to
// place the Anthropic cache_control boundary. It is the shared cache-split protocol token.
const DynamicContextMarker = "<!-- DYNAMIC_CONTEXT -->"

// APIPromptCacheStats tracks Anthropic API-level prompt caching metrics across calls.
type APIPromptCacheStats struct {
	CacheCreationTokens atomic.Int64
	CacheReadTokens     atomic.Int64
	InputTokens         atomic.Int64
	OutputTokens        atomic.Int64
}

// Snapshot returns a copy of the current cache stats.
func (s *APIPromptCacheStats) Snapshot() (creation, read int64) {
	return s.CacheCreationTokens.Load(), s.CacheReadTokens.Load()
}

// TokenSnapshot returns cumulative input/output token counts.
func (s *APIPromptCacheStats) TokenSnapshot() (input, output int64) {
	return s.InputTokens.Load(), s.OutputTokens.Load()
}

// ClaudeProvider implements Provider using the Anthropic SDK.
type ClaudeProvider struct {
	client     anthropic.Client
	model      string
	cacheStats APIPromptCacheStats
}

// supportsCaching returns true if the current model supports Anthropic prompt caching.
// Capabilities reports the negotiated capabilities. A cache breakpoint is offered
// only when the construction-time model supports prompt caching; otherwise the
// composer must not place a boundary marker (the non-caching path sends System
// verbatim). This keys off the same c.model as the actual cache_control branch in
// buildParams, so the composer and provider always agree on whether to place a
// boundary. (A `/model` runtime override changes req.Model but not c.model, so
// caching follows the construction model — a pre-existing limitation; the fix is
// to rebuild the provider on model change, tracked separately.)
func (c *ClaudeProvider) Capabilities() Caps {
	if c.supportsCaching() {
		return Caps{CacheBreakpoints: 1}
	}
	return Caps{}
}

func (c *ClaudeProvider) supportsCaching() bool {
	// All Claude 3+ models on the Anthropic API support prompt caching.
	cachePrefixes := []string{
		"claude-3-", "claude-3.", "claude-sonnet-", "claude-opus-", "claude-haiku-",
	}
	lower := strings.ToLower(c.model)
	for _, prefix := range cachePrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// GetCacheStats returns a snapshot of accumulated prompt cache metrics.
func (c *ClaudeProvider) GetCacheStats() (creation, read int64) {
	return c.cacheStats.Snapshot()
}

// GetTokenStats returns cumulative input/output token counts.
func (c *ClaudeProvider) GetTokenStats() (input, output int64) {
	return c.cacheStats.TokenSnapshot()
}

func NewClaudeProvider(apiKey, model, baseURL string) *ClaudeProvider {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if apiKey != "" {
		// NewClient auto-applies ANTHROPIC_AUTH_TOKEN from the process
		// environment as an Authorization bearer header, which silently
		// overrides the configured api_key at relays that prefer it. Config is
		// the source of truth: strip the ambient header when config supplies a key.
		opts = append(opts, option.WithHeaderDel("Authorization"))
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	client := anthropic.NewClient(opts...)
	return &ClaudeProvider{client: client, model: model}
}

func (c *ClaudeProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	params := c.buildParams(req)
	resp, err := c.client.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("claude complete: %w", err)
	}

	// Track prompt cache token usage
	c.trackCacheUsage(resp.Usage)

	return c.parseResponse(resp), nil
}

func (c *ClaudeProvider) Stream(ctx context.Context, req CompletionRequest) (StreamIterator, error) {
	params := c.buildParams(req)
	stream := c.client.Messages.NewStreaming(ctx, params)
	return &claudeStreamIterator{
		stream:    stream,
		provider:  c,
		finalized: false,
	}, nil
}

func (c *ClaudeProvider) buildParams(req CompletionRequest) anthropic.MessageNewParams {
	messages := make([]anthropic.MessageParam, 0, len(req.Messages))
	for i := 0; i < len(req.Messages); i++ {
		m := req.Messages[i]
		switch m.Role {
		case "user":
			if m.ToolUseID != "" {
				// Tool result(s). The Anthropic wire format requires every
				// tool_use in the preceding assistant turn to be answered by a
				// tool_result in the SINGLE next message; strict compatible
				// endpoints (e.g. DeepSeek) reject split tool_result messages with
				// a 400. The episode runner and legacy loop both append one
				// message per parallel tool call, so coalesce a consecutive run of
				// tool-result messages into one user message here.
				blocks := []anthropic.ContentBlockParamUnion{
					anthropic.NewToolResultBlock(m.ToolUseID, m.Content, false),
				}
				for i+1 < len(req.Messages) {
					next := req.Messages[i+1]
					if next.Role != "user" || next.ToolUseID == "" {
						break
					}
					blocks = append(blocks, anthropic.NewToolResultBlock(next.ToolUseID, next.Content, false))
					i++
				}
				messages = append(messages, anthropic.NewUserMessage(blocks...))
			} else {
				messages = append(messages, anthropic.NewUserMessage(
					anthropic.NewTextBlock(m.Content),
				))
			}
		case "assistant":
			blocks := make([]anthropic.ContentBlockParamUnion, 0)
			// A thinking block, when present, must precede text/tool_use blocks
			// and be replayed verbatim with its signature (the API verifies it).
			// Guard on signature too: only replay a fully-formed, signed block.
			if m.Thinking != "" && m.Signature != "" {
				blocks = append(blocks, anthropic.NewThinkingBlock(m.Signature, m.Thinking))
			}
			if m.Content != "" {
				blocks = append(blocks, anthropic.NewTextBlock(m.Content))
			}
			for _, tc := range m.ToolBlocks {
				var input any
				if err := json.Unmarshal([]byte(tc.Input), &input); err != nil {
					slog.Warn("claude: failed to unmarshal tool input for message building", "tool", tc.Name, "err", err)
					input = tc.Input // fall back to raw string
				}
				blocks = append(blocks, anthropic.NewToolUseBlock(tc.ID, input, tc.Name))
			}
			messages = append(messages, anthropic.NewAssistantMessage(blocks...))
		}
	}

	tools := make([]anthropic.ToolUnionParam, 0, len(req.Tools))
	if req.ToolChoice != "none" {
		for _, t := range req.Tools {
			schema := anthropic.ToolInputSchemaParam{
				Properties: t.InputSchema["properties"],
			}
			if req, ok := t.InputSchema["required"].([]string); ok {
				schema.Required = req
			}
			tools = append(tools, anthropic.ToolUnionParam{
				OfTool: &anthropic.ToolParam{
					Name:        t.Name,
					Description: anthropic.String(t.Description),
					InputSchema: schema,
				},
			})
		}
	}

	maxTokens := int64(req.MaxTokens)
	if maxTokens == 0 {
		maxTokens = 8192
	}

	model := req.Model
	if model == "" {
		model = c.model
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: maxTokens,
		Messages:  messages,
	}

	if len(tools) > 0 {
		params.Tools = tools
		// Mark the last tool definition with cache_control for prompt caching.
		// This ensures the entire tool definition block is cached.
		if c.supportsCaching() && len(params.Tools) > 0 {
			last := &params.Tools[len(params.Tools)-1]
			if last.OfTool != nil {
				last.OfTool.CacheControl = anthropic.NewCacheControlEphemeralParam()
			}
		}
	}

	switch req.ToolChoice {
	case "any":
		params.ToolChoice = anthropic.ToolChoiceUnionParam{
			OfAny: &anthropic.ToolChoiceAnyParam{},
		}
	case "":
	case "none":
	default:
		params.ToolChoice = anthropic.ToolChoiceParamOfTool(req.ToolChoice)
	}

	if req.ResponseFormat != nil && req.ResponseFormat.Type == "json_schema" && req.ResponseFormat.JSONSchema != nil {
		schema, ok := req.ResponseFormat.JSONSchema.Schema.(map[string]any)
		if ok {
			params.OutputConfig = anthropic.OutputConfigParam{
				Format: anthropic.JSONOutputFormatParam{
					Schema: schema,
				},
			}
		}
	}

	if req.System != "" {
		if c.supportsCaching() {
			if idx := strings.Index(req.System, DynamicContextMarker); idx >= 0 {
				staticPart := req.System[:idx]
				dynamicPart := req.System[idx+len(DynamicContextMarker):]
				staticBlock := anthropic.TextBlockParam{
					Text:         staticPart,
					CacheControl: anthropic.NewCacheControlEphemeralParam(),
				}
				params.System = []anthropic.TextBlockParam{staticBlock}
				if strings.TrimSpace(dynamicPart) != "" {
					params.System = append(params.System, anthropic.TextBlockParam{Text: dynamicPart})
				}
			} else {
				sysBlock := anthropic.TextBlockParam{
					Text:         req.System,
					CacheControl: anthropic.NewCacheControlEphemeralParam(),
				}
				params.System = []anthropic.TextBlockParam{sysBlock}
			}
		} else {
			params.System = []anthropic.TextBlockParam{{Text: req.System}}
		}
	}

	// Enable extended thinking when a budget is configured. The API requires
	// temperature=1 and budget_tokens < max_tokens, so bump max_tokens to leave
	// room for the final answer on top of the reasoning budget.
	if req.ThinkingBudget > 0 {
		budget := int64(req.ThinkingBudget)
		if budget >= params.MaxTokens {
			params.MaxTokens = budget + maxTokens
		}
		params.Thinking = anthropic.ThinkingConfigParamOfEnabled(budget)
		params.Temperature = anthropic.Float(1)
	}

	return params
}

func (c *ClaudeProvider) parseResponse(resp *anthropic.Message) *CompletionResponse {
	result := &CompletionResponse{
		StopReason: StopReason(resp.StopReason),
		Usage:      usageFromAnthropic(resp.Usage),
	}

	for _, block := range resp.Content {
		switch v := block.AsAny().(type) {
		case anthropic.TextBlock:
			result.Text += v.Text
		case anthropic.ThinkingBlock:
			result.Thinking += v.Thinking
			if v.Signature != "" {
				result.Signature = v.Signature
			}
		case anthropic.ToolUseBlock:
			inputBytes, err := json.Marshal(v.Input)
			if err != nil {
				slog.Warn("claude: failed to marshal tool use input", "tool", v.Name, "err", err)
				inputBytes = []byte("{}")
			}
			result.ToolCalls = append(result.ToolCalls, ToolUseBlock{
				ID:    v.ID,
				Name:  v.Name,
				Input: string(inputBytes),
			})
		}
	}

	return result
}

// claudeStreamIterator wraps the Anthropic streaming response.
type claudeStreamIterator struct {
	stream    *ssestream.Stream[anthropic.MessageStreamEventUnion]
	done      bool
	accum     anthropic.Message
	provider  *ClaudeProvider // back-reference for tracking cache usage
	finalized bool
}

func (it *claudeStreamIterator) Next() (StreamDelta, error) {
	if it.done {
		return StreamDelta{Done: true}, nil
	}

	for it.stream.Next() {
		event := it.stream.Current()
		_ = it.accum.Accumulate(event)

		switch e := event.AsAny().(type) {
		case anthropic.ContentBlockDeltaEvent:
			switch d := e.Delta.AsAny().(type) {
			case anthropic.TextDelta:
				return StreamDelta{Text: d.Text}, nil
			case anthropic.ThinkingDelta:
				return StreamDelta{Thinking: d.Thinking}, nil
			case anthropic.SignatureDelta:
				return StreamDelta{Signature: d.Signature}, nil
			}
		case anthropic.ContentBlockStopEvent:
		case anthropic.MessageStopEvent:
			it.done = true
			// Track cache usage from the accumulated message
			if it.provider != nil {
				it.provider.trackCacheUsage(it.accum.Usage)
			}
			resp := parseStreamedMessage(&it.accum)
			delta := StreamDelta{
				Done:       true,
				StopReason: resp.StopReason,
				ToolCalls:  resp.ToolCalls,
				Usage:      resp.Usage,
			}
			// Keep backward compat: set ToolCall to first if present
			if len(resp.ToolCalls) > 0 {
				delta.ToolCall = &resp.ToolCalls[0]
			}
			it.finish(nil)
			return delta, nil
		}
	}

	if err := it.stream.Err(); err != nil {
		it.done = true
		it.finish(err)
		return StreamDelta{}, fmt.Errorf("claude stream: %w", err)
	}

	// Stream ended — parse accumulated message for tool calls
	it.done = true
	if it.provider != nil {
		it.provider.trackCacheUsage(it.accum.Usage)
	}
	resp := parseStreamedMessage(&it.accum)
	delta := StreamDelta{Done: true, StopReason: resp.StopReason, ToolCalls: resp.ToolCalls, Usage: resp.Usage}
	if len(resp.ToolCalls) > 0 {
		delta.ToolCall = &resp.ToolCalls[0]
	}
	it.finish(nil)
	return delta, nil
}

func (it *claudeStreamIterator) Close() {
	if it.stream != nil {
		_ = it.stream.Close()
	}
	it.finish(nil)
}

func (it *claudeStreamIterator) finish(_ error) {
	if it.finalized {
		return
	}
	it.finalized = true
}

func parseStreamedMessage(msg *anthropic.Message) *CompletionResponse {
	result := &CompletionResponse{
		StopReason: StopReason(msg.StopReason),
		Usage:      usageFromAnthropic(msg.Usage),
	}
	for _, block := range msg.Content {
		switch v := block.AsAny().(type) {
		case anthropic.TextBlock:
			result.Text += v.Text
		case anthropic.ThinkingBlock:
			result.Thinking += v.Thinking
			if v.Signature != "" {
				result.Signature = v.Signature
			}
		case anthropic.ToolUseBlock:
			inputBytes, err := json.Marshal(v.Input)
			if err != nil {
				slog.Warn("claude: failed to marshal tool use input", "tool", v.Name, "err", err)
				inputBytes = []byte("{}")
			}
			result.ToolCalls = append(result.ToolCalls, ToolUseBlock{
				ID:    v.ID,
				Name:  v.Name,
				Input: string(inputBytes),
			})
		}
	}
	return result
}

// usageFromAnthropic maps the Anthropic per-response usage onto the provider-
// neutral Usage. Anthropic reports cache-read and cache-creation as separate
// counts from input_tokens (cached reads are billed at a discount and are not
// included in input_tokens), so all three are carried through distinctly.
func usageFromAnthropic(u anthropic.Usage) Usage {
	return Usage{
		InputTokens:         int(u.InputTokens),
		OutputTokens:        int(u.OutputTokens),
		CacheReadTokens:     int(u.CacheReadInputTokens),
		CacheCreationTokens: int(u.CacheCreationInputTokens),
	}
}

// trackCacheUsage accumulates prompt cache and token usage metrics from an API response.
func (c *ClaudeProvider) trackCacheUsage(usage anthropic.Usage) {
	if usage.InputTokens > 0 {
		c.cacheStats.InputTokens.Add(usage.InputTokens)
	}
	if usage.OutputTokens > 0 {
		c.cacheStats.OutputTokens.Add(usage.OutputTokens)
	}
	if usage.CacheCreationInputTokens > 0 {
		c.cacheStats.CacheCreationTokens.Add(usage.CacheCreationInputTokens)
	}
	if usage.CacheReadInputTokens > 0 {
		c.cacheStats.CacheReadTokens.Add(usage.CacheReadInputTokens)
	}
	if usage.CacheCreationInputTokens > 0 || usage.CacheReadInputTokens > 0 {
		slog.Debug("prompt cache usage",
			"cache_creation_tokens", usage.CacheCreationInputTokens,
			"cache_read_tokens", usage.CacheReadInputTokens,
			"input_tokens", usage.InputTokens,
		)
	}
}
