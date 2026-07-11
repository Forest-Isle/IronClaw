package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Forest-Isle/daimon/internal/channel"
	"github.com/Forest-Isle/daimon/internal/hook"
	"github.com/Forest-Isle/daimon/internal/memory"
	"github.com/Forest-Isle/daimon/internal/mind"
	"github.com/Forest-Isle/daimon/internal/session"
	"github.com/Forest-Isle/daimon/internal/tool"
)

// ApprovalFunc is called when a tool requires user approval.
// It should return true if approved, false if denied.
type ApprovalFunc func(ctx context.Context, ch channel.Channel, target channel.MessageTarget, toolName string, input string) (bool, error)

// agentContextKey is the context.Context key for storing the Agent reference.
type agentContextKey struct{}

// AgentToContext stores an Agent reference in the context.
func AgentToContext(ctx context.Context, a *Agent) context.Context {
	return context.WithValue(ctx, agentContextKey{}, a)
}

// AgentFromContext retrieves the Agent from the context.
func AgentFromContext(ctx context.Context) *Agent {
	a, _ := ctx.Value(agentContextKey{}).(*Agent)
	return a
}

// Agent is the unified agent runtime. All execution modes (simple, cognitive, graph)
// are implemented as LoopStrategy implementations.
//
// deps is a pointer so the gateway can wire subsystems (ContextManager,
// MemoryStore) after agent construction — the agent
// reads from the pointer on every access, so late-bound dependencies are
// immediately visible without a by-value copy staleness problem.
type Agent struct {
	deps          *AgentDeps
	strategy      LoopStrategy
	approvalFn    ApprovalFunc
	eventBus      EventBus
	sessionLocks  sync.Map // key: "channel:channel_id" → *sync.Mutex
	kernel        CognitiveKernel
	kernelEnabled bool
	// lastKernelOutcome holds the most recent kernel Outcome from runKernel, so a
	// caller running this agent as a one-shot sub-agent episode (SubAgentManager.
	// Spawn) can read the faithful Outcome.Status instead of re-parsing the prose
	// reply. Single-writer (one HandleMessage at a time per agent); a sub-agent
	// agent is freshly built per Spawn, so the read after HandleMessage is race-free.
	lastKernelOutcome *CognitiveOutcome
}

// LastKernelOutcome returns the Outcome of the most recent kernel run on this
// agent, or nil if the kernel did not run (legacy LinearLoop path). Used by
// SubAgentManager.Spawn to propagate the episode's structured Status into the
// SubAgentResult rather than inferring it from the reply text.
func (a *Agent) LastKernelOutcome() *CognitiveOutcome {
	return a.lastKernelOutcome
}

// NewAgent creates a new Agent with the given dependencies, strategy, and event bus.
// deps must be a pointer — the agent dereferences it on every access so the
// gateway can safely update fields after construction.
func NewAgent(deps *AgentDeps, strategy LoopStrategy, bus EventBus) *Agent {
	return &Agent{
		deps:     deps,
		strategy: strategy,
		eventBus: bus,
	}
}

// SetApprovalFunc sets the tool approval callback.
func (a *Agent) SetApprovalFunc(fn ApprovalFunc) { a.approvalFn = fn }

// SetModel updates the LLM model.
func (a *Agent) SetModel(model string) { a.deps.Core.LLMCfg.Model = model }

// Model returns the current LLM model.
func (a *Agent) Model() string { return a.deps.Core.LLMCfg.Model }

// EventBus returns the agent's event bus for external subscribers.
func (a *Agent) EventBus() EventBus { return a.eventBus }

// AgentID returns the agent's identifier.
func (a *Agent) AgentID() string { return a.deps.Core.AgentID }

// GetTools returns the tool registry.
func (a *Agent) GetTools() *tool.Registry { return a.deps.Core.Tools }

// Strategy returns the current execution strategy.
func (a *Agent) Strategy() LoopStrategy { return a.strategy }

// Sessions returns the session manager.
func (a *Agent) Sessions() *session.Manager { return a.deps.Core.Sessions }

