package config

type AgentConfig struct {
	MaxIterations          int               `yaml:"max_iterations"`
	EpisodeEnabled         bool              `yaml:"episode_enabled"`
	KernelFallbackEnabled  bool              `yaml:"kernel_fallback_enabled"`
	SubagentEpisodeEnabled bool              `yaml:"subagent_episode_enabled"`
	HeartEnabled           bool              `yaml:"heart_enabled"` // route autonomous (non-chat) events through heart→attention→episode
	Heart                  HeartConfig       `yaml:"heart"`
	Action                 ActionConfig      `yaml:"action"`
	SystemPrompt           string            `yaml:"system_prompt"`
	Personality            string            `yaml:"-"` // Soul.md → persona/style (injected by userdir)
	PersistentRules        string            `yaml:"-"` // Memory.md → long-term rules (injected by userdir)
	Execution              ExecutionConfig   `yaml:"execution"`
	Compression            CompressionConfig `yaml:"compression"`
}

// ActionConfig tunes the governed action layer. HoldEnabled defaults off so the
// hold queue is inert unless explicitly enabled.
type ActionConfig struct {
	HoldEnabled              bool `yaml:"hold_enabled"`
	HoldWindowSeconds        int  `yaml:"hold_window_seconds"`
	HoldDrainIntervalSeconds int  `yaml:"hold_drain_interval_seconds"`
}

// HeartConfig tunes the event heart. It only takes effect when HeartEnabled is
// true. Durations are expressed as integers (the codebase's yaml decoder does
// not parse "24h" into time.Duration), matching ExecutionConfig.ApprovalTimeoutSeconds.
type HeartConfig struct {
	HeartbeatIntervalMinutes  int                     `yaml:"heartbeat_interval_minutes"`   // 0 = no timer source registered
	DailyBriefIntervalMinutes int                     `yaml:"daily_brief_interval_minutes"` // 0 = no daily-brief timer; >0 fires internal.daily_brief every N minutes
	HealthIntervalMinutes     int                     `yaml:"health_interval_minutes"`      // 0 = no selfops health timer; >0 fires internal.health every N minutes
	SleepIntervalMinutes      int                     `yaml:"sleep_interval_minutes"`       // 0 = no autonomous sleep timer; >0 fires internal.sleep every N minutes (daily window = 1440)
	SleepIdleMinutes          int                     `yaml:"sleep_idle_minutes"`           // require this many minutes of no real-event activity before an autonomous cycle runs; 0 = no idle gate
	FSWatchDirs               []string                `yaml:"fs_watch_dirs"`                // directories the fs source watches (fsnotify); empty = no fs source. Use absolute paths or ${HOME}/... (env-expanded). Do not watch ~/.daimon.
	Mail                      MailConfig              `yaml:"mail"`                         // IMAP mail sensory source; disabled unless enabled and imap_host are set
	Calendar                  CalendarConfig          `yaml:"calendar"`                     // CalDAV calendar sensory source; disabled unless enabled and caldav_url are set
	ModelRouter               bool                    `yaml:"model_router"`                 // wire the small-model (haiku) triage tier
	HighRiskKinds             []string                `yaml:"high_risk_kinds"`              // extra always-wake event-kind prefixes (added to safe defaults)
	ChatThroughHeart          bool                    `yaml:"chat_through_heart"`           // record inbound chat in the event stream (dedup + audit) before handling
	Reflexes                  map[string]ReflexConfig `yaml:"reflexes"`                     // explicit reflex_id -> workflow mapping for attention action=reflex
}

type MailConfig struct {
	Enabled             bool   `yaml:"enabled"`
	IMAPHost            string `yaml:"imap_host"`
	IMAPPort            int    `yaml:"imap_port"`
	Username            string `yaml:"username"`
	Password            string `yaml:"password"`
	Mailbox             string `yaml:"mailbox"`
	PollIntervalSeconds int    `yaml:"poll_interval_seconds"`
}

// CalendarConfig tunes the CalDAV calendar sensory source.
type CalendarConfig struct {
	Enabled             bool     `yaml:"enabled"`
	CalDAVURL           string   `yaml:"caldav_url"`
	Username            string   `yaml:"username"`
	Password            string   `yaml:"password"`
	CalendarPaths       []string `yaml:"calendar_paths"`        // empty = auto-discover all VEVENT calendars
	PollIntervalSeconds int      `yaml:"poll_interval_seconds"` // 0 = 300s default
	LookaheadMinutes    int      `yaml:"lookahead_minutes"`     // 0 = 1440 (24h) default
}

type ReflexConfig struct {
	Workflow       string `yaml:"workflow"`        // inline YAML/JSON workflow spec
	WorkflowPath   string `yaml:"workflow_path"`   // path to a YAML/JSON workflow spec
	TimeoutSeconds int    `yaml:"timeout_seconds"` // 0 = default timeout
}

// CompressionConfig controls the context compression strategy.
type CompressionConfig struct {
	Strategy           string            `yaml:"strategy"` // "layered" | "legacy"
	Layers             CompressionLayers `yaml:"layers"`
	TokenEstimateRatio float64           `yaml:"token_estimate_ratio"`
}

// CompressionLayers defines thresholds for each compression layer.
type CompressionLayers struct {
	ToolOutputReducePct int `yaml:"tool_output_reduce_pct"`
	SummarizePct        int `yaml:"summarize_pct"`
	EmergencyPct        int `yaml:"emergency_pct"`
}

// ExecutionConfig holds runtime execution settings for the agent loop.
type ExecutionConfig struct {
	MaxParallelTools       int  `yaml:"max_parallel_tools"`       // default 3
	ApprovalTimeoutSeconds int  `yaml:"approval_timeout_seconds"` // default 120
	StreamingEnabled       bool `yaml:"streaming_enabled"`        // enable channel-based streaming pipeline (default false)
}
