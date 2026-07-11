package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Forest-Isle/daimon/internal/agent"
	"github.com/Forest-Isle/daimon/internal/appdir"
	"github.com/Forest-Isle/daimon/internal/channel"
	"github.com/Forest-Isle/daimon/internal/config"
	"github.com/Forest-Isle/daimon/internal/economy"
	"github.com/Forest-Isle/daimon/internal/episode"
	"github.com/Forest-Isle/daimon/internal/feature"
	"github.com/Forest-Isle/daimon/internal/heart"
	"github.com/Forest-Isle/daimon/internal/mind"
	"github.com/Forest-Isle/daimon/internal/proposals"
	"github.com/Forest-Isle/daimon/internal/session"
	"github.com/Forest-Isle/daimon/internal/sleep"
	"github.com/Forest-Isle/daimon/internal/store"
	"github.com/Forest-Isle/daimon/internal/taskruntime"
	"github.com/Forest-Isle/daimon/internal/tool"
	"github.com/Forest-Isle/daimon/internal/workflow"
	"github.com/Forest-Isle/daimon/internal/world"
)

type Gateway struct {
	db         *store.DB
	stopCh     chan struct{}
	stopOnce   sync.Once
	initCtx    context.Context
	initCancel context.CancelFunc

	agent      *agent.Agent
	sessions   *session.Manager
	features   *feature.Registry
	contextMgr agent.ContextManager

	EpisodeRunner  *episode.Runner
	EpisodeEnabled bool

	config       *ConfigSubsystem
	database     *DatabaseSubsystem
	admin        *AdminSubsystem
	toolSub      *ToolSubsystem
	memory       *MemorySubsystem
	skills       *SkillSubsystem
	channels     *ChannelSubsystem
	multiAgent   *MultiAgentSubsystem
	mcpSub       *MCPSubsystem
	health       *HealthSubsystem
	commands     *CommandSubsystem
	scheduler    *SchedulerSubsystem
	taskLedger   *taskruntime.Ledger
	telemetry    *TelemetrySubsystem
	heart        *HeartSubsystem // nil unless agent.heart_enabled
	reflexes     *reflexExecutor
	sleep        *sleep.Runner // consolidation jobs, triggered by /sleep (and later the heart)
	sleepRunning atomic.Bool   // gateway-level guard: manual and autonomous cycles never overlap
	lastEventAt  atomic.Int64  // unix sec of last real (non-internal) heart activity
	throttle     *throttleGate // economy C2e-3: gated enforcement for autonomous Cognize classes

	// proposals (§4.9): the anticipation loop's decision + delivery wiring.
	proposals         *proposalCoordinator
	proposalDeliverer *proposalDeliverer

	subsystems Subsystems
}

type GatewayOptions struct {
	ConfigPath string
}

