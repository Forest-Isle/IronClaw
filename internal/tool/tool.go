package tool

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// ResultType indicates the semantic type of tool output.
type ResultType string

const (
	ResultText      ResultType = "text"
	ResultImage     ResultType = "image"
	ResultFile      ResultType = "file"
	ResultReference ResultType = "reference"
)

// Result is the output of a tool execution.
type Result struct {
	Output    string         `json:"output"`
	Error     string         `json:"error,omitempty"`
	Type      ResultType     `json:"type,omitempty"`       // defaults to "text"
	FilePath  string         `json:"file_path,omitempty"`  // associated file path
	IsPartial bool           `json:"is_partial,omitempty"` // true if output was truncated
	Metadata  map[string]any `json:"metadata,omitempty"`   // extensible key-value pairs
}

// Tool is the interface all tools must implement.
type Tool interface {
	Name() string
	Description() string
	InputSchema() map[string]any
	Execute(ctx context.Context, input []byte) (Result, error)
	RequiresApproval() bool
}

// ReadOnlyTool is an optional interface that tools can implement to indicate
// they only read data and have no side effects. Tools implementing this interface
// with IsReadOnly() returning true are eligible for concurrent execution.
type ReadOnlyTool interface {
	IsReadOnly() bool
}

// ParallelSafety defines how a tool behaves in concurrent execution scenarios.
type ParallelSafety string

const (
	// ParallelNever indicates the tool must execute sequentially (e.g., user interaction).
	ParallelNever ParallelSafety = "never"
	// ParallelSafe indicates the tool is safe to run concurrently with any other safe tool.
	ParallelSafe ParallelSafety = "safe"
	// ParallelPathScoped indicates the tool can run concurrently unless it shares
	// a resource path with another tool in the same batch. Requires PathScopedTool.
	ParallelPathScoped ParallelSafety = "path_scoped"
)

// PathScopedTool is an optional interface for tools that need path-based deduplication
// in concurrent execution. Tools that implement this interface with ParallelPathScoped
// safety can run concurrently as long as they operate on different resource paths.
// Write-oriented file tools (file_write, file_edit) implement this to prevent concurrent
// writes to the same file while allowing parallel writes to different files.
type PathScopedTool interface {
	// ExtractPaths returns all filesystem or resource paths that this tool call accesses.
	ExtractPaths(input []byte) ([]string, error)
}

// CanonicalizePath returns a clean, absolute path for conflict detection.
// Returns the cleaned path as-is if Abs fails (e.g., no working directory).
func CanonicalizePath(p string) string {
	if p == "" {
		return ""
	}
	cleaned := filepath.Clean(p)
	abs, err := filepath.Abs(cleaned)
	if err != nil {
		return cleaned
	}
	return abs
}

// ToolCapabilities describes a tool's behavioral characteristics.
type ToolCapabilities struct {
	IsReadOnly      bool           // tool only reads, no side effects
	IsDestructive   bool           // tool may cause irreversible changes
	RequiresNetwork bool           // tool needs network access
	ApprovalMode    string         // "never", "always", "auto" (default: "auto")
	ParallelSafety  ParallelSafety // "never", "safe", or "path_scoped" (default: inferred)
	// Reversibility is the tool's self-declared action class: "reversible",
	// "compensable", or "irreversible". Empty means undeclared; the action
	// classifier treats an undeclared mutating tool as irreversible (fail-closed).
	Reversibility string
}

// CapableTool is an optional interface for tools to declare rich capabilities.
// It subsumes ReadOnlyTool — if a tool implements CapableTool, IsReadOnly()
// is derived from Capabilities().IsReadOnly.
type CapableTool interface {
	Capabilities() ToolCapabilities
}

