package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Forest-Isle/daimon/internal/channel"
	"github.com/Forest-Isle/daimon/internal/config"
	"github.com/Forest-Isle/daimon/internal/hook"
	"github.com/Forest-Isle/daimon/internal/mind"
	"github.com/Forest-Isle/daimon/internal/session"
	"github.com/Forest-Isle/daimon/internal/store"
	"github.com/Forest-Isle/daimon/internal/tool"
)

// --- Mock hook handlers ---

// denyHookHandler is a PreToolUse handler that denies all tool calls.
type denyHookHandler struct {
	reason string
}

func (h *denyHookHandler) OnPreToolUse(_ context.Context, _ hook.PreToolUseEvent) (hook.PreToolUseResult, error) {
	return hook.PreToolUseResult{
		Action: "deny",
		Reason: h.reason,
	}, nil
}

// allowHookHandler is a PreToolUse handler that allows all tool calls (skips approval).
type allowHookHandler struct{}

func (h *allowHookHandler) OnPreToolUse(_ context.Context, _ hook.PreToolUseEvent) (hook.PreToolUseResult, error) {
	return hook.PreToolUseResult{
		Action: "allow",
	}, nil
}

// trackingPostHookHandler is a PostToolUse handler that tracks calls.
type trackingPostHookHandler struct {
	callCount atomic.Int32
	lastEvent hook.PostToolUseEvent
}

type failingPostHookHandler struct {
	err error
}

func (h *failingPostHookHandler) OnPostToolUse(_ context.Context, _ hook.PostToolUseEvent) (hook.PostToolUseResult, error) {
	return hook.PostToolUseResult{}, h.err
}

func (h *trackingPostHookHandler) OnPostToolUse(_ context.Context, event hook.PostToolUseEvent) (hook.PostToolUseResult, error) {
	h.callCount.Add(1)
	h.lastEvent = event
	return hook.PostToolUseResult{}, nil
}

// contextInjectorHandler is an OnUserMessage handler that injects context.
type contextInjectorHandler struct {
	context string
}

func (h *contextInjectorHandler) OnUserMessage(_ context.Context, _ hook.OnUserMessageEvent) (hook.OnUserMessageResult, error) {
	return hook.OnUserMessageResult{
		InjectedContext: []string{h.context},
	}, nil
}

// --- Mock tool for hook tests ---

type hookTestTool struct {
	name      string
	execCount atomic.Int32
}

func (t *hookTestTool) Name() string                { return t.name }
func (t *hookTestTool) Description() string         { return "test tool for hooks" }
func (t *hookTestTool) InputSchema() map[string]any { return nil }
func (t *hookTestTool) RequiresApproval() bool      { return false }
func (t *hookTestTool) Execute(_ context.Context, _ []byte) (tool.Result, error) {
	t.execCount.Add(1)
	return tool.Result{Output: "executed " + t.name}, nil
}

func TestPreToolUseDenyPreventsExecution(t *testing.T) {
	db := newTestDB(t)
	registry := tool.NewRegistry()

	mockTool := &hookTestTool{name: "test_tool"}
	registry.Register(mockTool)

	hookMgr := hook.NewManager()
	hookMgr.RegisterPreToolUse(&denyHookHandler{reason: "blocked by test"})

	deps := AgentDeps{
		Core: CoreDeps{
			Tools: registry,
			DB:    db,
			Cfg:   config.AgentConfig{},
		},
		Security: SecurityDeps{
			HookMgr: hookMgr,
		},
	}.WithDefaults()
	rt := NewAgent(&deps, &LinearLoop{}, NewEventBus())

	sess := concurrentTestSession()
	tc := mind.ToolUseBlock{ID: "tc_1", Name: "test_tool", Input: "{}"}

	rt.executeToolCall(context.Background(), nil, sess, channel.MessageTarget{}, 0, tc, "")

	// Tool should NOT have been executed
	if mockTool.execCount.Load() != 0 {
		t.Errorf("tool was executed %d times, expected 0 (should have been denied by hook)", mockTool.execCount.Load())
	}

	// Verify denial in session history
	history := sess.History()
	if len(history) == 0 || history[len(history)-1].Role != "tool_result" {
		t.Fatal("expected tool_result message in session history")
	}
	lastMsg := history[len(history)-1]
	if !strings.Contains(lastMsg.Content, "denied by hook: blocked by test") {
		t.Errorf("expected output to contain denial reason, got: %s", lastMsg.Content)
	}
}