// HandleMessage is the single entry point for all agent modes.
func (a *Agent) HandleMessage(ctx context.Context, ch channel.Channel, msg channel.InboundMessage) error {
	ctx = AgentToContext(ctx, a)
	ctx = tool.WithChannelClass(ctx, tool.ChannelClassForName(msg.Channel))

	// Serialize per-session: prevent concurrent messages on the same
	// channel+channelID from interleaving LLM calls and session state.
	lockKey := msg.Channel + ":" + msg.ChannelID
	lockVal, _ := a.sessionLocks.LoadOrStore(lockKey, &sync.Mutex{})
	lockVal.(*sync.Mutex).Lock()
	defer lockVal.(*sync.Mutex).Unlock()

	sess, err := a.deps.Core.Sessions.Get(ctx, msg.Channel, msg.ChannelID)
	if err != nil {
		return fmt.Errorf("get session: %w", err)
	}

	start := time.Now()
	a.eventBus.Publish(SessionStarted{SessionID: sess.ID, Channel: msg.Channel})

	// Transcript before the current turn is appended, so the kernel's message
	// list ends with the new user message without duplicating it.
	priorTranscript := BuildMessages(sess)

	// Add user message
	sess.AddMessage(session.Message{
		ID:        fmt.Sprintf("msg_%d", time.Now().UnixNano()),
		Role:      "user",
		Content:   msg.Text,
		CreatedAt: time.Now(),
	})

	handled := false
	if a.kernel != nil && a.kernelEnabled {
		handled, err = a.runKernel(ctx, ch, sess, msg, priorTranscript)
	}

	if !handled {
		// Prepare layered prompt context once for this turn.
		frame := a.preparePromptFrame(ctx, sess, msg)
		systemPrompt := a.renderPromptFrame(ctx, frame, sess)

		// Compress context
		if _, cerr := a.deps.Memory.ContextMgr.Compress(ctx, sess, systemPrompt); cerr != nil {
			slog.Warn("context manager compression failed", "session", sess.ID, "err", cerr)
		}

		// Execute strategy
		err = a.strategy.Execute(ctx, a, ch, msg, sess, frame)
	}

	// Save user message to memory
	if a.deps.Memory.Store != nil {
		if saveErr := a.deps.Memory.Store.Save(ctx, memory.Entry{
			ID:        fmt.Sprintf("conv_%d", time.Now().UnixNano()),
			Scope:     memory.ScopeSession,
			SessionID: sess.ID,
			Content:   msg.Text,
			Metadata:  map[string]string{"role": "user", "channel": msg.Channel},
			CreatedAt: time.Now(),
		}); saveErr != nil {
			slog.Warn("failed to save memory", "err", saveErr)
		}
	}

	if err == nil {
		a.recordVerifiedStrategy(ctx, sess, msg)
	}

	// Persist
	if err := a.deps.Core.Sessions.Persist(ctx, sess); err != nil {
		slog.Error("failed to persist session", "err", err)
	}

	a.eventBus.Publish(SessionEnded{SessionID: sess.ID, Succeeded: err == nil, DurationMs: time.Since(start).Milliseconds()})
	return err
}

