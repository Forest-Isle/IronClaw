package evals

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Forest-Isle/daimon/internal/agent"
	"github.com/Forest-Isle/daimon/internal/channel"
	"github.com/Forest-Isle/daimon/internal/channel/scheduler"
	"github.com/Forest-Isle/daimon/internal/config"
	"github.com/Forest-Isle/daimon/internal/mind"
	"github.com/Forest-Isle/daimon/internal/session"
	"github.com/Forest-Isle/daimon/internal/store"
	"github.com/Forest-Isle/daimon/internal/taskruntime"
	"github.com/Forest-Isle/daimon/internal/tool"
	"github.com/Forest-Isle/daimon/internal/workflow"
)

func TestEvalGateDoesNotUseUserDir(t *testing.T) {
	root := repoRoot(t)
	home := t.TempDir()
	cmd := exec.Command("make", "eval-gate")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"GOCACHE="+goEnv(t, "GOCACHE"),
		"GOMODCACHE="+goEnv(t, "GOMODCACHE"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make eval-gate: %v\n%s", err, out)
	}
	wantCorpus := filepath.Join(root, "evals", "fixtures", "replays")
	if !strings.Contains(string(out), "corpus "+wantCorpus) {
		t.Fatalf("eval gate did not use fixture corpus %q:\n%s", wantCorpus, out)
	}
	if _, err := os.Stat(filepath.Join(home, ".daimon")); !os.IsNotExist(err) {
		t.Fatalf("eval gate touched user dir: %v", err)
	}
}