func TestPreToolUseAllowSkipsApproval(t *testing.T) {
	db := newTestDB(t)
	registry := tool.NewRegistry()

	// Tool that requires approval
	mockTool := &approvalTestTool{name: "approval_tool"}
	registry.Register(mockTool)

	hookMgr := hook.NewManager()
	hookMgr.RegisterPreToolUse(&allowHookHandler{})

	// Approval func that always denies (should be skipped)
	denyApproval := func(_ context.Context, _ channel.Channel, _ channel.MessageTarget, _ string, _ string) (bool, error) {
		return false, nil
	}

	deps := AgentDeps{
		Core: CoreDeps{
			Tools: registry,
			DB:    db,
			Cfg:   config.AgentConfig{},
		},
		Security: SecurityDeps{
			HookMgr: hookMgr,
		},
	}.WithDefaults()
	rt := NewAgent(&deps, &LinearLoop{}, NewEventBus())
	rt.SetApprovalFunc(denyApproval)

	sess := concurrentTestSession()
	tc := mind.ToolUseBlock{ID: "tc_1", Name: "approval_tool", Input: "{}"}

	rt.executeToolCall(context.Background(), nil, sess, channel.MessageTarget{}, 0, tc, "")

	// Tool SHOULD have been executed (hook allowed, skipping approval)
	if mockTool.execCount.Load() != 1 {
		t.Errorf("tool was executed %d times, expected 1 (hook should have skipped approval)", mockTool.execCount.Load())
	}

}

// approvalTestTool requires approval.
type approvalTestTool struct {
	name      string
	execCount atomic.Int32
}

func (t *approvalTestTool) Name() string                { return t.name }
func (t *approvalTestTool) Description() string         { return "test tool requiring approval" }
func (t *approvalTestTool) InputSchema() map[string]any { return nil }
func (t *approvalTestTool) RequiresApproval() bool      { return true }
func (t *approvalTestTool) Execute(_ context.Context, _ []byte) (tool.Result, error) {
	t.execCount.Add(1)
	return tool.Result{Output: "executed " + t.name}, nil
}

func TestPostToolUseAuditHandlerCalled(t *testing.T) {
	db := newTestDB(t)
	registry := tool.NewRegistry()

	mockTool := &hookTestTool{name: "audited_tool"}
	registry.Register(mockTool)

	tracker := &trackingPostHookHandler{}
	hookMgr := hook.NewManager()
	hookMgr.RegisterPostToolUse(tracker)

	deps := AgentDeps{
		Core: CoreDeps{
			Tools: registry,
			DB:    db,
			Cfg:   config.AgentConfig{},
		},
		Security: SecurityDeps{
			HookMgr: hookMgr,
		},
	}.WithDefaults()
	rt := NewAgent(&deps, &LinearLoop{}, NewEventBus())

	sess := concurrentTestSession()
	tc := mind.ToolUseBlock{ID: "tc_1", Name: "audited_tool", Input: `{"cmd":"test"}`}

	rt.executeToolCall(context.Background(), nil, sess, channel.MessageTarget{}, 0, tc, "")

	// Tool should have been executed
	if mockTool.execCount.Load() != 1 {
		t.Errorf("tool was executed %d times, expected 1", mockTool.execCount.Load())
	}

	// PostToolUse handler should have been called
	if tracker.callCount.Load() != 1 {
		t.Errorf("post-tool-use handler called %d times, expected 1", tracker.callCount.Load())
	}

	// Verify event data
	if tracker.lastEvent.ToolName != "audited_tool" {
		t.Errorf("expected tool name 'audited_tool', got '%s'", tracker.lastEvent.ToolName)
	}
	if tracker.lastEvent.Status != "success" {
		t.Errorf("expected status 'success', got '%s'", tracker.lastEvent.Status)
	}
	// Verify output in session history
	history := sess.History()
	if len(history) > 0 && history[len(history)-1].Role == "tool_result" {
		if history[len(history)-1].Content != "executed audited_tool" {
			t.Errorf("unexpected output: %s", history[len(history)-1].Content)
		}
	} else {
		t.Error("expected tool_result message in session history")
	}
}