// runKernel routes one turn through the cognitive kernel, reusing the agent's
// tool pipeline, memory retrieval, and session. Chat falls back to the legacy
// loop only when agent.kernel_fallback_enabled is explicitly true; otherwise a
// kernel failure is surfaced without re-running outside episode governance.
func (a *Agent) runKernel(ctx context.Context, ch channel.Channel, sess *session.Session, msg channel.InboundMessage, priorTranscript []mind.CompletionMessage) (bool, error) {
	target := channel.MessageTarget{Channel: msg.Channel, ChannelID: msg.ChannelID}
	transcript := append(priorTranscript, mind.CompletionMessage{Role: "user", Content: msg.Text})
	activityClass := "chat"
	if SubagentContextFromCtx(ctx) != nil {
		activityClass = "subagent"
	}

	req := CognitiveRequest{
		SessionID: sess.ID,
		// A sub-agent episode runs inside its spawning episode's tool dispatch, which
		// installed that parent's id in ctx (EpisodeIDToCtx). Recording it links the
		// child episode to its parent (§4.3); top-level chat/heart episodes read "".
		ParentEpisodeID: EpisodeIDFromCtx(ctx),
		Goal:            "Respond to the user's message",
		Trigger:         msg.Text,
		Persona:         a.deps.Core.Cfg.Personality,
		Rules:           a.deps.Core.Cfg.PersistentRules,
		Memories:        a.buildMemoryPromptSection(ctx, msg.Text),
		Model:           a.deps.Core.LLMCfg.Model,
		Provider:        a.deps.Core.LLMCfg.Provider,
		MaxTokens:       a.deps.Core.LLMCfg.MaxTokens,
		ActivityClass:   activityClass,
		Transcript:      transcript,
		ToolDefs:        a.buildToolDefs(),
		Invoke: func(ctx context.Context, iteration int, call mind.ToolUseBlock) (string, bool) {
			return a.invokeTool(ctx, ch, sess, target, iteration, call, "", false)
		},
	}

	outcome, kerr := a.kernel.Execute(ctx, req)
	// Stash the faithful Outcome so a sub-agent caller can read its structured
	// Status (Spawn → buildResult), not infer it from the reply text. Only on the
	// sub-agent path: a sub-agent Agent is built fresh per Spawn (a single
	// HandleMessage), so this write is race-free, whereas the long-lived chat agent
	// is shared across concurrent per-session turns and must never write this field.
	if SubagentContextFromCtx(ctx) != nil {
		oc := outcome
		a.lastKernelOutcome = &oc
	}
	var strictErr error
	if kerr != nil || outcome.Status == "failed" {
		// Sub-agent episodes are terminal: the episode already 交账'd (failEpisode
		// wrote the journal outcome). Falling back to the legacy loop would double-run
		// the task and bypass episode governance, so surface the outcome to Spawn
		// instead of retrying. (Propagating Outcome.Status into SubAgentResult.Status
		// is a follow-up; today a failed sub-agent episode returns its summary text.)
		if SubagentContextFromCtx(ctx) != nil {
			if kerr != nil {
				return true, kerr
			}
			// fall through: a 交账'd failed outcome surfaces its summary as the result
		} else {
			if a.deps.Core.Cfg.KernelFallbackEnabled {
				slog.Warn("agent: cognitive kernel failed; falling back to legacy loop",
					"session", sess.ID, "err", kerr, "status", outcome.Status)
				return false, nil
			}
			slog.Warn("agent: cognitive kernel failed; legacy fallback disabled",
				"session", sess.ID, "err", kerr, "status", outcome.Status)
			if kerr != nil {
				strictErr = kerr
			} else {
				strictErr = fmt.Errorf("cognitive kernel outcome failed")
			}
			if outcome.Reply == "" && outcome.Summary == "" {
				outcome.Summary = "I couldn't complete this turn through the governed episode kernel."
			}
		}
	}

	reply := outcome.Reply
	if reply == "" {
		reply = outcome.Summary
	}
	if reply != "" {
		sess.AddMessage(session.Message{
			ID:        fmt.Sprintf("msg_%d", time.Now().UnixNano()),
			Role:      "assistant",
			Content:   reply,
			CreatedAt: time.Now(),
		})
		if sendErr := ch.Send(ctx, channel.OutboundMessage{Channel: target.Channel, ChannelID: target.ChannelID, Text: reply}); sendErr != nil {
			slog.Warn("agent: kernel reply send failed", "session", sess.ID, "err", sendErr)
		}
	}
	return true, strictErr
}

