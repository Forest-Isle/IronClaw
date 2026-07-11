package hook

import (
	"context"
	"errors"
	"testing"
)

func TestPreToolUseChainFirstWins(t *testing.T) {
	m := NewManager()

	// First handler passes through
	m.RegisterPreToolUse(&SafetyAnalyzerHandler{BlockPatterns: []string{"harmless_pattern_xyz"}})
	// Second handler denies
	m.RegisterPreToolUse(&SafetyAnalyzerHandler{BlockPatterns: []string{"dangerous"}})

	result, err := m.FirePreToolUse(context.Background(), PreToolUseEvent{
		ToolName: "bash",
		Input:    `{"command":"do something dangerous"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "deny" {
		t.Errorf("expected deny, got %s", result.Action)
	}
}

func TestPreToolUseAllPassthrough(t *testing.T) {
	m := NewManager()
	m.RegisterPreToolUse(&SafetyAnalyzerHandler{BlockPatterns: []string{"xyz_not_in_input"}})

	result, err := m.FirePreToolUse(context.Background(), PreToolUseEvent{
		ToolName: "bash",
		Input:    `{"command":"git status"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "passthrough" {
		t.Errorf("expected passthrough, got %s", result.Action)
	}
}

func TestPostToolUseAllHandlersCalled(t *testing.T) {
	m := NewManager()
	var callCount int

	// Custom handler that counts calls
	m.RegisterPostToolUse(&countingPostHandler{count: &callCount})
	m.RegisterPostToolUse(&countingPostHandler{count: &callCount})

	_, err := m.FirePostToolUse(context.Background(), PostToolUseEvent{
		ToolName: "bash",
		Status:   "success",
	})
	if err != nil {
		t.Fatal(err)
	}
	if callCount != 2 {
		t.Errorf("expected 2 calls, got %d", callCount)
	}
}

func TestPostToolUseReturnsErrorAndContinues(t *testing.T) {
	m := NewManager()
	wantErr := errors.New("first post hook failed")
	wantSecondErr := errors.New("second post hook failed")
	var callCount int

	m.RegisterPostToolUse(&failingPostHandler{err: wantErr})
	m.RegisterPostToolUse(&failingPostHandler{err: wantSecondErr})
	m.RegisterPostToolUse(&countingPostHandler{count: &callCount})

	_, err := m.FirePostToolUse(context.Background(), PostToolUseEvent{
		ToolName: "bash",
		Status:   "success",
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("FirePostToolUse error = %v, want %v", err, wantErr)
	}
	if !errors.Is(err, wantSecondErr) {
		t.Fatalf("FirePostToolUse error = %v, want %v", err, wantSecondErr)
	}
	if callCount != 1 {
		t.Errorf("handler after failure called %d times, want 1", callCount)
	}
}

type failingPostHandler struct {
	err error
}

func (h *failingPostHandler) OnPostToolUse(_ context.Context, _ PostToolUseEvent) (PostToolUseResult, error) {
	return PostToolUseResult{}, h.err
}

type countingPostHandler struct {
	count *int
}

func (h *countingPostHandler) OnPostToolUse(_ context.Context, _ PostToolUseEvent) (PostToolUseResult, error) {
	*h.count++
	return PostToolUseResult{}, nil
}

func TestNoHandlersIsNoop(t *testing.T) {
	m := NewManager()

	result, err := m.FirePreToolUse(context.Background(), PreToolUseEvent{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "passthrough" {
		t.Errorf("expected passthrough with no handlers, got %s", result.Action)
	}

	if m.HasPreToolUseHandlers() {
		t.Error("should have no PreToolUse handlers")
	}
}

func TestSafetyAnalyzerBlocks(t *testing.T) {
	h := NewSafetyAnalyzerHandler([]string{"rm -rf /", "DROP TABLE"})

	result, _ := h.OnPreToolUse(context.Background(), PreToolUseEvent{
		ToolName: "bash",
		Input:    `{"command":"rm -rf / --no-preserve-root"}`,
	})
	if result.Action != "deny" {
		t.Errorf("expected deny for rm -rf, got %s", result.Action)
	}

	result, _ = h.OnPreToolUse(context.Background(), PreToolUseEvent{
		ToolName: "bash",
		Input:    `{"command":"git status"}`,
	})
	if result.Action != "passthrough" {
		t.Errorf("expected passthrough for git status, got %s", result.Action)
	}
}

func TestBuildManagerUnknownType(t *testing.T) {
	m := BuildManager(
		[]HandlerConfig{{Type: "unknown_handler"}},
		nil, nil, nil,
	)
	// Should not panic, should log warning and skip
	if m.HasPreToolUseHandlers() {
		t.Error("unknown handler type should be skipped")
	}
}
