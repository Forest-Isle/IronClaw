package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// repoRoot walks up from this test file to the repository root (where go.mod lives).
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	dir := filepath.Dir(file)
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not locate repo root (go.mod)")
	return ""
}

// TestExampleConfigLoadsAndValidates ensures the shipped example config —
// the file users copy to bootstrap — parses, expands env vars, and passes
// validation. A field rename that breaks onboarding is caught here.
func TestExampleConfigLoadsAndValidates(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "configs", "daimon.example.yaml")

	// Provide env values referenced via ${VAR} so expansion produces a valid config.
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	t.Setenv("OPENAI_API_KEY", "test-key")

	// Load from the config dir so any project-level overlay resolves predictably.
	cfg, err := Load(path)
	require.NoError(t, err, "example config must load and validate")
	require.NotNil(t, cfg)

	// Sanity: core fields populated from the file (not just defaults).
	assert.Equal(t, "claude", cfg.LLM.Provider)
	assert.NotEmpty(t, cfg.LLM.Model)
	assert.NotZero(t, cfg.Agent.MaxIterations)
	assert.Equal(t, "127.0.0.1:8080", cfg.Server.Addr)
}

// TestExampleConfigHasNoUnknownTopLevelKeys verifies every top-level key in the
// example config maps to a real Config struct field. Stale keys (from removed
// features) would silently no-op at runtime — this fails loudly instead.
func TestExampleConfigHasNoUnknownTopLevelKeys(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "configs", "daimon.example.yaml")

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, yaml.Unmarshal(data, &raw))

	for key := range raw {
		assert.Truef(t, knownTopLevelKeys[key],
			"example config has unknown top-level key %q — it has no effect at runtime", key)
	}
}