func New(cfg *config.Config, opts ...GatewayOptions) (*Gateway, error) {
	opt := GatewayOptions{}
	if len(opts) > 0 {
		opt = opts[0]
	}

	// The heart routes events into autonomous episodes, which need the cognitive
	// kernel. With episodes off, every routed event would fail with "cognitive
	// kernel unavailable" and any due follow-up would burn silently. Reject the
	// combination here, before any resources are allocated, rather than running a
	// loop that can only fail.
	if cfg.Agent.HeartEnabled && !cfg.Agent.EpisodeEnabled {
		return nil, fmt.Errorf("config: agent.heart_enabled requires agent.episode_enabled (the heart drives episodes through the kernel)")
	}

	gw := &Gateway{
		stopCh:   make(chan struct{}),
		throttle: &throttleGate{throttled: map[string]bool{}, overrides: map[string]bool{}},
	}
	gw.initCtx, gw.initCancel = context.WithTimeout(context.Background(), 30*time.Second)
	eventBus := agent.NewEventBus()

	gw.config = InitConfig(cfg, opt.ConfigPath)
	gw.telemetry = InitTelemetry(cfg, eventBus)
	featSub := InitFeatures(cfg)
	gw.features = featSub.Registry
	dbSub, err := InitDatabase(cfg.Store.Path)
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	gw.database = dbSub
	gw.db = dbSub.DB
	gw.sessions = dbSub.Sessions
	gw.admin, err = InitAdmin(cfg.Server, gw.db)
	if err != nil {
		_ = gw.db.Close()
		return nil, fmt.Errorf("admin: %w", err)
	}
	gw.taskLedger = taskruntime.NewLedger(gw.db.DB)
	gw.channels = &ChannelSubsystem{channels: make(map[string]channel.Channel)}

	gw.toolSub = InitTools(gw.initCtx, cfg, featSub, gw.sessions, gw.channels, gw.db, eventBus)
	if gw.toolSub.ActionInterceptor != nil {
		gw.toolSub.ActionInterceptor.SetTrustNotifier(newGatewayTrustNotifier(gw))
	}

	builder := agent.NewDepsBuilder()
	builder.Core.Tools = gw.toolSub.Registry
	builder.Core.Sessions = gw.sessions
	builder.Core.DB = gw.db
	builder.Security = agent.SecurityDeps{
		Interceptor: gw.toolSub.InterceptorChain,
		HookMgr:     gw.toolSub.HookMgr,
		PermEngine:  gw.toolSub.PermEngine,
	}

	agentSub := InitAgentRuntime(builder, cfg)
	gw.EpisodeRunner = episode.NewRunner(agentSub.Provider, gw.toolSub.WorldStore, &gw.toolSub.WorldIdentity, eventBus)
	gw.EpisodeRunner.SetValues(gw.toolSub.ValuesStore)
	// Economy: record per-episode token consumption to the costs ledger (§4.11).
	// Best-effort and observational — a cost-write failure never affects an episode.
	gw.EpisodeRunner.SetCostRecorder(costRecorderAdapter{
		store: economy.NewStore(gw.db.DB),
		now:   func() int64 { return time.Now().Unix() },
	})
	gw.EpisodeEnabled = cfg.Agent.EpisodeEnabled

	// Sleep: consolidation jobs over the world model. The digest job regenerates
	// the agent's self-summary; the drift job flags active values that recent
	// activity contradicts. Both use the LLM provider. Triggered on demand via
	// /sleep today; the heart can schedule it later.
	sleepSummarizer := &completerAdapter{provider: agentSub.Provider, model: cfg.LLM.Model, maxTokens: 1024}
	proposalsStore := proposals.NewStore(gw.db.DB)
	// The clock is injected at this boundary so the sleep jobs stay clock-free.
	unixNow := func() int64 { return time.Now().Unix() }
	var semanticEmbedder world.Embedder
	if cfg.Memory.SemanticRetrieval {
		if h := buildEmbedder(cfg); h.real {
			semanticEmbedder = h.provider
			gw.toolSub.WorldStore.SetEmbedder(semanticEmbedder)
		} else {
			slog.Warn("semantic_retrieval enabled but no real embedder configured; staying lexical")
		}
	}
	gw.sleep = sleep.NewRunner(
		sleep.NewEmbedJob(gw.toolSub.WorldStore, semanticEmbedder),
		sleep.NewDigestJob(gw.toolSub.WorldStore, sleepSummarizer),
		sleep.NewDriftJob(gw.toolSub.ValuesStore, gw.toolSub.WorldStore, sleepSummarizer),
		sleep.NewRollupJob(gw.toolSub.WorldStore, sleepSummarizer),
		// Reconcile: supersede contradicting/duplicate facts so the world stays
		// internally consistent (invariant #1); the canonical fact survives, stale
		// ones are removed from retrieval with a correction journal audit trace.
		sleep.NewReconcileJob(gw.toolSub.WorldStore, sleepSummarizer),
		// Distill: mine the journal for recurring successful episode patterns and
		// surface each as an append-only "distill candidate" decision (blueprint §4.8
		// detection half). It does NOT generate or promote skills — promotion is a
		// separate canary-gated step, since an auto-promoted skill executes
		// autonomously (the blueprint's highest 带病转正 risk, §706).
		sleep.NewDistillJob(gw.toolSub.WorldStore, sleepSummarizer),
		sleep.NewPromoteJob(gw.toolSub.WorldStore, sleepSummarizer, newFileDraftSink(defaultDistillStagingDir())),
		// Anticipation: scan commitments due soon and queue proposals the user will
		// likely need. Reads the world, writes the proposals queue; delivery/decision
		// UX is a later increment that reads from that queue.
		sleep.NewProposalsJob(
			worldCommitmentSource{world: gw.toolSub.WorldStore},
			proposalsStoreSink{store: proposalsStore, now: unixNow},
			sleepSummarizer,
			unixNow,
		),
		// Distill screen: screen staging drafts into promote_skill proposals. This
		// is a judge over draft quality/safety/structure, not a behavior replay
		// Canary: skills are lazily read by read_skill rather than injected into the
		// system prompt, so behavior canaries remain a later increment.
		sleep.NewDistillScreenJob(
			defaultStagedDraftSource(),
			proposalsStoreSink{store: proposalsStore, now: unixNow},
			sleepSummarizer,
			unixNow,
		),
		throttleEvalJob{refresh: gw.refreshThrottle},
	)

	gw.memory = InitMemorySystem(featSub, cfg, builder, agentSub.Provider, gw.db, gw.toolSub.Registry)
	gw.memory.BuildCortex()
	builder.Memory.Cortex = gw.memory.Cortex()

	gw.skills = InitSkills(featSub, cfg, gw.toolSub.Registry, builder)

	gw.multiAgent = InitMultiAgent(featSub, cfg, builder, agentSub.Provider,
		gw.sessions, gw.db, gw.memory.Store(), gw.toolSub.Registry, gw.toolSub.ResultStore)
	if gw.multiAgent.SubAgentMgr != nil {
		gw.multiAgent.SubAgentMgr.SetEpisodeKernel(gw.EpisodeRunner, gw.EpisodeEnabled && cfg.Agent.SubagentEpisodeEnabled)
	}
	gw.contextMgr = gw.multiAgent.ContextMgr
	if gw.multiAgent.AgentMgr != nil && gw.multiAgent.SubAgentMgr != nil {
		gw.toolSub.Registry.Register(agent.NewWorkflowTool(
			gw.multiAgent.AgentMgr,
			gw.multiAgent.SubAgentMgr,
			workflow.NewSQLiteCache(gw.db.DB),
			eventBus,
		))
		gw.toolSub.Registry.Register(agent.NewDispatchTool(
			gw.multiAgent.SubAgentMgr,
			gw.multiAgent.AgentMgr,
		))
	}

	gw.mcpSub = InitMCP()

	deps := builder.Build()
	gw.agent = agent.NewAgent(&deps, &agent.LinearLoop{}, eventBus)
	gw.agent.SetApprovalFunc(gw.handleApproval)
	gw.agent.SetKernel(gw.EpisodeRunner, gw.EpisodeEnabled)

	// Proposals (§4.9): decision coordinator + delivery driver over the same queue
	// the sleep anticipation job fills. Needs gw.agent (to fire accepted plans).
	gw.wireProposals(proposalsStore, unixNow)

	gw.health = InitHealth(cfg, gw.db)
	gw.commands = InitCommands(gw)

	// SchedulerChannel notifier is wired post-construction in main.go
	// after the Telegram channel is created.
	gw.scheduler = InitScheduler(gw.db, nil, gw.taskLedger)
	gw.AddChannel(gw.scheduler.Channel)

	// Heart: the autonomous event path. Built only when enabled; when off, the
	// binary behaves exactly as before (chat path untouched). The dispatch
	// handler needs gw.agent and gw.channels, so it is wired here after both exist.
	if cfg.Agent.HeartEnabled {
		// Invariant (heart ⇒ episode) is enforced at the top of New().
		var reflexErr error
		gw.reflexes, reflexErr = newReflexExecutor(cfg.Agent.Heart.Reflexes, gw.toolSub.Registry, gw.toolSub.InterceptorChain)
		if reflexErr != nil {
			return nil, fmt.Errorf("reflexes: %w", reflexErr)
		}
		gw.heart = InitHeart(cfg, gw.db, agentSub.Provider, gw.toolSub.WorldStore)
		gw.heart.heart = heart.New(gw.heart.store, gw.newEventDispatcher().handle)

		// Timer follow-ups planted by episodes re-enter through the heart: the
		// runner gets a planter backed by the follow-up store, and a source polls
		// the queue to fire due entries as internal.followup events.
		followUps := heart.NewFollowUpStore(gw.db.DB)
		gw.EpisodeRunner.SetPlanter(followUpPlanter{store: followUps, now: time.Now})
		gw.heart.heart.Register(&heart.FollowUpSource{Store: followUps})

		if mins := cfg.Agent.Heart.HeartbeatIntervalMinutes; mins > 0 {
			gw.heart.heart.Register(&heart.TimerSource{
				SourceName: "timer",
				Kind:       "internal.heartbeat",
				Interval:   time.Duration(mins) * time.Minute,
			})
		}
		if mins := cfg.Agent.Heart.DailyBriefIntervalMinutes; mins > 0 {
			gw.heart.heart.Register(&heart.TimerSource{
				SourceName: "timer",
				Kind:       "internal.daily_brief",
				Interval:   time.Duration(mins) * time.Minute,
			})
		}
		if mins := cfg.Agent.Heart.HealthIntervalMinutes; gw.features != nil && gw.features.IsEnabled("selfops") && mins > 0 {
			gw.heart.heart.Register(&heart.TimerSource{
				SourceName: "timer",
				Kind:       "internal.health",
				Interval:   time.Duration(mins) * time.Minute,
			})
		}
		if mins := cfg.Agent.Heart.SleepIntervalMinutes; mins > 0 {
			gw.heart.heart.Register(&heart.TimerSource{
				SourceName: "timer",
				Kind:       "internal.sleep",
				Interval:   time.Duration(mins) * time.Minute,
			})
		}
		if dirs := cfg.Agent.Heart.FSWatchDirs; len(dirs) > 0 {
			gw.heart.heart.Register(&heart.FSSource{Dirs: dirs})
		}
		if mc := cfg.Agent.Heart.Mail; mc.Enabled && mc.IMAPHost != "" {
			mailbox := mc.Mailbox
			if mailbox == "" {
				mailbox = "INBOX"
			}
			gw.heart.heart.Register(&heart.MailSource{
				Dial:         heart.IMAPDialer(mc.IMAPHost, mc.IMAPPort, mc.Username, mc.Password, mailbox),
				PollInterval: time.Duration(mc.PollIntervalSeconds) * time.Second,
				State:        gw.heart.store,
				Mailbox:      mailbox,
			})
		}
		if cc := cfg.Agent.Heart.Calendar; cc.Enabled && cc.CalDAVURL != "" {
			gw.heart.heart.Register(&heart.CalendarSource{
				Fetch:        heart.CalDAVFetch(cc.CalDAVURL, cc.Username, cc.Password, cc.CalendarPaths),
				PollInterval: time.Duration(cc.PollIntervalSeconds) * time.Second,
				Lookahead:    time.Duration(cc.LookaheadMinutes) * time.Minute,
			})
		}

		// Sleep can now learn from routing corrections: the synthesize-rules job
		// needs the heart's feedback + event stores, which only exist when the heart
		// is enabled. Registered here (construction, before Start) so no cycle runs.
		gw.sleep.Register(sleep.NewSynthesizeRulesJob(
			feedbackCorrectionSource{feedback: gw.heart.feedback, events: gw.heart.store},
			rulesFileSink{path: attentionRulesPath()},
			eventsCanaryCorpus{db: gw.db.DB},
		))
		slog.Info("heart enabled",
			"heartbeat_interval_minutes", cfg.Agent.Heart.HeartbeatIntervalMinutes,
			"model_router", cfg.Agent.Heart.ModelRouter)
	}

	gw.config.OnReload(func(newCfg *config.Config) {
		// A running heart requires the kernel; never disable episodes out from
		// under it on reload, or routed events would start failing mid-session.
		episodeEnabled := newCfg.Agent.EpisodeEnabled
		if gw.heart != nil && !episodeEnabled {
			slog.Warn("config reload: ignoring episode_enabled=false while heart is running (heart requires the kernel)")
			episodeEnabled = true
		}
		if gw.agent != nil {
			gw.agent.SetModel(newCfg.LLM.Model)
			gw.agent.SetKernel(gw.EpisodeRunner, episodeEnabled)
			gw.agent.EventBus().Publish(agent.ConfigChanged{Path: opt.ConfigPath})
		}
		gw.EpisodeEnabled = episodeEnabled
	})

	gw.subsystems = Subsystems{gw.memory, gw.channels, gw.mcpSub, gw.health, gw.config, gw.scheduler, gw.telemetry}
	if gw.heart != nil {
		gw.subsystems = append(gw.subsystems, gw.heart)
	}
	if gw.admin.enabled {
		gw.subsystems = append(gw.subsystems, gw.admin)
	}
	return gw, nil
}

