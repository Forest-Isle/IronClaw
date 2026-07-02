package bench

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Forest-Isle/daimon/internal/mind"
)

type benchProviderResponse struct {
	text      string
	toolCalls []mind.ToolUseBlock
	usage     mind.Usage
	err       error
}

type benchStubProvider struct {
	streams  []benchProviderResponse
	complete benchProviderResponse
	requests []mind.CompletionRequest
}

func (p *benchStubProvider) Complete(_ context.Context, req mind.CompletionRequest) (*mind.CompletionResponse, error) {
	p.requests = append(p.requests, req)
	if p.complete.err != nil {
		return nil, p.complete.err
	}
	return &mind.CompletionResponse{
		Text:      p.complete.text,
		ToolCalls: p.complete.toolCalls,
		Usage:     p.complete.usage,
	}, nil
}

func (p *benchStubProvider) Stream(_ context.Context, req mind.CompletionRequest) (mind.StreamIterator, error) {
	p.requests = append(p.requests, req)
	if len(p.streams) == 0 {
		return &benchStubStream{response: benchProviderResponse{text: "done"}}, nil
	}
	resp := p.streams[0]
	p.streams = p.streams[1:]
	if resp.err != nil {
		return nil, resp.err
	}
	return &benchStubStream{response: resp}, nil
}

func (p *benchStubProvider) Capabilities() mind.Caps { return mind.Caps{} }

type benchStubStream struct {
	response benchProviderResponse
	done     bool
}

func (s *benchStubStream) Next() (mind.StreamDelta, error) {
	if s.done {
		return mind.StreamDelta{Done: true}, nil
	}
	s.done = true
	if s.response.err != nil {
		return mind.StreamDelta{}, s.response.err
	}
	return mind.StreamDelta{
		Text:       s.response.text,
		ToolCalls:  s.response.toolCalls,
		Done:       true,
		StopReason: mind.StopToolUse,
		Usage:      s.response.usage,
	}, nil
}

func (s *benchStubStream) Close() {}

func TestRunHappyPathWithStubProvider(t *testing.T) {
	seed := t.TempDir()
	writeTestFile(t, filepath.Join(seed, "config.yaml"), "port: 8080\n")
	provider := &benchStubProvider{streams: []benchProviderResponse{
		{
			text: "writing",
			toolCalls: []mind.ToolUseBlock{
				toolCall(t, "file_write", map[string]string{
					"path":    "settings.yaml",
					"content": "port: 8080\n",
				}),
			},
			usage: mind.Usage{InputTokens: 7, OutputTokens: 3},
		},
		{
			text:      "closing",
			toolCalls: []mind.ToolUseBlock{closeToolCall(t, "wrote settings")},
			usage:     mind.Usage{InputTokens: 5, OutputTokens: 2},
		},
	}}
	cases := []Case{{
		Name:    "rename-config",
		Goal:    "Write settings.yaml preserving the config content.",
		SeedDir: seed,
		Checks: []Check{
			{Type: "file_exists", Path: "settings.yaml"},
			{Type: "content_contains", Path: "settings.yaml", Value: "port: 8080"},
		},
	}}

	results, err := Run(context.Background(), provider, cases, Options{Model: "stub", MaxTokens: 123})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 || !results[0].Passed {
		t.Fatalf("results = %+v, want one passing result", results)
	}
	if results[0].InputTokens != 12 || results[0].OutputTokens != 5 {
		t.Fatalf("tokens = %d+%d, want 12+5", results[0].InputTokens, results[0].OutputTokens)
	}
	if provider.requests[0].Model != "stub" || provider.requests[0].MaxTokens != 123 {
		t.Fatalf("request model/max = %q/%d", provider.requests[0].Model, provider.requests[0].MaxTokens)
	}
}