// RunInternalEpisode fires a cognitive episode for an autonomous, non-chat
// trigger (a timer, a mail event, an internal signal). Unlike runKernel there is
// no channel and no user waiting: the episode reuses the kernel, tool pipeline,
// and world side-effects, but produces no channel reply — its outcome lands in
// the journal and world through the Runner's own ApplyOutcome. Because there is
// no interactive channel to ask, tools requiring approval are denied (the
// approval callback treats a nil channel as "cannot get sign-off"), so only
// auto-approved / read-only tools run autonomously. Each call gets its own
// "internal" session so the tool transcript is captured for replay without
// colliding with chat sessions.
// activityClass labels the episode for cost accounting (blueprint §4.11); the
// caller passes the triggering event kind. Empty records the cost unclassified.
func (a *Agent) RunInternalEpisode(ctx context.Context, idempotencyKey, goal, trigger, activityClass string) (CognitiveOutcome, error) {
	if a.kernel == nil || !a.kernelEnabled {
		return CognitiveOutcome{}, fmt.Errorf("cognitive kernel unavailable")
	}
	ctx = AgentToContext(ctx, a)
	ctx = tool.WithChannelClass(ctx, tool.ChannelClassForName("internal"))

	// A non-empty idempotency key (the triggering event id) makes the session and
	// episode deterministic, so a re-delivered event reuses the same identity and
	// the kernel can skip an already-completed episode. Empty ⇒ a fresh ad-hoc id.
	channelID := fmt.Sprintf("evt_%d", time.Now().UnixNano())
	if idempotencyKey != "" {
		channelID = "evt_" + idempotencyKey
	}
	sess, err := a.deps.Core.Sessions.Get(ctx, "internal", channelID)
	if err != nil {
		return CognitiveOutcome{}, fmt.Errorf("get internal session: %w", err)
	}
	target := channel.MessageTarget{Channel: "internal", ChannelID: channelID}

	start := time.Now()
	a.eventBus.Publish(SessionStarted{SessionID: sess.ID, Channel: "internal"})

	req := CognitiveRequest{
		SessionID:     sess.ID,
		EpisodeID:     idempotencyKey,
		Goal:          goal,
		Trigger:       trigger,
		Persona:       a.deps.Core.Cfg.Personality,
		Rules:         a.deps.Core.Cfg.PersistentRules,
		Memories:      a.buildMemoryPromptSection(ctx, trigger),
		Model:         a.deps.Core.LLMCfg.Model,
		Provider:      a.deps.Core.LLMCfg.Provider,
		MaxTokens:     a.deps.Core.LLMCfg.MaxTokens,
		ActivityClass: activityClass,
		Transcript:    []mind.CompletionMessage{{Role: "user", Content: trigger}},
		ToolDefs:      a.buildToolDefs(),
		Invoke: func(ctx context.Context, iteration int, call mind.ToolUseBlock) (string, bool) {
			// nil channel ⇒ approval-required tools are denied (see handleApproval).
			return a.invokeTool(ctx, nil, sess, target, iteration, call, "", false)
		},
	}

	outcome, kerr := a.kernel.Execute(ctx, req)
	if perr := a.deps.Core.Sessions.Persist(ctx, sess); perr != nil {
		slog.Error("agent: persist internal session failed", "session", sess.ID, "err", perr)
	}
	a.eventBus.Publish(SessionEnded{
		SessionID:  sess.ID,
		Succeeded:  kerr == nil && outcome.Status != "failed",
		DurationMs: time.Since(start).Milliseconds(),
	})
	if kerr != nil {
		return outcome, fmt.Errorf("internal episode: %w", kerr)
	}
	return outcome, nil
}

// buildSystemPrompt constructs the system prompt from personality + memories + skills + profile.
func (a *Agent) buildSystemPrompt(ctx context.Context, sess *session.Session, userText string) string {
	frame := a.buildPromptFrame(ctx, userText)
	return a.renderPromptFrame(ctx, frame, sess)
}