func (gw *Gateway) Config() *config.Config      { return gw.config.Config() }
func (gw *Gateway) Features() *feature.Registry { return gw.features }

func (gw *Gateway) AddChannel(ch channel.Channel) {
	gw.channels.channels[ch.Name()] = ch
}

// SetSchedulerNotifier wires the scheduler's reply forwarding to a real channel.
func (gw *Gateway) SetSchedulerNotifier(ch channel.Channel) {
	if gw.scheduler != nil {
		gw.scheduler.Channel.SetNotifier(ch)
	}
}

func (gw *Gateway) Start(ctx context.Context) error {
	if err := gw.admin.Start(ctx); err != nil {
		return fmt.Errorf("admin: %w", err)
	}

	gw.health.StartServer(gw.config.Config())

	if gw.config.Config().Tools.MCP.Deferred {
		gw.mcpSub.Manager.SetDeferredCatalog(gw.toolSub.DeferredCatalog)
		slog.Info("mcp: tools routed to deferred catalog (discoverable via tool_search)")
	}
	if len(gw.config.Config().Tools.MCP.Servers) > 0 {
		go gw.mcpSub.StartServers(ctx, gw.config.Config(), gw.toolSub.Registry)
	}
	go gw.mcpSub.WatchDir(ctx, gw.config.Config(), gw.toolSub.Registry)

	if gw.toolSub.ResultStore != nil {
		go func() {
			ticker := time.NewTicker(1 * time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if err := gw.toolSub.ResultStore.Cleanup(); err != nil {
						slog.Warn("gateway: result store cleanup failed", "err", err)
					}
				}
			}
		}()
	}

	if gw.config.Config().Agent.Action.HoldEnabled {
		// Crash recovery: any hold left 'executing' is orphaned from a prior run
		// (nothing is in flight at startup), so reset it to 'pending' before the
		// drain ticker starts. Persisted 'pending' holds then re-drain normally.
		if gw.toolSub != nil && gw.toolSub.ActionStore != nil {
			if n, err := gw.toolSub.ActionStore.RecoverStaleHolds(ctx); err != nil {
				slog.Warn("holds: recover stale failed", "err", err)
			} else if n > 0 {
				slog.Info("holds: recovered stale holds", "count", n)
			}
		}
		go func() {
			ticker := time.NewTicker(holdDrainInterval(gw.config.Config().Agent.Action.HoldDrainIntervalSeconds))
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					gw.drainHolds(ctx)
				}
			}
		}()
	}

	// Route proposal inline-button taps to the decision coordinator BEFORE the
	// channels' update loops start, so a tap can never race a not-yet-registered
	// handler. Channels are already registered (AddChannel runs before Start).
	gw.registerProposalHandler()

	for name, ch := range gw.channels.Channels() {
		if err := ch.Start(ctx, gw.handleInbound); err != nil {
			return err
		}
		slog.Info("channel started", "name", name)
	}

	// Heart runs after channels so its wake_user path can reach them. Its run
	// loop (backlog recovery + sources) lives on this serve ctx and exits at
	// shutdown when ctx is cancelled.
	if gw.heart != nil {
		if err := gw.heart.Start(ctx); err != nil {
			return err
		}
	}

	slog.Info("gateway started")
	return nil
}

