package action

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Forest-Isle/daimon/internal/tool"
)

type trustNotifyCall struct {
	class      Class
	contextKey string
	from       Level
	to         Level
}

type stubTrustNotifier struct {
	calls []trustNotifyCall
}

func (s *stubTrustNotifier) TrustPromoted(_ context.Context, class Class, contextKey string, from, to Level) {
	s.calls = append(s.calls, trustNotifyCall{class: class, contextKey: contextKey, from: from, to: to})
}

func TestClassifyReadOnlyNotGoverned(t *testing.T) {
	c := NewClassifier()
	call := &tool.ToolCall{ToolName: "world_read", Capabilities: tool.ToolCapabilities{IsReadOnly: true}}
	_, governed := c.Classify(call)
	if governed {
		t.Fatal("read-only tool should not be governed")
	}
}

func TestClassifyBashByCommand(t *testing.T) {
	c := NewClassifier()
	cases := []struct {
		cmd  string
		want Class
	}{
		{`{"command":"ls -la"}`, Reversible},
		{`{"command":"go test ./..."}`, Reversible},
		{`{"command":"rm -rf build"}`, Irreversible},
		{`{"command":"git push --force origin main"}`, Irreversible},
		{`{"command":"dd if=/dev/zero of=disk"}`, Irreversible},
	}
	for _, tc := range cases {
		call := &tool.ToolCall{ToolName: "bash", Input: tc.cmd}
		got, governed := c.Classify(call)
		if !governed {
			t.Fatalf("bash %q should be governed", tc.cmd)
		}
		if got != tc.want {
			t.Fatalf("bash %q classified %v, want %v", tc.cmd, got, tc.want)
		}
	}
}

func TestClassifyDestructiveCapability(t *testing.T) {
	c := NewClassifier()
	call := &tool.ToolCall{ToolName: "danger_tool"}
	got, governed := c.Classify(call)
	if !governed || got != Irreversible {
		t.Fatalf("undeclared mutating tool classified %v governed=%v, want Irreversible/true", got, governed)
	}
}

func TestClassifyDefaultReversible(t *testing.T) {
	c := NewClassifier()
	call := &tool.ToolCall{ToolName: "world_edit", Capabilities: reversibleCaps()}
	got, governed := c.Classify(call)
	if !governed || got != Reversible {
		t.Fatalf("declared reversible tool classified %v governed=%v, want Reversible/true", got, governed)
	}
}

func TestHoldAwareClassifierHTTP(t *testing.T) {
	c := NewClassifierWithCompensableHTTP()
	post := &tool.ToolCall{ToolName: "http", Input: `{"method":"POST","url":"https://example.com"}`}
	got, governed := c.Classify(post)
	if !governed || got != Compensable {
		t.Fatalf("POST classified %v governed=%v, want Compensable/true", got, governed)
	}

	get := &tool.ToolCall{ToolName: "http", Input: `{"method":"GET","url":"https://example.com"}`}
	got, governed = c.Classify(get)
	if !governed || got != Reversible {
		t.Fatalf("GET classified %v governed=%v, want Reversible/true", got, governed)
	}

	defaultGot, defaultGoverned := NewClassifier().Classify(post)
	if !defaultGoverned || defaultGot != Compensable {
		t.Fatalf("default classifier changed: POST classified %v governed=%v, want Compensable/true", defaultGot, defaultGoverned)
	}
}

func TestHoldAwareClassifierSendEmail(t *testing.T) {
	c := NewClassifierWithCompensableHTTP()
	call := &tool.ToolCall{
		ToolName:     "send_email",
		Input:        `{"to":"user@example.com","subject":"Hello","body":"Body text"}`,
		Capabilities: declaredCaps("compensable"),
	}
	got, governed := c.Classify(call)
	if !governed || got != Compensable {
		t.Fatalf("send_email classified %v governed=%v, want Compensable/true", got, governed)
	}
}

