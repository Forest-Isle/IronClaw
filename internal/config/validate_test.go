package config

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestValidateEnabledServerRejectsUnresolvedTokenPlaceholder(t *testing.T) {
	cfg := &Config{}
	cfg.LLM.Provider = "claude"
	cfg.LLM.APIKey = "test"
	cfg.Server.Enabled = true
	cfg.Server.Token = "${DAIMON_ADMIN_TOKEN}"

	err := validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "server.token") {
		t.Fatalf("validate() error = %v, want unresolved server.token error", err)
	}
}

func TestValidateDisabledServerAllowsUnresolvedTokenPlaceholder(t *testing.T) {
	cfg := &Config{}
	cfg.LLM.Provider = "claude"
	cfg.LLM.APIKey = "test"
	cfg.Server.Token = "${DAIMON_ADMIN_TOKEN}"

	if err := validate(cfg); err != nil {
		t.Fatalf("validate() error = %v, want nil", err)
	}
}

func TestValidateThrottleBounds(t *testing.T) {
	base := func() *Config {
		c := &Config{}
		c.LLM.Provider = "claude"
		c.LLM.APIKey = "test"
		return c
	}
	cases := []struct {
		name    string
		t       ThrottleConfig
		wantErr bool
	}{
		{"empty ok", ThrottleConfig{}, false},
		{"valid", ThrottleConfig{PerClassBudgetUSD: 5, MinCleanRate: 0.5, MinEpisodes: 3}, false},
		{"clean rate 0 ok", ThrottleConfig{MinCleanRate: 0}, false},
		{"clean rate 1 ok", ThrottleConfig{MinCleanRate: 1}, false},
		{"negative budget", ThrottleConfig{PerClassBudgetUSD: -1}, true},
		{"clean rate above 1", ThrottleConfig{MinCleanRate: 2}, true},
		{"negative clean rate", ThrottleConfig{MinCleanRate: -0.1}, true},
		{"negative min episodes", ThrottleConfig{MinEpisodes: -1}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			cfg.Economy.Throttle = tc.t
			err := validate(cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validate() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateReflexConfig(t *testing.T) {
	base := func() *Config {
		c := &Config{}
		c.LLM.Provider = "claude"
		c.LLM.APIKey = "test"
		return c
	}
	cases := []struct {
		name    string
		reflex  ReflexConfig
		wantErr bool
	}{
		{"inline workflow", ReflexConfig{Workflow: "name: x\nstages: []"}, false},
		{"workflow path", ReflexConfig{WorkflowPath: "/tmp/reflex.yaml"}, false},
		{"missing both", ReflexConfig{}, true},
		{"both set", ReflexConfig{Workflow: "x", WorkflowPath: "/tmp/reflex.yaml"}, true},
		{"negative timeout", ReflexConfig{WorkflowPath: "/tmp/reflex.yaml", TimeoutSeconds: -1}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			cfg.Agent.Heart.Reflexes = map[string]ReflexConfig{"r": tc.reflex}
			err := validate(cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validate() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestCheckUnknownKeys_NoWarnings(t *testing.T) {
	// Known keys should not trigger warnings.
	yaml := []byte(`
llm:
  provider: claude
  api_key: test
telegram:
  token: test
  allowed_user_ids: [123]
agent:
  mode: simple
store:
  path: ./data/test.db
`)
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))

	CheckUnknownKeys(yaml)
	if buf.Len() > 0 {
		t.Errorf("unexpected warnings for known keys: %s", buf.String())
	}
}

func TestCheckUnknownKeys_WarnsOnUnknown(t *testing.T) {
	// Unknown top-level keys should trigger warnings.
	yaml := []byte(`
rl:
  enabled: true
  cold_start_episodes: 1000
some_typo:
  foo: bar
llm:
  provider: claude
  api_key: test
telegram:
  token: test
  allowed_user_ids: [123]
`)
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))

	CheckUnknownKeys(yaml)
	if buf.Len() == 0 {
		t.Error("expected warnings for unknown keys 'rl' and 'some_typo', got none")
	}
	t.Logf("warnings: %s", buf.String())
}

func TestCheckUnknownKeys_InvalidYAML(t *testing.T) {
	// Invalid YAML should not panic.
	yaml := []byte(`this is not valid: {{{`)
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))

	// Should not panic.
	CheckUnknownKeys(yaml)
}

func TestBuildKnownKeys_HasExpectedKeys(t *testing.T) {
	keys := buildKnownKeys()
	expected := []string{"llm", "telegram", "agent", "store", "memory", "tools", "server"}
	for _, k := range expected {
		if !keys[k] {
			t.Errorf("expected key %q in knownTopLevelKeys", k)
		}
	}
	// Verify removed keys are NOT present.
	if keys["rl"] {
		t.Error("'rl' key should not be in knownTopLevelKeys (RL was removed)")
	}
}