func (gw *Gateway) Stop(ctx context.Context) error {
	gw.subsystems.StopAll(ctx)
	if gw.toolSub != nil {
		// toolSub is not in the subsystems list, so stop its background
		// codebase-indexing goroutine explicitly.
		_ = gw.toolSub.Stop(ctx)
	}
	if gw.mcpSub.Manager != nil {
		_ = gw.mcpSub.Manager.Close()
	}
	gw.stopOnce.Do(func() { close(gw.stopCh) })
	if gw.initCancel != nil {
		gw.initCancel()
	}
	_ = gw.db.Close()
	slog.Info("gateway stopped")
	return nil
}

func (gw *Gateway) handleInbound(ctx context.Context, msg channel.InboundMessage) {
	if msg.Text == "" {
		return
	}
	ch, ok := gw.channels.Channels()[msg.Channel]
	if !ok {
		slog.Error("unknown channel", "channel", msg.Channel)
		return
	}

	if sw, ok := ch.(channel.ToolStreamWriter); ok {
		target := channel.MessageTarget{Channel: msg.Channel, ChannelID: msg.ChannelID}
		ctx = tool.WithStreamCallback(ctx, func(chunk string) {
			if err := sw.WriteToolStream(ctx, target, "bash", chunk); err != nil {
				slog.Warn("gateway: tool stream write failed", "err", err)
			}
		})
	}

	if gw.commands != nil {
		if resp, handled := gw.commands.Dispatch(ctx, ch, msg); handled {
			if resp != "" {
				_ = ch.Send(ctx, channel.OutboundMessage{Channel: msg.Channel, ChannelID: msg.ChannelID, Text: resp})
			}
			return
		}
	}

	slog.Info("message received", "channel", msg.Channel, "user", msg.UserName, "text_len", len(msg.Text))
	if msg.Channel == "scheduler" {
		gw.publishTaskTransition(msg.ChannelID, "scheduled", "", "running", "scheduler message handling started")
	}

	// Chat ingress through the heart (when enabled): record the message in the
	// unified event stream for audit + idempotent dedup. A redelivered message
	// (same channel-native id) is skipped so the turn is not handled twice. A
	// recording error is best-effort — never drop the user's turn over an audit
	// failure, so fall through to normal handling. Disabled ⇒ inserted=true.
	if inserted, recErr := gw.heart.RecordChatEvent(ctx, msg); recErr != nil {
		slog.Warn("gateway: record chat event failed; handling anyway", "channel", msg.Channel, "err", recErr)
	} else if !inserted {
		slog.Info("gateway: skipping redelivered chat message", "channel", msg.Channel, "message_id", msg.MessageID)
		return
	}

	err := gw.agent.HandleMessage(ctx, ch, msg)
	if err != nil {
		slog.Error("agent error", "err", err)
		_ = ch.Send(ctx, channel.OutboundMessage{Channel: msg.Channel, ChannelID: msg.ChannelID, Text: "Error: " + err.Error()})
	}
	gw.finishInbound(ctx, msg, err)
}

