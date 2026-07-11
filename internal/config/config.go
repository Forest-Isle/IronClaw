package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/Forest-Isle/daimon/internal/appdir"
	"gopkg.in/yaml.v3"
)

type Config struct {
	LLM         LLMConfig         `yaml:"llm"`
	Telegram    TelegramConfig    `yaml:"telegram"`
	TUI         TUIConfig         `yaml:"tui"`
	Agent       AgentConfig       `yaml:"agent"`
	Store       StoreConfig       `yaml:"store"`
	Memory      MemoryConfig      `yaml:"memory"`
	Tools       ToolsConfig       `yaml:"tools"`
	Server      ServerConfig      `yaml:"server"`
	Health      HealthConfig      `yaml:"health"`
	Log         LogConfig         `yaml:"log"`
	Telemetry   TelemetryConfig   `yaml:"telemetry"`
	Skills      SkillsConfig      `yaml:"skills"`
	Agents      AgentsConfig      `yaml:"agents"`
	Permissions PermissionsConfig `yaml:"permissions"`
	Hooks       HooksConfig       `yaml:"hooks"`
	Economy     EconomyConfig     `yaml:"economy"`
}

// EconomyConfig configures the cost ledger report (DAIMON_BLUEPRINT.md §4.11).
// Prices maps a model id (or a substring of one) to its USD per-million-token
// rates; the `daimon costs` report uses them to convert recorded token usage into
// dollars. A model with no configured price is reported in tokens only — no rates
// are hard-coded, since they vary by provider/endpoint and change over time.
type EconomyConfig struct {
	Prices   map[string]ModelPrice `yaml:"prices"`
	Throttle ThrottleConfig        `yaml:"throttle"`
}

// ThrottleConfig sets thresholds for the cost/ROI throttle advisor
// (DAIMON_BLUEPRINT.md §4.11). The `daimon costs` report surfaces advisory
// recommendations. Runtime enforcement is gated by Enforce: when false, flagged
// classes are only observed; when true, flagged autonomous Cognize classes are
// skipped until the class recovers or the user overrides the throttle.
type ThrottleConfig struct {
	Enforce           bool    `yaml:"enforce"`              // when true, flagged classes are automatically throttled (default false)
	PerClassBudgetUSD float64 `yaml:"per_class_budget_usd"` // flag a class spending more than this over the report window (0 = off)
	MinCleanRate      float64 `yaml:"min_clean_rate"`       // flag a class whose clean-outcome rate is below this, 0..1 (0 = off)
	MinEpisodes       int     `yaml:"min_episodes"`         // don't flag low value on fewer than this many episodes (0 = no minimum)
}

// ModelPrice is a model's USD rate per million tokens for each token class.
type ModelPrice struct {
	InputPerMTok         float64 `yaml:"input_per_mtok"`
	OutputPerMTok        float64 `yaml:"output_per_mtok"`
	CacheReadPerMTok     float64 `yaml:"cache_read_per_mtok"`
	CacheCreationPerMTok float64 `yaml:"cache_creation_per_mtok"`
}

var envVarPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// ExpandEnv replaces ${VAR} placeholders in data with values from the environment.
// Placeholders with no matching env var are left unchanged.
func ExpandEnv(data []byte) []byte {
	return envVarPattern.ReplaceAllFunc(data, func(match []byte) []byte {
		varName := envVarPattern.FindSubmatch(match)[1]
		if val, ok := os.LookupEnv(string(varName)); ok {
			return []byte(val)
		}
		return match
	})
}

