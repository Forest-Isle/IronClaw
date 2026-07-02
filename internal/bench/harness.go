package bench

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/Forest-Isle/daimon/internal/agent"
	"github.com/Forest-Isle/daimon/internal/episode"
	"github.com/Forest-Isle/daimon/internal/mind"
	"github.com/Forest-Isle/daimon/internal/store"
	"github.com/Forest-Isle/daimon/internal/tool"
	"github.com/Forest-Isle/daimon/internal/world"
)

// Options controls behavior bench execution.
type Options struct {
	Model     string
	MaxTokens int
}

// CaseResult is the result of running one behavior bench case.
type CaseResult struct {
	Name         string
	Passed       bool
	Checks       []CheckResult
	Status       string
	InputTokens  int64
	OutputTokens int64
	Err          error
}

// Run executes cases sequentially in isolated fork directories.
func Run(ctx context.Context, provider mind.Provider, cases []Case, opts Options) ([]CaseResult, error) {
	if provider == nil {
		return nil, fmt.Errorf("behavior bench provider unavailable")
	}
	results := make([]CaseResult, 0, len(cases))
	for _, c := range cases {
		result, err := runCase(ctx, provider, c, opts)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func runCase(ctx context.Context, provider mind.Provider, c Case, opts Options) (CaseResult, error) {
	forkDir, err := os.MkdirTemp("", "daimon-bench-fork-")
	if err != nil {
		return CaseResult{}, fmt.Errorf("create fork for case %s: %w", c.Name, err)
	}
	defer func() { _ = os.RemoveAll(forkDir) }()
	if c.SeedDir != "" {
		if err := copyTree(c.SeedDir, forkDir); err != nil {
			return CaseResult{}, fmt.Errorf("copy seed for case %s: %w", c.Name, err)
		}
	}

	ws, identity, cleanup, err := newBenchWorld(c.Name)
	if err != nil {
		return CaseResult{}, err
	}
	defer cleanup()

	counting := &usageCountingProvider{Provider: provider}
	registry := fileToolRegistry()
	outcome, execErr := runEpisode(ctx, counting, ws, identity, registry, forkDir, c, opts)
	checks := evaluateChecks(forkDir, c.Checks)
	return CaseResult{
		Name:         c.Name,
		Passed:       execErr == nil && outcome.Status != "failed" && allChecksPassed(checks),
		Checks:       checks,
		Status:       outcome.Status,
		InputTokens:  counting.in.Load(),
		OutputTokens: counting.out.Load(),
		Err:          execErr,
	}, nil
}

func runEpisode(ctx context.Context, provider mind.Provider, ws *world.Store, identity *world.Identity, registry *tool.Registry, forkDir string, c Case, opts Options) (agent.CognitiveOutcome, error) {
	runner := episode.NewRunner(provider, ws, identity, nil)
	return runner.Execute(ctx, agent.CognitiveRequest{
		SessionID:     "bench-" + c.Name,
		Goal:          c.Goal,
		Trigger:       "bench",
		Model:         opts.Model,
		MaxTokens:     opts.MaxTokens,
		ActivityClass: "bench",
		ToolDefs:      buildToolDefs(registry),
		Invoke: func(ctx context.Context, _ int, call mind.ToolUseBlock) (string, bool) {
			return invokeFileTool(ctx, registry, forkDir, call)
		},
	})
}

func newBenchWorld(caseName string) (*world.Store, *world.Identity, func(), error) {
	dir, err := os.MkdirTemp("", "daimon-bench-world-")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create world temp dir for case %s: %w", caseName, err)
	}
	path := filepath.Join(dir, "bench.db")
	db, err := store.Open(path)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, nil, nil, fmt.Errorf("open world database for case %s: %w", caseName, err)
	}
	identity := &world.Identity{Dir: filepath.Join(dir, "identity")}
	if err := identity.EnsureDir(); err != nil {
		_ = db.Close()
		_ = os.RemoveAll(dir)
		return nil, nil, nil, fmt.Errorf("ensure identity dir for case %s: %w", caseName, err)
	}
	cleanup := func() {
		_ = db.Close()
		removeSQLiteFiles(path)
		_ = os.RemoveAll(dir)
	}
	return world.NewStore(db.DB), identity, cleanup, nil
}

func invokeFileTool(ctx context.Context, registry *tool.Registry, forkDir string, call mind.ToolUseBlock) (string, bool) {
	ctx = tool.WithWorkDir(ctx, forkDir)
	t, err := registry.Get(call.Name)
	if err != nil {
		return err.Error(), true
	}
	res, err := t.Execute(ctx, []byte(call.Input))
	if err != nil {
		return err.Error(), true
	}
	if res.Error != "" {
		return res.Error, true
	}
	return res.Output, false
}

func fileToolRegistry() *tool.Registry {
	registry := tool.NewRegistry()
	registry.Register(tool.NewFileReadTool())
	registry.Register(tool.NewFileWriteTool(false))
	registry.Register(tool.NewFileEditTool(false))
	registry.Register(tool.NewFileListTool())
	return registry
}

func buildToolDefs(registry *tool.Registry) []mind.ToolDefinition {
	tools := registry.All()
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

func allChecksPassed(checks []CheckResult) bool {
	for _, check := range checks {
		if !check.Passed {
			return false
		}
	}
	return true
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk %s: %w", path, err)
		}
		return copyTreeEntry(src, dst, path, entry)
	})
}

func copyTreeEntry(src, dst, path string, entry os.DirEntry) error {
	if entry.Type()&os.ModeSymlink != 0 {
		return fmt.Errorf("seed symlink refused: %s", path)
	}
	rel, err := filepath.Rel(src, path)
	if err != nil {
		return fmt.Errorf("resolve seed path %s: %w", path, err)
	}
	target := filepath.Join(dst, rel)
	info, err := entry.Info()
	if err != nil {
		return fmt.Errorf("stat seed path %s: %w", path, err)
	}
	if entry.IsDir() {
		if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
			return fmt.Errorf("create seed target dir %s: %w", target, err)
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("seed path is not a regular file: %s", path)
	}
	return copyFile(path, target, info.Mode().Perm())
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open seed file %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create seed target dir: %w", err)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("create seed target %s: %w", dst, err)
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy seed file %s: %w", src, err)
	}
	return nil
}

func removeSQLiteFiles(path string) {
	_ = os.Remove(path)
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
}

type usageCountingProvider struct {
	mind.Provider
	in  atomic.Int64
	out atomic.Int64
}

func (p *usageCountingProvider) Complete(ctx context.Context, req mind.CompletionRequest) (*mind.CompletionResponse, error) {
	resp, err := p.Provider.Complete(ctx, req)
	if resp != nil {
		p.add(resp.Usage)
	}
	return resp, err
}

func (p *usageCountingProvider) Stream(ctx context.Context, req mind.CompletionRequest) (mind.StreamIterator, error) {
	stream, err := p.Provider.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	return &usageCountingStream{inner: stream, provider: p}, nil
}

func (p *usageCountingProvider) add(usage mind.Usage) {
	p.in.Add(int64(usage.InputTokens))
	p.out.Add(int64(usage.OutputTokens))
}

type usageCountingStream struct {
	inner    mind.StreamIterator
	provider *usageCountingProvider
}

func (s *usageCountingStream) Next() (mind.StreamDelta, error) {
	delta, err := s.inner.Next()
	if err == nil {
		s.provider.add(delta.Usage)
	}
	return delta, err
}

func (s *usageCountingStream) Close() {
	s.inner.Close()
}
