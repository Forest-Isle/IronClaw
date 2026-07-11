package config

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/Forest-Isle/daimon/internal/appdir"
	"gopkg.in/yaml.v3"
)

// knownProviders lists the supported LLM provider values.
var knownProviders = map[string]bool{
	"claude":            true,
	"openai":            true,
	"openai-compatible": true,
}

// knownTopLevelKeys is the set of valid top-level YAML keys in the Config struct.
// Built from Config struct yaml tags at init time.
var knownTopLevelKeys = buildKnownKeys()

func buildKnownKeys() map[string]bool {
	keys := make(map[string]bool)
	t := reflect.TypeOf(Config{})
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		// Strip ",omitempty" and ",flow" etc.
		name := tag
		for j := 0; j < len(tag); j++ {
			if tag[j] == ',' {
				name = tag[:j]
				break
			}
		}
		keys[name] = true
	}
	return keys
}

// CheckUnknownKeys parses raw YAML data and warns about top-level keys that
// have no corresponding Config struct field. This catches typos and keys from
// removed features (e.g., RL config) that silently do nothing.
func CheckUnknownKeys(rawYAML []byte) {
	var raw map[string]any
	if err := yaml.Unmarshal(rawYAML, &raw); err != nil {
		return // can't parse, let the main unmarshal report the error
	}
	for key := range raw {
		if !knownTopLevelKeys[key] {
			slog.Warn("config: unknown top-level key — has no effect",
				"key", key,
				"hint", "check for typos or keys from removed features (e.g., rl:)")
		}
	}
}

func validate(cfg *Config) error {
	if cfg.LLM.APIKey == "" {
		return fmt.Errorf("llm.api_key is required")
	}
	if !knownProviders[cfg.LLM.Provider] {
		return fmt.Errorf("llm.provider must be one of: claude, openai, openai-compatible (got %q)", cfg.LLM.Provider)
	}
	// Telegram is optional — only validate when token or user IDs are provided.
	if cfg.Telegram.Token != "" || len(cfg.Telegram.AllowedUserIDs) > 0 {
		if cfg.Telegram.Token == "" {
			return fmt.Errorf("telegram.token is required when telegram is configured")
		}
		if len(cfg.Telegram.AllowedUserIDs) == 0 {
			return fmt.Errorf("telegram.allowed_user_ids must have at least one entry when telegram is configured")
		}
	}
	if cfg.Agent.MaxIterations <= 0 {
		cfg.Agent.MaxIterations = 20
	}
	for id, reflex := range cfg.Agent.Heart.Reflexes {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("agent.heart.reflexes: reflex id must not be empty")
		}
		hasInline := strings.TrimSpace(reflex.Workflow) != ""
		hasPath := strings.TrimSpace(reflex.WorkflowPath) != ""
		if hasInline == hasPath {
			return fmt.Errorf("agent.heart.reflexes.%s: set exactly one of workflow or workflow_path", id)
		}
		if reflex.TimeoutSeconds < 0 {
			return fmt.Errorf("agent.heart.reflexes.%s.timeout_seconds must be >= 0", id)
		}
	}
	if cfg.Store.Path == "" {
		cfg.Store.Path = filepath.Join(appdir.BaseDir(), "data", appdir.DBName)
	}
	if cfg.Server.Enabled && strings.TrimSpace(cfg.Server.Token) == "" {
		return fmt.Errorf("server.token is required when server is enabled")
	}

	// Validate MCP server configs
	for name, srv := range cfg.Tools.MCP.Servers {
		if srv.Command == "" {
			return fmt.Errorf("mcp.servers.%s: command is required", name)
		}
	}

	// Throttle thresholds bound the cost/ROI advisor; out-of-range values would
	// produce misleading recommendations (e.g. a clean-rate above 1 flags every
	// class). A zero/unset threshold is valid — it disables that check.
	t := cfg.Economy.Throttle
	if t.PerClassBudgetUSD < 0 {
		return fmt.Errorf("economy.throttle.per_class_budget_usd must be >= 0 (got %v)", t.PerClassBudgetUSD)
	}
	if t.MinCleanRate < 0 || t.MinCleanRate > 1 {
		return fmt.Errorf("economy.throttle.min_clean_rate must be between 0 and 1 (got %v)", t.MinCleanRate)
	}
	if t.MinEpisodes < 0 {
		return fmt.Errorf("economy.throttle.min_episodes must be >= 0 (got %d)", t.MinEpisodes)
	}

	return nil
}