func (gw *Gateway) finishInbound(ctx context.Context, msg channel.InboundMessage, err error) {
	result := gw.saveTaskCheckpoint(ctx, msg)
	if msg.Channel == "scheduler" && gw.scheduler != nil && gw.scheduler.Channel != nil {
		gw.scheduler.Channel.FinishRun(ctx, msg.ChannelID, err, result)
		toState := "succeeded"
		if err != nil {
			toState = "failed"
		}
		gw.publishTaskTransition(msg.ChannelID, "scheduled", "running", toState, "scheduler message handling completed")
	}
}

func (gw *Gateway) publishTaskTransition(taskID, kind, from, to, reason string) {
	if gw == nil || gw.agent == nil || taskID == "" {
		return
	}
	gw.agent.EventBus().Publish(agent.TaskTransitioned{
		TaskID:    taskID,
		Kind:      kind,
		FromState: from,
		ToState:   to,
		Reason:    reason,
	})
}

func (gw *Gateway) handleApproval(ctx context.Context, ch channel.Channel, target channel.MessageTarget, toolName, input string) (bool, error) {
	if ch == nil {
		// No interactive channel (autonomous internal episode): we cannot get a
		// human sign-off, so deny tools that require approval. Only auto-approved
		// / read-only tools run autonomously. Routing autonomous approvals to
		// Telegram is a later increment.
		return false, nil
	}
	if sender, ok := ch.(channel.ApprovalSender); ok {
		return sender.SendApprovalRequest(ctx, target, toolName, input)
	}
	return true, nil
}