func TestRunForkIsolation(t *testing.T) {
	relPath := "bench-isolation-file.txt"
	_ = os.Remove(relPath)
	t.Cleanup(func() { _ = os.Remove(relPath) })
	provider := &benchStubProvider{streams: []benchProviderResponse{
		{text: "write", toolCalls: []mind.ToolUseBlock{
			toolCall(t, "file_write", map[string]string{"path": relPath, "content": "inside"}),
		}},
		{text: "close", toolCalls: []mind.ToolUseBlock{closeToolCall(t, "done")}},
	}}
	cases := []Case{{Name: "isolation", Goal: "write a file", Checks: []Check{
		{Type: "file_exists", Path: relPath},
		{Type: "content_equals", Path: relPath, Value: "inside\n"},
	}}}

	results, err := Run(context.Background(), provider, cases, Options{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !results[0].Passed {
		t.Fatalf("result = %+v, want pass", results[0])
	}
	if _, err := os.Stat(relPath); !os.IsNotExist(err) {
		t.Fatalf("relative tool write touched cwd: stat err = %v", err)
	}
}

func TestRunRejectsAbsolutePathEscape(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.txt")
	provider := &benchStubProvider{streams: []benchProviderResponse{
		{text: "escape", toolCalls: []mind.ToolUseBlock{
			toolCall(t, "file_write", map[string]string{"path": outside, "content": "bad"}),
		}},
		{text: "close", toolCalls: []mind.ToolUseBlock{closeToolCall(t, "done")}},
	}}
	cases := []Case{{Name: "escape", Goal: "try absolute path", Checks: []Check{
		{Type: "file_absent", Path: "placeholder"},
	}}}

	results, err := Run(context.Background(), provider, cases, Options{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("absolute escape created file: stat err = %v", err)
	}
	if len(provider.requests) < 2 || !strings.Contains(lastMessage(provider.requests[1]), "escapes working directory") {
		t.Fatalf("second request did not receive escape error: %+v", provider.requests)
	}
}

func TestRunEpisodeErrorDoesNotStopSubsequentCases(t *testing.T) {
	streamErr := errors.New("stream failed")
	provider := &benchStubProvider{streams: []benchProviderResponse{
		{err: streamErr},
		{text: "close", toolCalls: []mind.ToolUseBlock{closeToolCall(t, "ok")}},
	}}
	cases := []Case{
		{Name: "first", Goal: "fail", Checks: []Check{{Type: "file_absent", Path: "x"}}},
		{Name: "second", Goal: "pass", Checks: []Check{{Type: "file_absent", Path: "x"}}},
	}

	results, err := Run(context.Background(), provider, cases, Options{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	if results[0].Err == nil || results[0].Passed {
		t.Fatalf("first result = %+v, want episode error and fail", results[0])
	}
	if results[1].Err != nil || !results[1].Passed {
		t.Fatalf("second result = %+v, want pass", results[1])
	}
}

func TestCopyTreeCopiesNestedFilesAndRejectsSymlink(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	writeTestFile(t, filepath.Join(src, "nested", "file.txt"), "content\n")
	if err := copyTree(src, dst); err != nil {
		t.Fatalf("copyTree() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dst, "nested", "file.txt"))
	if err != nil {
		t.Fatalf("read copied file: %v", err)
	}
	if string(data) != "content\n" {
		t.Fatalf("copied content = %q", data)
	}

	linkSrc := t.TempDir()
	if err := os.Symlink(filepath.Join(src, "nested", "file.txt"), filepath.Join(linkSrc, "link")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	if err := copyTree(linkSrc, t.TempDir()); err == nil {
		t.Fatal("copyTree() error = nil, want symlink refusal")
	}
}

func TestRunSeedSymlinkReturnsError(t *testing.T) {
	seed := t.TempDir()
	target := filepath.Join(seed, "target.txt")
	writeTestFile(t, target, "content\n")
	if err := os.Symlink(target, filepath.Join(seed, "link")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	provider := &benchStubProvider{}
	cases := []Case{{Name: "symlink", Goal: "fail setup", SeedDir: seed, Checks: []Check{
		{Type: "file_absent", Path: "x"},
	}}}

	_, err := Run(context.Background(), provider, cases, Options{})
	if err == nil {
		t.Fatal("Run() error = nil, want seed symlink error")
	}
	if !strings.Contains(err.Error(), "seed symlink refused") {
		t.Fatalf("error = %q, want symlink refusal", err)
	}
}

func toolCall(t *testing.T, name string, input any) mind.ToolUseBlock {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return mind.ToolUseBlock{ID: "call_" + name, Name: name, Input: string(data)}
}

func closeToolCall(t *testing.T, summary string) mind.ToolUseBlock {
	t.Helper()
	input := map[string]string{"status": "done", "summary": summary}
	return toolCall(t, "episode_close", input)
}

func lastMessage(req mind.CompletionRequest) string {
	if len(req.Messages) == 0 {
		return ""
	}
	return req.Messages[len(req.Messages)-1].Content
}