func TestInterceptorRecordsReversibleAndStamps(t *testing.T) {
	store := openActionTestStore(t)
	ic := NewInterceptor(store, nil)
	ctx := context.Background()

	call := &tool.ToolCall{ToolName: "world_edit", Capabilities: reversibleCaps()}
	final := func(_ context.Context, _ *tool.ToolCall) (*tool.ToolResult, error) {
		return &tool.ToolResult{Output: "done"}, nil
	}
	res, err := ic.Intercept(ctx, call, final)
	if err != nil {
		t.Fatalf("Intercept() error = %v", err)
	}
	if res.Metadata["action_class"] != "reversible" {
		t.Fatalf("metadata action_class = %q, want reversible", res.Metadata["action_class"])
	}
	// reversible success earns a verified attempt → level promoted to AskFirst
	lvl, err := store.TrustLevel(ctx, Reversible, "world_edit")
	if err != nil {
		t.Fatalf("TrustLevel() error = %v", err)
	}
	if lvl != AskFirst {
		t.Fatalf("level = %v, want AskFirst", lvl)
	}
}

func TestInterceptorNotifiesOnTrustPromotion(t *testing.T) {
	store := openActionTestStore(t)
	ic := NewInterceptor(store, nil)
	notifier := &stubTrustNotifier{}
	ic.SetTrustNotifier(notifier)
	ctx := context.Background()
	call := &tool.ToolCall{ToolName: "world_edit", Capabilities: reversibleCaps()}
	final := func(_ context.Context, _ *tool.ToolCall) (*tool.ToolResult, error) {
		return &tool.ToolResult{Output: "done"}, nil
	}

	if _, err := ic.Intercept(ctx, call, final); err != nil {
		t.Fatalf("Intercept() error = %v", err)
	}
	if len(notifier.calls) != 1 {
		t.Fatalf("TrustPromoted calls = %d, want 1", len(notifier.calls))
	}
	want := trustNotifyCall{class: Reversible, contextKey: "world_edit", from: AskEvery, to: AskFirst}
	if notifier.calls[0] != want {
		t.Fatalf("TrustPromoted call = %#v, want %#v", notifier.calls[0], want)
	}

	if _, err := ic.Intercept(ctx, call, final); err != nil {
		t.Fatalf("Intercept() second error = %v", err)
	}
	if len(notifier.calls) != 1 {
		t.Fatalf("TrustPromoted calls after non-promotion = %d, want 1", len(notifier.calls))
	}
}

func TestInterceptorNilTrustNotifierDoesNotPanic(t *testing.T) {
	store := openActionTestStore(t)
	ic := NewInterceptor(store, nil)
	call := &tool.ToolCall{ToolName: "world_edit", Capabilities: reversibleCaps()}
	final := func(_ context.Context, _ *tool.ToolCall) (*tool.ToolResult, error) {
		return &tool.ToolResult{Output: "done"}, nil
	}
	if _, err := ic.Intercept(context.Background(), call, final); err != nil {
		t.Fatalf("Intercept() error = %v", err)
	}
}