// costRecorderAdapter bridges the episode kernel's CostRecorder to the economy
// store, stamping each row with the current time at this boundary so the store
// stays clock-free. Token counts flow straight through from the per-call Usage
// the episode accumulated.
type costRecorderAdapter struct {
	store *economy.Store
	now   func() int64
}

func (a costRecorderAdapter) RecordEpisodeCost(_ context.Context, c episode.EpisodeCost) error {
	entry := economy.Entry{
		EpisodeID:           c.EpisodeID,
		Model:               c.Model,
		Provider:            c.Provider,
		ActivityClass:       c.ActivityClass,
		InputTokens:         c.Usage.InputTokens,
		OutputTokens:        c.Usage.OutputTokens,
		CacheReadTokens:     c.Usage.CacheReadTokens,
		CacheCreationTokens: c.Usage.CacheCreationTokens,
		OccurredAt:          a.now(),
	}
	// Persist off the episode's return path: cost recording is observational, so a
	// slow or write-locked DB must never delay (or, via panic, disturb) the episode.
	// A fresh bounded context detaches the write from the episode's lifecycle so a
	// cancelled episode still records the cost it incurred; a crash before the write
	// lands just drops that row (best-effort ledger). Record is idempotent per
	// episode, so a retried episode does not double-count.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Warn("economy: cost record panicked (ignored)", "episode", entry.EpisodeID, "panic", r)
			}
		}()
		wctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.store.Record(wctx, entry); err != nil {
			slog.Warn("economy: record cost failed", "episode", entry.EpisodeID, "err", err)
		}
	}()
	return nil
}