func TestPostToolUseErrorPreservesSuccessfulToolResult(t *testing.T) {
	registry := tool.NewRegistry()
	registry.Register(&hookTestTool{name: "successful_tool"})

	hookMgr := hook.NewManager()
	hookMgr.RegisterPostToolUse(&failingPostHookHandler{err: errors.New("post hook failed")})

	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	bus := NewEventBus()
	events := make(chan ToolExecuted, 1)
	sub := bus.Subscribe(func(event Event) {
		if executed, ok := event.(ToolExecuted); ok {
			events <- executed
		}
	})
	defer sub.Unsubscribe()

	deps := AgentDeps{
		Core: CoreDeps{
			Tools: registry,
			Cfg:   config.AgentConfig{},
		},
		Security: SecurityDeps{HookMgr: hookMgr},
	}.WithDefaults()
	rt := NewAgent(&deps, &LinearLoop{}, bus)
	sess := concurrentTestSession()
	tc := mind.ToolUseBlock{ID: "tc_1", Name: "successful_tool", Input: "{}"}

	content, isError := rt.invokeTool(context.Background(), nil, sess, channel.MessageTarget{}, 0, tc, "", true)
	if isError || content != "executed successful_tool" {
		t.Fatalf("tool result = (%q, %v), want successful output", content, isError)
	}
	select {
	case event := <-events:
		if !event.Succeeded || event.Error != "executed successful_tool" {
			t.Fatalf("ToolExecuted = %#v, want successful event", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ToolExecuted was not published")
	}
	if got := logs.String(); !strings.Contains(got, "agent: post-tool hook failed") || !strings.Contains(got, "post hook failed") {
		t.Fatalf("log output = %q, want post-hook warning", got)
	}
}

func TestOnUserMessageContextInjection(t *testing.T) {
	hookMgr := hook.NewManager()
	hookMgr.RegisterOnUserMessage(&contextInjectorHandler{
		context: "Current time: 2026-04-02T12:00:00Z",
	})

	deps := AgentDeps{
		Core: CoreDeps{
			Cfg: config.AgentConfig{
				SystemPrompt: "You are a helpful assistant.",
			},
		},
		Security: SecurityDeps{
			HookMgr: hookMgr,
		},
	}.WithDefaults()
	rt := NewAgent(&deps, &LinearLoop{}, NewEventBus())

	// Build system prompt
	ctx := context.Background()
	systemPrompt := rt.buildSystemPrompt(ctx, nil, "What time is it?")

	// Simulate OnUserMessage hook firing (as done in HandleMessage)
	if rt.deps.Security.HookMgr != nil && rt.deps.Security.HookMgr.HasOnUserMessageHandlers() {
		msgResult, _ := rt.deps.Security.HookMgr.FireOnUserMessage(ctx, hook.OnUserMessageEvent{
			Channel:   "test",
			ChannelID: "test-channel",
			UserID:    "user-1",
			Text:      "What time is it?",
		})
		if len(msgResult.InjectedContext) > 0 {
			for _, ic := range msgResult.InjectedContext {
				systemPrompt += "\n\n## Environment Context\n" + ic
			}
		}
	}

	// Verify injected context appears in system prompt
	if !containsSubstring(systemPrompt, "Current time: 2026-04-02T12:00:00Z") {
		t.Errorf("expected injected context in system prompt, got: %s", systemPrompt)
	}

	if !containsSubstring(systemPrompt, "## Environment Context") {
		t.Errorf("expected '## Environment Context' section in system prompt, got: %s", systemPrompt)
	}
}

func TestPermissionEngineDenyPreventsExecution(t *testing.T) {
	db := newTestDB(t)
	registry := tool.NewRegistry()

	mockTool := &hookTestTool{name: "bash"}
	registry.Register(mockTool)

	// Permission engine that denies bash with rm commands
	rules := []tool.PermissionRule{
		{Tool: "bash", Pattern: "rm *", Action: "deny"},
	}
	permEngine := tool.NewPermissionEngine(rules, "ask", nil)

	deps := AgentDeps{
		Core: CoreDeps{
			Tools: registry,
			DB:    db,
			Cfg:   config.AgentConfig{},
		},
		Security: SecurityDeps{
			PermEngine: permEngine,
		},
	}.WithDefaults()
	rt := NewAgent(&deps, &LinearLoop{}, NewEventBus())

	sess := concurrentTestSession()
	tc := mind.ToolUseBlock{ID: "tc_1", Name: "bash", Input: `{"command":"rm -rf /tmp/test"}`}

	rt.executeToolCall(context.Background(), nil, sess, channel.MessageTarget{}, 0, tc, "")

	// Tool should NOT have been executed
	if mockTool.execCount.Load() != 0 {
		t.Errorf("tool was executed %d times, expected 0 (should have been denied by permission engine)", mockTool.execCount.Load())
	}

	// Verify denial in session history
	history := sess.History()
	if len(history) == 0 || history[len(history)-1].Role != "tool_result" {
		t.Fatal("expected tool_result message in session history")
	}
	lastMsg := history[len(history)-1]
	if !strings.Contains(lastMsg.Content, "denied") && !strings.Contains(lastMsg.Content, "Permission") {
		t.Errorf("expected denial message in session history, got: %s", lastMsg.Content)
	}
}

func TestHookAndPermissionEngineIntegration(t *testing.T) {
	db := newTestDB(t)
	registry := tool.NewRegistry()

	mockTool := &hookTestTool{name: "test_tool"}
	registry.Register(mockTool)

	// Hook that passes through (doesn't deny or allow)
	hookMgr := hook.NewManager()
	tracker := &trackingPostHookHandler{}
	hookMgr.RegisterPostToolUse(tracker)

	// Permission engine allows all by default
	permEngine := tool.NewPermissionEngine(nil, "allow", nil)

	deps := AgentDeps{
		Core: CoreDeps{
			Tools: registry,
			DB:    db,
			Cfg:   config.AgentConfig{},
		},
		Security: SecurityDeps{
			HookMgr:    hookMgr,
			PermEngine: permEngine,
		},
	}.WithDefaults()
	rt := NewAgent(&deps, &LinearLoop{}, NewEventBus())

	sess := concurrentTestSession()
	tc := mind.ToolUseBlock{ID: "tc_1", Name: "test_tool", Input: "{}"}

	rt.executeToolCall(context.Background(), nil, sess, channel.MessageTarget{}, 0, tc, "")

	// Tool should have been executed
	if mockTool.execCount.Load() != 1 {
		t.Errorf("tool was executed %d times, expected 1", mockTool.execCount.Load())
	}
	// Post hook should have been called
	if tracker.callCount.Load() != 1 {
		t.Errorf("post-tool-use handler called %d times, expected 1", tracker.callCount.Load())
	}
}

func TestConcurrentExecutionWithHooks(t *testing.T) {
	db := newTestDB(t)
	registry := tool.NewRegistry()

	tools := make([]*hookTestTool, 3)
	for i := 0; i < 3; i++ {
		tools[i] = &hookTestTool{name: fmt.Sprintf("tool_%d", i)}
		registry.Register(tools[i])
	}

	tracker := &trackingPostHookHandler{}
	hookMgr := hook.NewManager()
	hookMgr.RegisterPostToolUse(tracker)

	deps := AgentDeps{
		Core: CoreDeps{
			Tools: registry,
			DB:    db,
			Cfg:   config.AgentConfig{},
			ToolsCfg: config.ToolsConfig{
				ConcurrentExecution: config.ConcurrentExecutionConfig{
					Enabled:        true,
					MaxConcurrency: 4,
				},
			},
		},
		Security: SecurityDeps{
			HookMgr: hookMgr,
		},
	}.WithDefaults()
	rt := NewAgent(&deps, &LinearLoop{}, NewEventBus())

	sess := concurrentTestSession()
	toolCalls := []mind.ToolUseBlock{
		{ID: "tc_0", Name: "tool_0", Input: "{}"},
		{ID: "tc_1", Name: "tool_1", Input: "{}"},
		{ID: "tc_2", Name: "tool_2", Input: "{}"},
	}

	for _, tc := range toolCalls {
		rt.executeToolCall(context.Background(), nil, sess, channel.MessageTarget{}, 0, tc, "")
	}

	// All tools should have been executed
	for i, tl := range tools {
		if tl.execCount.Load() != 1 {
			t.Errorf("tool_%d executed %d times, expected 1", i, tl.execCount.Load())
		}
	}

	// Post hook should have been called for each tool
	if tracker.callCount.Load() != 3 {
		t.Errorf("post-tool-use handler called %d times, expected 3", tracker.callCount.Load())
	}

	// Verify results were added to session
	history := sess.History()
	toolResults := 0
	for _, m := range history {
		if m.Role == "tool_result" {
			toolResults++
		}
	}
	if toolResults != 3 {
		t.Errorf("expected 3 tool_result messages, got %d", toolResults)
	}
}

// containsSubstring checks if s contains substr.
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// newTestDB opens a store.DB on a temporary SQLite file.
func newTestDB(t *testing.T) *store.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "daimon_test.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open(%q) failed: %v", dbPath, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// concurrentTestSession creates a basic test session.
func concurrentTestSession() *session.Session {
	return &session.Session{
		ID:        "test-session",
		Channel:   "test",
		ChannelID: "test-channel",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Metadata:  make(map[string]string),
	}
}
