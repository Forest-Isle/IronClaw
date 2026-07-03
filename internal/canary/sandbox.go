package canary

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Forest-Isle/daimon/internal/agent"
	"github.com/Forest-Isle/daimon/internal/mind"
	"github.com/Forest-Isle/daimon/internal/store"
	"github.com/Forest-Isle/daimon/internal/tool"
	"github.com/Forest-Isle/daimon/internal/world"
)

// Sandbox is a disposable execution environment for one canary trial: real
// tools, real LLM decisions, but every side effect is contained: file writes are
// fenced to Root, network tools are absent, and the world journal lands in a
// throwaway database.
type Sandbox struct {
	// Root is the throwaway working directory; file tools are fenced here.
	Root string
	// World is the throwaway world store; journal writes never reach the real world.
	World *world.Store
	// Registry contains only the canary-safe tool whitelist.
	Registry *tool.Registry

	mu     sync.Mutex
	db     *store.DB
	closed bool
}

// NewSandbox creates a disposable canary environment with a fenced filesystem
// root, throwaway world database, and fail-closed tool whitelist.
func NewSandbox() (*Sandbox, error) {
	root, err := os.MkdirTemp("", "daimon-canary-*")
	if err != nil {
		return nil, fmt.Errorf("canary: create sandbox root: %w", err)
	}

	db, err := store.Open(filepath.Join(root, "world.db"))
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("canary: open sandbox world: %w", err)
	}

	registry := tool.NewRegistry()
	registry.Register(tool.NewFileReadTool())
	registry.Register(tool.NewFileWriteTool(false))
	registry.Register(tool.NewFileEditTool(false))
	registry.Register(tool.NewFileListTool())

	seatbelt := tool.NewSeatbeltShellBackend()
	if seatbelt.Available() {
		registry.Register(tool.NewBashToolWithBackend(30*time.Second, false, tool.NewPolicy(nil), seatbelt))
	} else {
		slog.Warn("canary: seatbelt shell backend unavailable; bash not registered")
	}

	return &Sandbox{
		Root:     root,
		World:    world.NewStore(db.DB),
		Registry: registry,
		db:       db,
	}, nil
}

// Close releases the throwaway database and removes the sandbox root. It is
// idempotent.
func (s *Sandbox) Close() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	db := s.db
	root := s.Root
	s.db = nil
	s.mu.Unlock()

	var errs []error
	if db != nil {
		if err := db.Close(); err != nil {
			errs = append(errs, fmt.Errorf("canary: close sandbox world: %w", err))
		}
	}
	if root != "" {
		if err := os.RemoveAll(root); err != nil {
			errs = append(errs, fmt.Errorf("canary: remove sandbox root: %w", err))
		}
	}
	return errors.Join(errs...)
}

// Seed writes initial files into the sandbox root after applying the same
// working-directory fence used by canary tool execution.
func (s *Sandbox) Seed(files map[string]string) error {
	if s == nil {
		return fmt.Errorf("canary: seed: nil sandbox")
	}
	ctx := tool.WithWorkDir(context.Background(), s.Root)
	for rel, content := range files {
		path, err := tool.ResolveWorkPath(ctx, rel)
		if err != nil {
			return fmt.Errorf("canary: seed path %q: %w", rel, err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("canary: seed path %q: mkdir: %w", rel, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return fmt.Errorf("canary: seed path %q: write: %w", rel, err)
		}
	}
	return nil
}

// ToolDefs returns the sandbox registry as LLM tool definitions.
func (s *Sandbox) ToolDefs() []mind.ToolDefinition {
	if s == nil || s.Registry == nil {
		return nil
	}
	tools := s.Registry.All()
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name() < tools[j].Name() })

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

// Invoke returns a fenced bare-tool invocation function for the episode kernel.
func (s *Sandbox) Invoke() agent.ToolInvokeFunc {
	return func(ctx context.Context, _ int, call mind.ToolUseBlock) (string, bool) {
		if s == nil || s.Registry == nil {
			return "sandbox unavailable", true
		}
		ctx = tool.WithWorkDir(ctx, s.Root)

		t, err := s.Registry.Get(call.Name)
		if err != nil {
			return "unknown tool: " + call.Name, true
		}
		result, err := t.Execute(ctx, []byte(call.Input))
		if err != nil {
			return err.Error(), true
		}
		switch {
		case result.Error != "" && result.Output != "":
			return result.Output + "\n" + result.Error, true
		case result.Error != "":
			return result.Error, true
		default:
			return result.Output, false
		}
	}
}