// FindConfigPath resolves the config file path by searching standard locations.
// explicitPath comes from the -c flag; empty means "auto-discover".
// devMode uses configs/daimon.yaml when no explicit path is given.
//
// Production (devMode=false, explicitPath empty):
//
//	~/.daimon/config.yaml
//
// Development (devMode=true, explicitPath empty):
//
//	configs/daimon.yaml in CWD
func FindConfigPath(explicitPath string, devMode bool) (string, error) {
	if explicitPath != "" {
		if _, err := os.Stat(explicitPath); err == nil {
			return explicitPath, nil
		}
		return "", fmt.Errorf("config file not found: %s", explicitPath)
	}

	if devMode {
		cwd, err := os.Getwd()
		if err != nil {
			cwd = "."
		}
		devPath := filepath.Join(cwd, "configs", "daimon.yaml")
		if _, err := os.Stat(devPath); err == nil {
			return devPath, nil
		}
		return "", fmt.Errorf(
			"dev mode: configs/daimon.yaml not found.\n" +
				"  Copy from configs/daimon.example.yaml and edit it",
		)
	}

	globalPath := filepath.Join(appdir.BaseDir(), "config.yaml")
	if _, err := os.Stat(globalPath); err == nil {
		return globalPath, nil
	}

	return "", fmt.Errorf(
		"no config file found.\n\n" +
			"  Production: create ~/.daimon/config.yaml for global defaults.\n" +
			"  Development: daimon tui --dev\n" +
			"  Template:    cp configs/daimon.example.yaml ~/.daimon/config.yaml",
	)
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	// Substitute environment variables
	expanded := ExpandEnv(data)

	cfg := defaultConfig()
	if err := yaml.Unmarshal(expanded, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// Warn about unknown top-level YAML keys (typos, removed features like RL).
	CheckUnknownKeys(expanded)

	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return &cfg, nil
}

func defaultConfig() Config {
	return Config{
		LLM: LLMConfig{
			Provider:  "claude",
			Model:     "claude-sonnet-4-20250514",
			MaxTokens: 8192,
			Retry: RetryConfig{
				MaxRetries: 3,
				BaseDelay:  1 * time.Second,
				MaxDelay:   30 * time.Second,
			},
		},
		Telegram: TelegramConfig{
			Timeout: 30 * time.Second,
		},
		Agent: AgentConfig{
			MaxIterations:          20,
			EpisodeEnabled:         true,
			SubagentEpisodeEnabled: true,
			Action: ActionConfig{
				HoldWindowSeconds:        120,
				HoldDrainIntervalSeconds: 15,
			},
			Execution: ExecutionConfig{
				MaxParallelTools:       3,
				ApprovalTimeoutSeconds: 120,
			},
			Compression: CompressionConfig{
				Strategy: "layered",
				Layers: CompressionLayers{
					ToolOutputReducePct: 30,
					SummarizePct:        50,
					EmergencyPct:        90,
				},
				TokenEstimateRatio: 0.25,
			},
		},
		Memory: MemoryConfig{
			Enabled: true,
		},
		Store: StoreConfig{
			Path: filepath.Join(appdir.BaseDir(), "data", appdir.DBName),
		},
		Health: HealthConfig{
			Port: 9090,
		},
		Server: ServerConfig{
			Addr: "127.0.0.1:8080",
		},
		Log: LogConfig{
			Level:  "info",
			Format: "text",
		},
		Telemetry: TelemetryConfig{
			Enabled:       true,
			TracePath:     filepath.Join(appdir.BaseDir(), "traces", "events.jsonl"),
			ReplayEnabled: true,
			ReplayDir:     filepath.Join(appdir.BaseDir(), "replays"),
		},
		Tools: ToolsConfig{
			Bash: BashToolConfig{
				Enabled:          true,
				Timeout:          30 * time.Second,
				RequiresApproval: true,
			},
			File: FileToolConfig{
				Enabled: true,
			},
			HTTP: HTTPToolConfig{
				Enabled: true,
				Timeout: 30 * time.Second,
			},
			Verify: VerifyConfig{
				Enabled: true,
			},
			MCP: MCPConfig{
				PollInterval: 30 * time.Second,
			},
			ConcurrentExecution: ConcurrentExecutionConfig{
				Enabled:        true,
				MaxConcurrency: 4,
			},
			ResultPersistence: ResultPersistenceConfig{
				Enabled:        true,
				ThresholdBytes: 8192,
				PreviewChars:   2000,
				TTLHours:       24,
			},
		},
		Skills: SkillsConfig{
			Enabled: true,
		},
		Agents: AgentsConfig{
			Enabled: true,
		},
		Permissions: PermissionsConfig{
			Default: "ask",
		},
	}
}