func TestEval_MultiStepEditRequiresReadBeforeEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	interceptor := tool.NewReadBeforeEditInterceptor(nil)
	ctx := tool.WithWorkDir(tool.WithSessionID(context.Background(), "sess_edit"), dir)

	editInput := `{"path":"target.txt","old_string":"before","new_string":"after"}`
	editCall := &tool.ToolCall{ToolName: "file_edit", Input: editInput, SessionID: "sess_edit"}
	denied, err := interceptor.Intercept(ctx, editCall, evalToolFinal)
	if err != nil {
		t.Fatalf("edit before read error: %v", err)
	}
	if denied.Error == "" || !strings.Contains(denied.Error, "read-before-edit required") {
		t.Fatalf("expected read-before-edit denial, got %#v", denied)
	}

	readCall := &tool.ToolCall{ToolName: "file_read", Input: `{"path":"target.txt"}`, SessionID: "sess_edit"}
	if res, err := interceptor.Intercept(ctx, readCall, evalToolFinal); err != nil || res.Error != "" {
		t.Fatalf("read failed: res=%#v err=%v", res, err)
	}
	edited, err := interceptor.Intercept(ctx, editCall, evalToolFinal)
	if err != nil || edited.Error != "" {
		t.Fatalf("edit after read failed: res=%#v err=%v", edited, err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "after\n" {
		t.Fatalf("file content = %q", data)
	}
}

func TestEval_DeniedToolPath(t *testing.T) {
	engine := tool.NewPermissionEngine([]tool.PermissionRule{
		{Tool: "bash", Pattern: "rm *", Action: string(tool.PermissionDeny)},
	}, string(tool.PermissionNone), nil)
	interceptor := tool.NewPermissionInterceptor(engine)
	res, err := interceptor.Intercept(context.Background(), &tool.ToolCall{
		ToolName: "bash",
		Input:    `{"command":"rm file"}`,
	}, func(context.Context, *tool.ToolCall) (*tool.ToolResult, error) {
		return &tool.ToolResult{Output: "should not run"}, nil
	})
	if err != nil {
		t.Fatalf("permission intercept error: %v", err)
	}
	if res.Error == "" || !strings.Contains(res.Error, "denied") {
		t.Fatalf("expected deny result, got %#v", res)
	}
}

func TestEval_CompactResumeCheckpointPath(t *testing.T) {
	db := evalDB(t, "resume.db")
	ledger := taskruntime.NewLedger(db.DB)
	if err := ledger.SaveCheckpoint(context.Background(), taskruntime.Checkpoint{
		SessionID:    "sess_resume",
		SubtaskIndex: 2,
		Observations: []string{
			"read target files",
			"tests passed",
		},
		PlanJSON: `{"goal":"resume safely"}`,
	}); err != nil {
		t.Fatalf("SaveCheckpoint() error = %v", err)
	}
	cp, err := ledger.GetCheckpoint(context.Background(), "sess_resume")
	if err != nil {
		t.Fatalf("GetCheckpoint() error = %v", err)
	}
	if cp.PlanJSON == "" || len(cp.Observations) != 2 {
		t.Fatalf("checkpoint = %#v", cp)
	}
}

func TestEval_SubAgentStructuredOutputPath(t *testing.T) {
	db := evalDB(t, "subagent.db")
	sessions := session.NewManager(db)
	tools := tool.NewRegistry()
	provider := &evalProvider{response: "```json\n{\"status\":\"success\",\"summary\":\"Subagent done\",\"artifacts\":[\"result.txt\"]}\n```"}
	deps := agent.AgentDeps{
		Core: agent.CoreDeps{
			Provider: provider,
			Sessions: sessions,
			DB:       db,
			Tools:    tools,
			Cfg:      config.AgentConfig{MaxIterations: 2},
			LLMCfg:   config.LLMConfig{Model: "eval-model", MaxTokens: 200},
		},
	}.WithDefaults()
	mgr := agent.NewSubAgentManager(deps)
	result, err := mgr.Spawn(context.Background(), agent.SpawnRequest{
		Spec: &agent.AgentSpec{Name: "eval", Description: "eval", MaxIterations: 2},
		Task: "return structured output",
	})
	if err != nil {
		t.Fatalf("Spawn() error = %v", err)
	}
	if result.Status != agent.StatusSuccess || result.Summary != "Subagent done" || len(result.Artifacts) != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestEval_SchedulerLedgerPath(t *testing.T) {
	db := evalDB(t, "scheduler.db")
	ledger := taskruntime.NewLedger(db.DB)
	sc := scheduler.New(db, &evalNotifier{}, ledger)
	ctx := context.Background()
	task, err := sc.AddTask(ctx, "run scheduled eval", "@daily", "tui", "chat")
	if err != nil {
		t.Fatalf("AddTask() error = %v", err)
	}
	entry, err := ledger.Get(ctx, taskruntime.ScheduledLedgerID(task.ID))
	if err != nil {
		t.Fatalf("ledger.Get() error = %v", err)
	}
	if entry.Kind != "scheduled" || entry.Metadata.ScheduledTaskID != task.ID {
		t.Fatalf("entry = %#v", entry)
	}
	sc.FinishRun(ctx, task.ID, nil, "ok")
	entry, _ = ledger.Get(ctx, taskruntime.ScheduledLedgerID(task.ID))
	if entry.State != taskruntime.StateSucceeded {
		t.Fatalf("state = %s", entry.State)
	}
}

func TestEval_WorkflowReplayPath(t *testing.T) {
	spec, err := workflow.ParseSpec([]byte(`
name: eval-workflow
stages:
  - id: one
    steps:
      - id: a
        type: agent
        agent: worker
        task: first
      - id: b
        type: agent
        agent: worker
        task: second
`))
	if err != nil {
		t.Fatalf("ParseSpec() error = %v", err)
	}
	cache := workflow.NewMemoryCache()
	runner := &evalWorkflowRunner{}
	executor := workflow.Executor{Runner: runner, Cache: cache}
	if _, err := executor.Execute(context.Background(), spec); err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	if runner.calls != 2 {
		t.Fatalf("first calls = %d", runner.calls)
	}
	replayRunner := &evalWorkflowRunner{}
	replayExecutor := workflow.Executor{Runner: replayRunner, Cache: cache}
	run, err := replayExecutor.Execute(context.Background(), spec)
	if err != nil {
		t.Fatalf("replay Execute() error = %v", err)
	}
	if replayRunner.calls != 0 || run.Budget.CacheHits != 2 {
		t.Fatalf("replay calls=%d budget=%#v", replayRunner.calls, run.Budget)
	}
}

func evalToolFinal(ctx context.Context, call *tool.ToolCall) (*tool.ToolResult, error) {
	var res tool.Result
	var err error
	switch call.ToolName {
	case "file_read":
		res, err = tool.NewFileReadTool().Execute(ctx, []byte(call.Input))
	case "file_edit":
		res, err = tool.NewFileEditTool(false).Execute(ctx, []byte(call.Input))
	default:
		return &tool.ToolResult{Error: "unknown tool"}, nil
	}
	if err != nil {
		return nil, err
	}
	return &tool.ToolResult{Output: res.Output, Error: res.Error}, nil
}

func evalDB(t *testing.T, name string) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func goEnv(t *testing.T, key string) string {
	t.Helper()
	out, err := exec.Command("go", "env", key).Output()
	if err != nil {
		t.Fatalf("go env %s: %v", key, err)
	}
	return strings.TrimSpace(string(out))
}

type evalProvider struct {
	response string
}

func (p *evalProvider) Complete(context.Context, mind.CompletionRequest) (*mind.CompletionResponse, error) {
	return &mind.CompletionResponse{Text: p.response}, nil
}

func (p *evalProvider) Capabilities() mind.Caps { return mind.Caps{} }

func (p *evalProvider) Stream(context.Context, mind.CompletionRequest) (mind.StreamIterator, error) {
	return &evalStream{text: p.response}, nil
}

type evalStream struct {
	text string
	done bool
}

func (s *evalStream) Next() (mind.StreamDelta, error) {
	if s.done {
		return mind.StreamDelta{Done: true, StopReason: mind.StopEndTurn}, nil
	}
	s.done = true
	return mind.StreamDelta{Text: s.text, Done: true, StopReason: mind.StopEndTurn}, nil
}

func (s *evalStream) Close() {}

type evalNotifier struct {
	mu sync.Mutex
}

func (n *evalNotifier) Name() string { return "eval" }
func (n *evalNotifier) Start(context.Context, channel.InboundHandler) error {
	return nil
}
func (n *evalNotifier) Stop(context.Context) error { return nil }
func (n *evalNotifier) Send(context.Context, channel.OutboundMessage) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return nil
}
func (n *evalNotifier) SendStreaming(context.Context, channel.MessageTarget) (channel.StreamUpdater, error) {
	return nil, nil
}

type evalWorkflowRunner struct {
	calls int
}

func (r *evalWorkflowRunner) RunStep(_ context.Context, step workflow.Step, input workflow.StepInput) (workflow.StepOutput, error) {
	r.calls++
	return workflow.StepOutput{
		Status:  workflow.StatusSuccess,
		Summary: fmt.Sprintf("%s after %d", step.ID, len(input.PriorResults)),
		Output:  "ok",
	}, nil
}
