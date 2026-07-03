package canary

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Forest-Isle/daimon/internal/agent"
	"github.com/Forest-Isle/daimon/internal/episode"
	"github.com/Forest-Isle/daimon/internal/mind"
	"github.com/Forest-Isle/daimon/internal/world"
)

type scriptedResponse struct {
	text      string
	toolCalls []mind.ToolUseBlock
	usage     mind.Usage
	err       error
}

type scriptedProvider struct {
	streams  []scriptedResponse
	complete scriptedResponse
	requests []mind.CompletionRequest
}

func (p *scriptedProvider) Complete(_ context.Context, req mind.CompletionRequest) (*mind.CompletionResponse, error) {
	p.requests = append(p.requests, req)
	if p.complete.err != nil {
		return nil, p.complete.err
	}
	return &mind.CompletionResponse{
		Text:       p.complete.text,
		ToolCalls:  p.complete.toolCalls,
		Usage:      p.complete.usage,
		StopReason: mind.StopEndTurn,
	}, nil
}

func (p *scriptedProvider) Stream(_ context.Context, req mind.CompletionRequest) (mind.StreamIterator, error) {
	p.requests = append(p.requests, req)
	if len(p.streams) == 0 {
		return &scriptedStream{response: scriptedResponse{text: "done"}}, nil
	}
	resp := p.streams[0]
	p.streams = p.streams[1:]
	if resp.err != nil {
		return nil, resp.err
	}
	return &scriptedStream{response: resp}, nil
}

func (p *scriptedProvider) Capabilities() mind.Caps { return mind.Caps{} }

type scriptedStream struct {
	response scriptedResponse
	done     bool
}

func (s *scriptedStream) Next() (mind.StreamDelta, error) {
	if s.done {
		return mind.StreamDelta{Done: true, StopReason: mind.StopEndTurn}, nil
	}
	s.done = true
	if s.response.err != nil {
		return mind.StreamDelta{}, s.response.err
	}
	stop := mind.StopEndTurn
	if len(s.response.toolCalls) > 0 {
		stop = mind.StopToolUse
	}
	return mind.StreamDelta{
		Text:       s.response.text,
		ToolCalls:  s.response.toolCalls,
		Done:       true,
		StopReason: stop,
		Usage:      s.response.usage,
	}, nil
}

func (s *scriptedStream) Close() {}

func TestRunnerRunTaskEndToEndPass(t *testing.T) {
	provider := &scriptedProvider{streams: []scriptedResponse{
		{toolCalls: []mind.ToolUseBlock{toolCall("file_write", `{"path":"out.txt","content":"hello"}`)}},
		{text: "Wrote the file.", toolCalls: []mind.ToolUseBlock{closeCall(`{"status":"done","summary":"wrote out.txt"}`)}},
	}}
	runner := Runner{
		Provider:     provider,
		Model:        "test-model",
		ProviderName: "test-provider",
		MaxTokens:    100,
		Trials:       1,
	}

	result, err := runner.RunTask(context.Background(), Task{
		Name: "write-output",
		Goal: "Write hello to out.txt.",
		Checks: []Check{
			{Kind: "file_exists", Path: "out.txt"},
			{Kind: "file_contains", Path: "out.txt", Contains: "hello"},
			{Kind: "outcome_status", Status: "done"},
		},
	})
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if result.Trials != 1 || result.Passes != 1 || result.PassRate != 1.0 {
		t.Fatalf("result = %+v, want one passing trial", result)
	}
	if len(result.Results) != 1 || !result.Results[0].Passed {
		t.Fatalf("trial = %+v, want passed", result.Results)
	}
}

