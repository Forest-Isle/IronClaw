package action

import (
	"context"
	"strings"
	"testing"

	"github.com/Forest-Isle/daimon/internal/tool"
)

// stubGate is a programmable ValueGate for interceptor tests.
type stubGate struct {
	ref       string
	permitted bool
	calls     int
	gotClass  Class
	gotKey    string
}

func (g *stubGate) Permit(_ context.Context, class Class, contextKey string) (string, bool) {
	g.calls++
	g.gotClass = class
	g.gotKey = contextKey
	return g.ref, g.permitted
}

func TestValueGateBlocksUnpermittedIrreversible(t *testing.T) {
	store := openActionTestStore(t)
	gate := &stubGate{permitted: false}
	ic := NewInterceptorWithGate(store, nil, gate)
	ctx := context.Background()

	executed := false
	final := func(_ context.Context, _ *tool.ToolCall) (*tool.ToolResult, error) {
		executed = true
		return &tool.ToolResult{Output: "ran"}, nil
	}
	call := &tool.ToolCall{ToolName: "bash", Input: `{"command":"rm -rf /data"}`}
	res, err := ic.Intercept(ctx, call, final)
	if err != nil {
		t.Fatalf("Intercept() error = %v", err)
	}
	if executed {
		t.Fatal("blocked action must not execute the tool")
	}
	if res.Error == "" || res.Metadata["value_blocked"] != "true" {
		t.Fatalf("expected a value_blocked error result, got %+v", res)
	}
	if gate.gotClass != Irreversible || gate.gotKey != "bash" {
		t.Fatalf("gate consulted with class=%v key=%q", gate.gotClass, gate.gotKey)
	}
	// A blocked action records no trust attempt — it never ran.
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM trust_ledger`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("blocked action recorded %d trust rows, want 0", count)
	}
}

func TestValueGatePermitsAndStampsRef(t *testing.T) {
	store := openActionTestStore(t)
	gate := &stubGate{ref: "value:v-bash-tmp-only", permitted: true}
	ic := NewInterceptorWithGate(store, nil, gate)
	ctx := context.Background()

	final := func(_ context.Context, _ *tool.ToolCall) (*tool.ToolResult, error) {
		return &tool.ToolResult{Output: "ran"}, nil
	}
	call := &tool.ToolCall{ToolName: "bash", Input: `{"command":"rm -rf /tmp/scratch"}`}
	res, err := ic.Intercept(ctx, call, final)
	if err != nil {
		t.Fatalf("Intercept() error = %v", err)
	}
	if res.Metadata["value_ref"] != "value:v-bash-tmp-only" {
		t.Fatalf("value_ref = %q, want value:v-bash-tmp-only", res.Metadata["value_ref"])
	}
	if res.Metadata["action_class"] != "irreversible" {
		t.Fatalf("action_class = %q, want irreversible", res.Metadata["action_class"])
	}
}

func TestValueGateSkippedForReversible(t *testing.T) {
	store := openActionTestStore(t)
	gate := &stubGate{permitted: false} // would block if consulted
	ic := NewInterceptorWithGate(store, nil, gate)
	ctx := context.Background()

	final := func(_ context.Context, _ *tool.ToolCall) (*tool.ToolResult, error) {
		return &tool.ToolResult{Output: "ran"}, nil
	}
	call := &tool.ToolCall{ToolName: "world_edit", Capabilities: reversibleCaps()}
	res, err := ic.Intercept(ctx, call, final)
	if err != nil {
		t.Fatalf("Intercept() error = %v", err)
	}
	if gate.calls != 0 {
		t.Fatal("reversible (low-risk) action must not consult the value gate")
	}
	if res.Metadata["value_ref"] != "trust:ask_first" {
		t.Fatalf("value_ref = %q, want trust:ask_first", res.Metadata["value_ref"])
	}
	if !strings.HasPrefix(res.Metadata["value_ref"], "trust:") {
		t.Fatalf("value_ref = %q, want trust:<level>", res.Metadata["value_ref"])
	}
}

func TestNilGateLeavesObserveOnly(t *testing.T) {
	store := openActionTestStore(t)
	ic := NewInterceptor(store, nil) // nil gate
	ctx := context.Background()

	executed := false
	final := func(_ context.Context, _ *tool.ToolCall) (*tool.ToolResult, error) {
		executed = true
		return &tool.ToolResult{Output: "ran"}, nil
	}
	call := &tool.ToolCall{ToolName: "bash", Input: `{"command":"rm -rf /data"}`}
	if _, err := ic.Intercept(ctx, call, final); err != nil {
		t.Fatalf("Intercept() error = %v", err)
	}
	if !executed {
		t.Fatal("with nil gate, irreversible action must still execute (observe-only)")
	}
}