func TestInterceptorReadOnlyNotRecorded(t *testing.T) {
	store := openActionTestStore(t)
	ic := NewInterceptor(store, nil)
	ctx := context.Background()

	call := &tool.ToolCall{ToolName: "world_read", Capabilities: tool.ToolCapabilities{IsReadOnly: true}}
	final := func(_ context.Context, _ *tool.ToolCall) (*tool.ToolResult, error) {
		return &tool.ToolResult{Output: "data"}, nil
	}
	if _, err := ic.Intercept(ctx, call, final); err != nil {
		t.Fatalf("Intercept() error = %v", err)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM trust_ledger`).Scan(&count); err != nil {
		t.Fatalf("count error = %v", err)
	}
	if count != 0 {
		t.Fatalf("trust_ledger rows = %d, want 0 for read-only", count)
	}
}

func TestInterceptorIrreversibleStaysAskEvery(t *testing.T) {
	store := openActionTestStore(t)
	ic := NewInterceptor(store, nil)
	ctx := context.Background()

	final := func(_ context.Context, _ *tool.ToolCall) (*tool.ToolResult, error) {
		return &tool.ToolResult{Output: "ok"}, nil
	}
	// many successful destructive bash runs must NOT auto-promote: irreversible
	// actions don't earn autonomy from mere execution success.
	for i := 0; i < 15; i++ {
		call := &tool.ToolCall{ToolName: "bash", Input: `{"command":"rm -rf tmp"}`}
		if _, err := ic.Intercept(ctx, call, final); err != nil {
			t.Fatalf("Intercept() error = %v", err)
		}
	}
	lvl, err := store.TrustLevel(ctx, Irreversible, "bash")
	if err != nil {
		t.Fatalf("TrustLevel() error = %v", err)
	}
	if lvl != AskEvery {
		t.Fatalf("level = %v, want AskEvery (irreversible never auto-promotes)", lvl)
	}
}

func TestInterceptorFailureNotVerified(t *testing.T) {
	store := openActionTestStore(t)
	ic := NewInterceptor(store, nil)
	ctx := context.Background()

	call := &tool.ToolCall{ToolName: "world_edit", Capabilities: reversibleCaps()}
	final := func(_ context.Context, _ *tool.ToolCall) (*tool.ToolResult, error) {
		return nil, errors.New("boom")
	}
	if _, err := ic.Intercept(ctx, call, final); err == nil {
		t.Fatal("Intercept() error = nil, want propagated error")
	}
	// failed execution records an attempt but not a verified one → no promotion
	lvl, err := store.TrustLevel(ctx, Reversible, "world_edit")
	if err != nil {
		t.Fatalf("TrustLevel() error = %v", err)
	}
	if lvl != AskEvery {
		t.Fatalf("level = %v, want AskEvery after a failed attempt", lvl)
	}
}

// TestInterceptorReportsToActionCollector pins the J12 contract: when the caller
// installs an ActionVerification collector in the context, the interceptor reports
// each governed call into it (with the same verified verdict it records in the
// ledger), and read-only calls — which return before the governed block — are not
// counted.
func TestInterceptorReportsToActionCollector(t *testing.T) {
	ctx := context.Background()
	okFinal := func(_ context.Context, _ *tool.ToolCall) (*tool.ToolResult, error) {
		return &tool.ToolResult{Output: "done"}, nil
	}
	errFinal := func(_ context.Context, _ *tool.ToolCall) (*tool.ToolResult, error) {
		return nil, errors.New("boom")
	}

	cases := []struct {
		name         string
		call         *tool.ToolCall
		final        tool.InterceptorFunc
		wantErr      bool
		wantGoverned int
		wantVerified int
	}{
		{
			// reversible + clean execution = the only path that earns a verified attempt.
			name:         "reversible_success",
			call:         &tool.ToolCall{ToolName: "world_edit", Capabilities: reversibleCaps()},
			final:        okFinal,
			wantGoverned: 1,
			wantVerified: 1,
		},
		{
			// reversible but the tool errored → governed, not verified.
			name:         "reversible_failure",
			call:         &tool.ToolCall{ToolName: "world_edit", Capabilities: reversibleCaps()},
			final:        errFinal,
			wantErr:      true,
			wantGoverned: 1,
			wantVerified: 0,
		},
		{
			// irreversible never auto-verifies on mere success → governed, not verified.
			name:         "irreversible_success",
			call:         &tool.ToolCall{ToolName: "bash", Input: `{"command":"rm -rf build"}`},
			final:        okFinal,
			wantGoverned: 1,
			wantVerified: 0,
		},
		{
			// read-only returns before the governed block → never counted.
			name:         "read_only_not_counted",
			call:         &tool.ToolCall{ToolName: "world_read", Capabilities: tool.ToolCapabilities{IsReadOnly: true}},
			final:        okFinal,
			wantGoverned: 0,
			wantVerified: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := openActionTestStore(t)
			ic := NewInterceptor(store, nil)
			coll := &tool.ActionVerification{}
			cctx := tool.WithActionCollector(ctx, coll)
			_, err := ic.Intercept(cctx, tc.call, tc.final)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Intercept() error = %v, wantErr = %v", err, tc.wantErr)
			}
			gov, ver := coll.Snapshot()
			if gov != tc.wantGoverned || ver != tc.wantVerified {
				t.Fatalf("collector = (governed=%d, verified=%d), want (%d, %d)", gov, ver, tc.wantGoverned, tc.wantVerified)
			}
		})
	}
}

// TestInterceptorNilCollectorSafe confirms a governed call with no collector in
// context is a no-op for reporting (nil-safe) and does not affect the result.
func TestInterceptorNilCollectorSafe(t *testing.T) {
	store := openActionTestStore(t)
	ic := NewInterceptor(store, nil)
	call := &tool.ToolCall{ToolName: "world_edit", Capabilities: reversibleCaps()}
	final := func(_ context.Context, _ *tool.ToolCall) (*tool.ToolResult, error) {
		return &tool.ToolResult{Output: "done"}, nil
	}
	res, err := ic.Intercept(context.Background(), call, final)
	if err != nil {
		t.Fatalf("Intercept() error = %v", err)
	}
	if res.Metadata["action_class"] != "reversible" {
		t.Fatalf("metadata action_class = %q, want reversible", res.Metadata["action_class"])
	}
}

// TestInterceptorNilStoreStillReportsToCollector guards the J12 store-nil decoupling:
// a governed action with no trust store wired must STILL be counted by the episode
// collector, so its episode is not mislabeled distill-clean. (Without a store the
// interceptor cannot earn trust, but the verification signal is about the action.)
func TestInterceptorNilStoreStillReportsToCollector(t *testing.T) {
	ic := NewInterceptor(nil, nil) // observe-only, no trust ledger
	coll := &tool.ActionVerification{}
	ctx := tool.WithActionCollector(context.Background(), coll)

	// An irreversible governed action: succeeds but is never auto-verified.
	call := &tool.ToolCall{ToolName: "bash", Input: `{"command":"rm -rf build"}`}
	final := func(_ context.Context, _ *tool.ToolCall) (*tool.ToolResult, error) {
		return &tool.ToolResult{Output: "done"}, nil
	}
	if _, err := ic.Intercept(ctx, call, final); err != nil {
		t.Fatalf("Intercept() error = %v", err)
	}
	gov, ver := coll.Snapshot()
	if gov != 1 || ver != 0 {
		t.Fatalf("collector = (governed=%d, verified=%d), want (1, 0) even with nil store", gov, ver)
	}
}

// TestInterceptorDryRunSkipsGovernedExecution: under a dry run, a governed action
// must NOT execute, must return a synthetic receipt, and must leave trust ledger
// and the episode collector untouched — the shadow brain reasons without acting.
func TestInterceptorDryRunSkipsGovernedExecution(t *testing.T) {
	store := openActionTestStore(t)
	ic := NewInterceptor(store, nil)
	coll := &tool.ActionVerification{}
	ctx := tool.WithDryRun(tool.WithActionCollector(context.Background(), coll))

	called := false
	call := &tool.ToolCall{ToolName: "world_edit", Capabilities: reversibleCaps()} // governed, reversible
	final := func(_ context.Context, _ *tool.ToolCall) (*tool.ToolResult, error) {
		called = true
		return &tool.ToolResult{Output: "REAL EXECUTION"}, nil
	}
	res, err := ic.Intercept(ctx, call, final)
	if err != nil {
		t.Fatalf("Intercept() error = %v", err)
	}
	if called {
		t.Fatal("dry-run must NOT execute the governed tool")
	}
	if res.Metadata["dry_run"] != "true" {
		t.Fatalf("dry_run metadata = %q, want true", res.Metadata["dry_run"])
	}
	if res.Metadata["action_class"] != "reversible" {
		t.Fatalf("action_class = %q, want reversible", res.Metadata["action_class"])
	}
	if res.Output == "REAL EXECUTION" {
		t.Fatal("dry-run result must be synthetic, not the tool's real output")
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM trust_ledger`).Scan(&count); err != nil {
		t.Fatalf("count error = %v", err)
	}
	if count != 0 {
		t.Fatalf("trust_ledger rows = %d, want 0 (dry-run records no trust)", count)
	}
	if gov, ver := coll.Snapshot(); gov != 0 || ver != 0 {
		t.Fatalf("collector = (%d,%d), want (0,0) (dry-run executes nothing)", gov, ver)
	}
}