func TestSandboxFenceRejectsEscapingWrite(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	provider := &scriptedProvider{streams: []scriptedResponse{
		{toolCalls: []mind.ToolUseBlock{toolCall("file_write", `{"path":"../escape.txt","content":"owned"}`)}},
		{text: "Closing.", toolCalls: []mind.ToolUseBlock{closeCall(`{"status":"done","summary":"saw fenced write fail"}`)}},
	}}
	sb := newTestSandbox(t)
	root := sb.Root
	parent := filepath.Dir(root)
	escapePath := filepath.Join(parent, "escape.txt")

	kernel := episode.NewRunner(provider, sb.World, &world.Identity{Dir: sb.Root}, nil)
	outcome, err := kernel.Execute(context.Background(), agent.CognitiveRequest{
		Goal:          "Try to write outside the sandbox.",
		Trigger:       "canary:escape",
		ActivityClass: "canary",
		ToolDefs:      sb.ToolDefs(),
		Invoke:        sb.Invoke(),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outcome.Status != "done" {
		t.Fatalf("outcome status = %q, want done", outcome.Status)
	}
	if _, err := os.Stat(escapePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("escape path stat err = %v, want not exist at %s", err, escapePath)
	}
	if !providerSawToolResult(provider, "escapes working directory") {
		t.Fatalf("provider requests did not include fenced write error: %#v", provider.requests)
	}
	journal, err := sb.World.ListJournal(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("ListJournal: %v", err)
	}
	if len(journal) != 1 || journal[0].Detail != "tool_failures=1" {
		t.Fatalf("journal = %#v, want one outcome with tool_failures=1", journal)
	}
}

func TestCheckFailureHasDetail(t *testing.T) {
	outcome := agent.CognitiveOutcome{Status: "done"}
	results := evaluateChecks(t.TempDir(), outcome, []Check{
		{Kind: "file_exists", Path: "missing.txt"},
	})
	if len(results) != 1 {
		t.Fatalf("checks = %d, want 1", len(results))
	}
	if results[0].Passed {
		t.Fatalf("check passed unexpectedly: %+v", results[0])
	}
	if results[0].Detail == "" {
		t.Fatalf("check detail is empty")
	}
}

func TestSandboxWorldIsDisposable(t *testing.T) {
	provider := &scriptedProvider{streams: []scriptedResponse{
		{text: "Done.", toolCalls: []mind.ToolUseBlock{closeCall(`{"status":"done","summary":"world isolated"}`)}},
	}}
	sb := newTestSandbox(t)
	root := sb.Root

	kernel := episode.NewRunner(provider, sb.World, &world.Identity{Dir: sb.Root}, nil)
	if _, err := kernel.Execute(context.Background(), agent.CognitiveRequest{
		Goal:          "Close cleanly.",
		Trigger:       "canary:world",
		ActivityClass: "canary",
		ToolDefs:      sb.ToolDefs(),
		Invoke:        sb.Invoke(),
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	journal, err := sb.World.ListJournal(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("ListJournal: %v", err)
	}
	if len(journal) != 1 || journal[0].Kind != "outcome" || journal[0].Summary != "world isolated" {
		t.Fatalf("journal = %#v, want isolated outcome row", journal)
	}
	if err := sb.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sandbox root stat err = %v, want removed", err)
	}
}

func TestUnknownToolReturnsErrorAndEpisodeContinues(t *testing.T) {
	provider := &scriptedProvider{streams: []scriptedResponse{
		{toolCalls: []mind.ToolUseBlock{toolCall("missing_tool", `{}`)}},
		{text: "Recovered.", toolCalls: []mind.ToolUseBlock{closeCall(`{"status":"done","summary":"recovered after missing tool"}`)}},
	}}
	sb := newTestSandbox(t)

	kernel := episode.NewRunner(provider, sb.World, &world.Identity{Dir: sb.Root}, nil)
	outcome, err := kernel.Execute(context.Background(), agent.CognitiveRequest{
		Goal:          "Call a missing tool, then close.",
		Trigger:       "canary:unknown",
		ActivityClass: "canary",
		ToolDefs:      sb.ToolDefs(),
		Invoke:        sb.Invoke(),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outcome.Status != "done" {
		t.Fatalf("outcome status = %q, want done", outcome.Status)
	}
	if !providerSawToolResult(provider, "unknown tool: missing_tool") {
		t.Fatalf("provider requests did not include unknown-tool result: %#v", provider.requests)
	}
	journal, err := sb.World.ListJournal(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("ListJournal: %v", err)
	}
	if len(journal) != 1 || journal[0].Detail != "tool_failures=1" {
		t.Fatalf("journal = %#v, want one outcome with tool_failures=1", journal)
	}
}

func TestUnknownCheckKindFailsClosed(t *testing.T) {
	results := evaluateChecks(t.TempDir(), agent.CognitiveOutcome{Status: "done"}, []Check{
		{Kind: "bogus"},
	})
	if len(results) != 1 {
		t.Fatalf("checks = %d, want 1", len(results))
	}
	if results[0].Passed {
		t.Fatalf("unknown check kind passed: %+v", results[0])
	}
	if !strings.Contains(results[0].Detail, "unknown check kind") {
		t.Fatalf("detail = %q, want unknown-kind explanation", results[0].Detail)
	}
}

func TestToolDefsExcludeNetworkTools(t *testing.T) {
	sb := newTestSandbox(t)
	names := map[string]bool{}
	for _, def := range sb.ToolDefs() {
		names[def.Name] = true
	}
	if names["http"] {
		t.Fatalf("ToolDefs included http: %#v", names)
	}
	if names["send_email"] {
		t.Fatalf("ToolDefs included send_email: %#v", names)
	}
}

func newTestSandbox(t *testing.T) *Sandbox {
	t.Helper()
	sb, err := NewSandbox()
	if err != nil {
		t.Fatalf("NewSandbox: %v", err)
	}
	t.Cleanup(func() {
		if err := sb.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	return sb
}

func toolCall(name, input string) mind.ToolUseBlock {
	return mind.ToolUseBlock{ID: "tool_" + name, Name: name, Input: input}
}

func closeCall(input string) mind.ToolUseBlock {
	return mind.ToolUseBlock{ID: "close_1", Name: "episode_close", Input: input}
}

func providerSawToolResult(provider *scriptedProvider, text string) bool {
	for _, req := range provider.requests {
		for _, msg := range req.Messages {
			if msg.ToolUseID != "" && strings.Contains(msg.Content, text) {
				return true
			}
		}
	}
	return false
}