// buildToolDefs returns tool definitions for the LLM request.
func (a *Agent) buildToolDefs() []mind.ToolDefinition {
	tools := a.deps.Core.Tools.All()
	defs := make([]mind.ToolDefinition, 0, len(tools))
	for _, t := range tools {
		defs = append(defs, mind.ToolDefinition{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	return defs
}

// executeToolCall runs a single tool through the interceptor chain, records the
// tool_result in the session, and emits ToolExecuted.
func (a *Agent) executeToolCall(ctx context.Context, ch channel.Channel, sess *session.Session, target channel.MessageTarget, iteration int, tc mind.ToolUseBlock, budgetWarning string) {
	a.invokeTool(ctx, ch, sess, target, iteration, tc, budgetWarning, true)
}

// invokeTool runs one tool call through the full security pipeline (interceptor
// chain, approval, PostToolUse hooks), emits ToolExecuted/ToolRoundTrip, and
// returns the output and error flag. Both the legacy loop and the cognitive
// kernel route tool execution through here so they share identical governance
// and replay recording. recordToSession controls whether the tool_result is
// written back to the session transcript: the legacy loop rebuilds each request
// from session history (so it needs it), while the kernel keeps its own message
// list and persists only the user/assistant exchange (so it does not, avoiding
// orphan tool_results in the session).
func (a *Agent) invokeTool(ctx context.Context, ch channel.Channel, sess *session.Session, target channel.MessageTarget, iteration int, tc mind.ToolUseBlock, budgetWarning string, recordToSession bool) (string, bool) {
	call := &tool.ToolCall{
		ToolName: tc.Name, Input: tc.Input, SessionID: sess.ID,
	}
	if t, getErr := a.deps.Core.Tools.Get(tc.Name); getErr == nil {
		call.Capabilities = tool.GetCapabilities(t)
	}
	start := time.Now()
	toolCtx := agentToolContext(ctx, call.SessionID)

	result, err := a.deps.Security.Interceptor.Execute(toolCtx, call, func(ctx context.Context, call *tool.ToolCall) (*tool.ToolResult, error) {
		t, getErr := a.deps.Core.Tools.Get(call.ToolName)
		if getErr != nil {
			return &tool.ToolResult{Error: getErr.Error()}, nil
		}

		// Approval check (skip if pre-tool-use hook already approved)
		if t.RequiresApproval() && a.approvalFn != nil && !call.HookApproved {
			approved, approveErr := a.approvalFn(ctx, ch, target, call.ToolName, call.Input)
			if approveErr != nil || !approved {
				return &tool.ToolResult{Error: "tool execution denied by user"}, nil
			}
		}

		r, execErr := t.Execute(ctx, []byte(call.Input))
		if execErr != nil {
			return &tool.ToolResult{Error: execErr.Error()}, nil
		}
		// Tools report soft failures (e.g. file_edit "old_string not found") via
		// Result.Error with a nil Go error. Propagate it: dropping it makes the
		// model see an empty result on failure, and makes the action interceptor
		// treat the failed run as a success (false undo record + verified trust).
		return &tool.ToolResult{Output: r.Output, Error: r.Error}, nil
	})

	duration := time.Since(start)
	content := ""
	isError := false
	if err != nil {
		content = err.Error()
		isError = true
	} else if result != nil {
		switch {
		case result.Error != "" && result.Output != "":
			// Tools like bash/test_run report a failure marker in Error while
			// still returning stdout/stderr (or failing cases) in Output; the
			// model needs both, so show the output followed by the error.
			content = result.Output + "\n" + result.Error
			isError = true
		case result.Error != "":
			content = result.Error
			isError = true
		default:
			content = result.Output
			if result.Metadata["verify"] != "" {
				sess.SetMetadata("verified_tool_success", "true")
			}
		}
	}

	// Fire PostToolUse hooks
	if a.deps.Security.HookMgr != nil && a.deps.Security.HookMgr.HasPostToolUseHandlers() {
		status := "success"
		if isError {
			status = "error"
		}
		_, hookErr := a.deps.Security.HookMgr.FirePostToolUse(ctx, hook.PostToolUseEvent{
			ToolName: tc.Name,
			Input:    tc.Input,
			Output:   content,
			Status:   status,
		})
		if hookErr != nil {
			slog.Warn("agent: post-tool hook failed", "tool", tc.Name, "err", hookErr)
		}
	}

	if recordToSession {
		sess.AddMessage(session.Message{
			ID:        fmt.Sprintf("msg_%d", time.Now().UnixNano()),
			Role:      "tool_result",
			Content:   content,
			ToolName:  tc.ID,
			CreatedAt: time.Now(),
		})
	}

	a.eventBus.Publish(ToolExecuted{
		SessionID: sess.ID, ToolName: tc.Name, Succeeded: !isError,
		DurationMs: duration.Milliseconds(), Error: content,
	})
	a.eventBus.Publish(ToolRoundTrip{
		SessionID:  sess.ID,
		Iteration:  iteration,
		ToolName:   tc.Name,
		ArgsJSON:   replayRawJSONString(tc.Input),
		ResultJSON: replayToolResultJSON(result, err),
		Succeeded:  !isError,
		DurationMs: duration.Milliseconds(),
	})

	if budgetWarning != "" {
		sess.UpdateLastToolResult(budgetWarning)
	}
	return content, isError
}

func agentToolContext(ctx context.Context, sessionID string) context.Context {
	ctx = tool.WithSessionID(ctx, sessionID)
	if tool.WorkDirFromContext(ctx) != "" {
		return ctx
	}
	cwd, err := os.Getwd()
	if err != nil || cwd == "" {
		return ctx
	}
	return tool.WithWorkDir(ctx, cwd)
}

// dispatchToolsParallel executes multiple independent tool calls concurrently.
// Each call goes through the full executeToolCall pipeline.
func (a *Agent) dispatchToolsParallel(ctx context.Context, ch channel.Channel, sess *session.Session, target channel.MessageTarget, iteration int, calls []mind.ToolUseBlock, budgetWarning string) {
	if len(calls) == 0 {
		return
	}
	for _, batch := range a.scheduleToolBatches(calls) {
		if len(batch) == 1 {
			a.executeToolCall(ctx, ch, sess, target, iteration, batch[0], budgetWarning)
			continue
		}
		var wg sync.WaitGroup
		for i := range batch {
			wg.Add(1)
			go func(tc mind.ToolUseBlock) {
				defer wg.Done()
				defer func() {
					if r := recover(); r != nil {
						slog.Error("agent: panic in parallel tool dispatch", "tool", tc.Name, "panic", r)
					}
				}()
				a.executeToolCall(ctx, ch, sess, target, iteration, tc, budgetWarning)
			}(batch[i])
		}
		wg.Wait()
	}
}

func replayToolResultJSON(result *tool.ToolResult, err error) json.RawMessage {
	if err != nil {
		return replayMarshalJSON(tool.ToolResult{Error: err.Error()})
	}
	if result == nil {
		return replayMarshalJSON(tool.ToolResult{})
	}
	return replayMarshalJSON(result)
}

func (a *Agent) recordVerifiedStrategy(ctx context.Context, sess *session.Session, msg channel.InboundMessage) {
	if a == nil || sess == nil || sess.GetMetadata("verified_tool_success") != "true" {
		return
	}
	if a.deps.Memory.Cortex == nil || a.deps.Memory.Cortex.GetProcedural() == nil {
		return
	}
	tools := toolSequenceFromHistory(sess.History())
	if len(tools) == 0 {
		return
	}
	hints := []string{
		"channel=" + msg.Channel,
	}
	if plan := strings.TrimSpace(sess.GetMetadata("plan")); plan != "" {
		hints = append(hints, "active_plan="+plan)
	}
	if err := a.deps.Memory.Cortex.GetProcedural().RecordStrategy(ctx, msg.Text, tools, hints, true, sess.ID, msg.UserID); err != nil {
		slog.Warn("agent: record procedural strategy failed", "err", err)
	}
}

func toolSequenceFromHistory(history []session.Message) []string {
	tools := make([]string, 0)
	for _, m := range history {
		if m.Role != "tool_use" || m.ToolName == "" {
			continue
		}
		tools = append(tools, m.ToolName)
	}
	return tools
}