// GetCapabilities returns a tool's capabilities, using safe defaults
// if the tool doesn't implement CapableTool.
func GetCapabilities(t Tool) ToolCapabilities {
	if ct, ok := t.(CapableTool); ok {
		caps := ct.Capabilities()
		// Infer parallel safety from read-only status if not explicitly set
		if caps.ParallelSafety == "" {
			if caps.IsReadOnly {
				caps.ParallelSafety = ParallelSafe
			} else {
				caps.ParallelSafety = ParallelNever
			}
		}
		return caps
	}
	// Fallback: check ReadOnlyTool for backward compatibility
	readOnly := false
	if ro, ok := t.(ReadOnlyTool); ok {
		readOnly = ro.IsReadOnly()
	}
	safety := ParallelNever
	if readOnly {
		safety = ParallelSafe
	}
	return ToolCapabilities{
		IsReadOnly:     readOnly,
		ParallelSafety: safety,
		ApprovalMode:   "auto",
	}
}

// IsToolReadOnly checks if a tool implements CapableTool or ReadOnlyTool and returns true.
// Tools that do not implement either interface are treated as write-capable (false).
func IsToolReadOnly(t Tool) bool {
	if ct, ok := t.(CapableTool); ok {
		return ct.Capabilities().IsReadOnly
	}
	if ro, ok := t.(ReadOnlyTool); ok {
		return ro.IsReadOnly()
	}
	return false
}

// AvailableTool is an optional interface that tools can implement to indicate
// runtime availability. Tools that do not implement this interface are assumed
// to always be available. Use this for tools whose prerequisites (e.g. shell,
// external binary, network) may or may not be present at runtime.
type AvailableTool interface {
	Available() bool
}

// IsToolAvailable returns true if the tool is available for use. Tools that do
// not implement AvailableTool are always considered available.
func IsToolAvailable(t Tool) bool {
	if at, ok := t.(AvailableTool); ok {
		return at.Available()
	}
	return true
}

// Registry holds all registered tools.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name()] = t
}

func (r *Registry) Get(name string) (Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	if !ok {
		return nil, fmt.Errorf("tool not found: %s", name)
	}
	if !IsToolAvailable(t) {
		return nil, fmt.Errorf("tool not available: %s", name)
	}
	return t, nil
}

// All returns all registered tools that are currently available.
// Tools implementing AvailableTool with Available() == false are excluded.
func (r *Registry) All() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		if !IsToolAvailable(t) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// StreamCallback is called by tools to emit output chunks during execution.
// Tools call this for each line/chunk of output so channels can display
// real-time progress. If nil, tools buffer all output and return it at completion.
type StreamCallback func(chunk string)

type streamCtxKey struct{}

// WithStreamCallback attaches a StreamCallback to the context. Tools that
// support streaming will call the callback with output chunks as they arrive.
func WithStreamCallback(ctx context.Context, cb StreamCallback) context.Context {
	return context.WithValue(ctx, streamCtxKey{}, cb)
}

// StreamCallbackFromContext extracts the StreamCallback from the context.
// Returns nil if no callback was set.
func StreamCallbackFromContext(ctx context.Context) StreamCallback {
	cb, _ := ctx.Value(streamCtxKey{}).(StreamCallback)
	return cb
}

type sessionIDCtxKey struct{}

// WithSessionID attaches a session ID to the context.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDCtxKey{}, sessionID)
}

// SessionIDFromContext extracts the session ID from the context.
func SessionIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(sessionIDCtxKey{}).(string)
	return id
}

type workDirCtxKey struct{}

func WithWorkDir(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, workDirCtxKey{}, dir)
}

func WorkDirFromContext(ctx context.Context) string {
	dir, _ := ctx.Value(workDirCtxKey{}).(string)
	return dir
}

func ResolveWorkPath(ctx context.Context, path string) (string, error) {
	dir := WorkDirFromContext(ctx)
	if dir == "" {
		return path, nil
	}
	return resolvePathInRoot(dir, path)
}

func resolvePathInRoot(dir string, path string) (string, error) {
	root, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return "", err
	}

	var resolved string
	if filepath.IsAbs(path) {
		resolved = filepath.Clean(path)
	} else {
		resolved = filepath.Join(root, path)
	}

	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q escapes working directory %q", path, root)
	}
	return resolved, nil
}

// UnregisterByPrefix removes all tools whose name starts with prefix and returns the removed names.
func (r *Registry) UnregisterByPrefix(prefix string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var removed []string
	for name := range r.tools {
		if strings.HasPrefix(name, prefix) {
			delete(r.tools, name)
			removed = append(removed, name)
		}
	}
	return removed
}
