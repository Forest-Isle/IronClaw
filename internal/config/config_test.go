package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerDefaultAddress(t *testing.T) {
	cfg := defaultConfig()
	if cfg.Server.Addr != "127.0.0.1:8080" {
		t.Fatalf("server.addr = %q, want %q", cfg.Server.Addr, "127.0.0.1:8080")
	}
}

func TestServerTokenExpandsFromEnvironment(t *testing.T) {
	t.Setenv("DAIMON_ADMIN_TOKEN", "secret-token")
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(`
llm:
  provider: claude
  api_key: test-key
server:
  enabled: true
  token: ${DAIMON_ADMIN_TOKEN}
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Server.Token != "secret-token" {
		t.Fatalf("server.token = %q, want environment value", cfg.Server.Token)
	}
}

func TestServerEnabledRequiresToken(t *testing.T) {
	cfg := validConfigForServerTest()
	cfg.Server.Enabled = true
	cfg.Server.Token = "  "
	if err := validate(&cfg); err == nil || !strings.Contains(err.Error(), "server.token") {
		t.Fatalf("validate() error = %v, want server.token error", err)
	}
}

func validConfigForServerTest() Config {
	cfg := defaultConfig()
	cfg.LLM.APIKey = "test-key"
	return cfg
}