// TestInterceptorDryRunPassesReadOnly: a dry run must still execute read-only
// tools — the shadow brain has to observe the world it reasons over.
func TestInterceptorDryRunPassesReadOnly(t *testing.T) {
	ic := NewInterceptor(openActionTestStore(t), nil)
	ctx := tool.WithDryRun(context.Background())

	called := false
	call := &tool.ToolCall{ToolName: "world_read", Capabilities: tool.ToolCapabilities{IsReadOnly: true}}
	final := func(_ context.Context, _ *tool.ToolCall) (*tool.ToolResult, error) {
		called = true
		return &tool.ToolResult{Output: "world data"}, nil
	}
	res, err := ic.Intercept(ctx, call, final)
	if err != nil {
		t.Fatalf("Intercept() error = %v", err)
	}
	if !called {
		t.Fatal("dry-run must still execute read-only tools (shadow needs to observe)")
	}
	if res.Output != "world data" {
		t.Fatalf("read-only output = %q, want real data through (not synthetic)", res.Output)
	}
}

// TestInterceptorDryRunDisabledExecutesForReal is the production guard: with NO
// dry-run flag, a governed action MUST execute and be recorded. The dry-run path
// must never leak into a normal request context.
func TestInterceptorDryRunDisabledExecutesForReal(t *testing.T) {
	store := openActionTestStore(t)
	ic := NewInterceptor(store, nil)
	ctx := context.Background() // plain — not a dry run

	called := false
	call := &tool.ToolCall{ToolName: "world_edit", Capabilities: reversibleCaps()}
	final := func(_ context.Context, _ *tool.ToolCall) (*tool.ToolResult, error) {
		called = true
		return &tool.ToolResult{Output: "done"}, nil
	}
	if _, err := ic.Intercept(ctx, call, final); err != nil {
		t.Fatalf("Intercept() error = %v", err)
	}
	if !called {
		t.Fatal("production (no dry-run) MUST execute the governed tool")
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM trust_ledger`).Scan(&count); err != nil {
		t.Fatalf("count error = %v", err)
	}
	if count != 1 {
		t.Fatalf("trust_ledger rows = %d, want 1 (production records the attempt)", count)
	}
}

func TestInterceptorHoldProducerQueuesCompensable(t *testing.T) {
	store := openActionTestStore(t)
	ic := NewInterceptorWithGateAndHold(store, NewClassifierWithCompensableHTTP(), nil, true, 120*time.Second)
	coll := &tool.ActionVerification{}
	ctx := tool.WithActionCollector(context.Background(), coll)

	called := false
	call := &tool.ToolCall{ToolName: "http", Input: `{"method":"POST","url":"https://example.com"}`}
	final := func(_ context.Context, _ *tool.ToolCall) (*tool.ToolResult, error) {
		called = true
		return &tool.ToolResult{Output: "executed"}, nil
	}
	res, err := ic.Intercept(ctx, call, final)
	if err != nil {
		t.Fatalf("Intercept() error = %v", err)
	}
	if called {
		t.Fatal("held compensable action must not execute immediately")
	}
	// The queued action counts as a governed, unverified action so the episode
	// that queued it is not falsely distill-clean.
	if governed, verified := coll.Snapshot(); governed != 1 || verified != 0 {
		t.Fatalf("collector snapshot = (governed=%d, verified=%d), want (1, 0)", governed, verified)
	}
	if res.Metadata["held"] != "true" || res.Metadata["action_class"] != "compensable" || res.Metadata["hold_id"] == "" {
		t.Fatalf("metadata = %#v, want held compensable hold id", res.Metadata)
	}
	if !strings.Contains(res.Output, "daimon holds recall "+res.Metadata["hold_id"]) {
		t.Fatalf("held output missing recall command: %q", res.Output)
	}

	holds, err := store.ListPendingHolds(ctx)
	if err != nil {
		t.Fatalf("ListPendingHolds() error = %v", err)
	}
	if len(holds) != 1 || holds[0].ID != res.Metadata["hold_id"] || holds[0].Payload != call.Input {
		t.Fatalf("holds = %#v, want queued input with returned hold id", holds)
	}

	var attempts int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM trust_ledger`).Scan(&attempts); err != nil {
		t.Fatalf("count trust ledger: %v", err)
	}
	if attempts != 0 {
		t.Fatalf("trust attempts = %d, want 0 before deferred execution", attempts)
	}
}

func TestInterceptorHoldDisabledExecutesNormally(t *testing.T) {
	store := openActionTestStore(t)
	ic := NewInterceptorWithGateAndHold(store, NewClassifierWithCompensableHTTP(), nil, false, 120*time.Second)
	ctx := context.Background()

	called := false
	call := &tool.ToolCall{ToolName: "http", Input: `{"method":"POST","url":"https://example.com"}`}
	final := func(_ context.Context, _ *tool.ToolCall) (*tool.ToolResult, error) {
		called = true
		return &tool.ToolResult{Output: "executed"}, nil
	}
	res, err := ic.Intercept(ctx, call, final)
	if err != nil {
		t.Fatalf("Intercept() error = %v", err)
	}
	if !called || res.Output != "executed" {
		t.Fatalf("disabled hold path did not execute normally: called=%v res=%#v", called, res)
	}
	holds, err := store.ListPendingHolds(ctx)
	if err != nil {
		t.Fatalf("ListPendingHolds() error = %v", err)
	}
	if len(holds) != 0 {
		t.Fatalf("pending holds = %#v, want none when disabled", holds)
	}
}