// routeCostAdapter bridges the attention model router's cost sink to the
// economy store. Routing has no episode id, so each row gets the store's random
// id fallback and uses a fixed class for cost-share reporting.
type routeCostAdapter struct {
	store    *economy.Store
	provider string
	now      func() int64
}

func (a routeCostAdapter) RecordRouteCost(_ context.Context, model string, usage mind.Usage) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("economy: route cost record panicked (ignored)", "model", model, "panic", r)
		}
	}()
	now := a.now
	if now == nil {
		now = func() int64 { return time.Now().Unix() }
	}
	entry := economy.Entry{
		Model:               model,
		Provider:            a.provider,
		ActivityClass:       "routing",
		InputTokens:         usage.InputTokens,
		OutputTokens:        usage.OutputTokens,
		CacheReadTokens:     usage.CacheReadTokens,
		CacheCreationTokens: usage.CacheCreationTokens,
		OccurredAt:          now(),
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Warn("economy: route cost record panicked (ignored)", "model", entry.Model, "panic", r)
			}
		}()
		wctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.store.Record(wctx, entry); err != nil {
			slog.Warn("economy: record route cost failed", "model", entry.Model, "err", err)
		}
	}()
}

type completerAdapter struct {
	provider mind.Provider
	model    string
	// maxTokens caps the response; 0 falls back to completerDefaultMaxTokens so
	// existing callers keep their prior behavior.
	maxTokens int
}

const completerDefaultMaxTokens = 512

func (a *completerAdapter) Complete(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	maxTokens := a.maxTokens
	if maxTokens <= 0 {
		maxTokens = completerDefaultMaxTokens
	}
	req := mind.CompletionRequest{
		Model: a.model, System: systemPrompt,
		Messages:  []mind.CompletionMessage{{Role: "user", Content: userMessage}},
		MaxTokens: maxTokens,
	}
	resp, err := a.provider.Complete(ctx, req)
	if err != nil {
		return "", err
	}
	if resp.StopReason == mind.StopMaxToken {
		slog.Warn("completerAdapter: response truncated at max_tokens",
			"model", a.model, "max_tokens", maxTokens)
	}
	return resp.Text, nil
}

func defaultSkillsDir() string {
	return filepath.Join(appdir.BaseDir(), "skills")
}

func defToSpec(def config.AgentDefinition) *agent.AgentSpec {
	return &agent.AgentSpec{
		Name: def.Name, Description: def.Description, SystemPrompt: def.SystemPrompt,
		Model: def.Model, MaxTokens: def.MaxTokens, MaxIterations: def.MaxIterations,
		Tools: def.Tools, Tags: def.Tags, Mode: def.Mode,
	}
}
