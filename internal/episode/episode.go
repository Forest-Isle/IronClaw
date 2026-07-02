package episode

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Forest-Isle/daimon/internal/agent"
	"github.com/Forest-Isle/daimon/internal/mind"
	"github.com/Forest-Isle/daimon/internal/tool"
	"github.com/Forest-Isle/daimon/internal/world"
)

const (
	episodeCloseToolName = "episode_close"
	defaultMaxIterations = 20
)

// Outcome is the structured exit contract a model declares via episode_close.
type Outcome struct {
	Status       string           `json:"status"`
	Summary      string           `json:"summary"`
	WorldWrites  []world.Mutation `json:"world_writes,omitempty"`
	Receipts     []string         `json:"receipts,omitempty"`
	FollowUps    []FollowUp       `json:"follow_ups,omitempty"`
	OpenQuestion *string          `json:"open_question,omitempty"`
	// ValueCreatedUSD is an optional self-reported USD value created or saved.
	ValueCreatedUSD float64 `json:"value_created_usd,omitempty"`
	// Salvaged is set by the framework (not the model) when the Outcome was
	// recovered because episode_close was never called. It feeds the salvaged-rate
	// metric and is recorded in the journal detail.
	Salvaged bool `json:"-"`
	// AutoClosed is set by the framework when a no-tool conversational turn ended
	// without an episode_close call and the framework closed it implicitly. Unlike
	// Salvaged it is a clean delivered outcome (it counts toward value/ROI), but it
	// is excluded from distill candidacy: a pure chat turn is not a repeatable task.
	AutoClosed bool `json:"-"`
}

// FollowUp describes future work to plant after an episode closes. Kind is one of
// timer (re-enter at a later time), watch, or check.
type FollowUp struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
	Goal   string `json:"goal"`
}

// FollowUpPlanter schedules a timer follow-up for autonomous re-entry. The
// gateway provides the implementation (backed by the event heart); a nil planter
// drops timer follow-ups with a warning. watch/check follow-ups never reach the
// planter — they persist transactionally as commitments alongside the outcome.
type FollowUpPlanter interface {
	Plant(ctx context.Context, episodeID string, f FollowUp) error
}

// Runner is the cognitive kernel: it composes context from the world model,
// runs a bare ReAct loop, and enforces the episode_close exit contract. Tool
// execution and replay recording are delegated to the agent runtime via the
// CognitiveRequest, so episodes share the same governance as the legacy path.
type Runner struct {
	provider mind.Provider
	world    *world.Store
	identity *world.Identity
	bus      agent.EventBus
	planter  FollowUpPlanter
	values   valueDigester
	cost     CostRecorder
}

// CostRecorder persists one episode's token consumption to the economy ledger
// (blueprint §4.11). Optional: a nil recorder (the default) disables cost
// accounting, leaving the binary behaviorally unchanged.
type CostRecorder interface {
	RecordEpisodeCost(ctx context.Context, c EpisodeCost) error
}

// EpisodeCost is the token consumption of one episode, summed across all of its
// provider calls. Usage may under-count for a streamed OpenAI backend (which does
// not report per-call streaming usage); the active Anthropic provider and the
// non-streamed Complete path report it fully.
type EpisodeCost struct {
	EpisodeID     string
	Model         string
	Provider      string
	ActivityClass string
	Usage         mind.Usage
}

// NewRunner builds a cognitive kernel. bus may be nil (events are then skipped).
func NewRunner(p mind.Provider, ws *world.Store, id *world.Identity, bus agent.EventBus) *Runner {
	return &Runner{provider: p, world: ws, identity: id, bus: bus}
}

// SetPlanter wires the timer follow-up planter. Optional: a nil planter (the
// default) means timer follow-ups are dropped with a warning, leaving the binary
// behaviorally unchanged when the event heart is disabled.
func (r *Runner) SetPlanter(p FollowUpPlanter) { r.planter = p }

// SetValues wires the value digester whose high-confidence entries are injected
// into the episode system prompt. Optional: a nil digester omits the section.
func (r *Runner) SetValues(v valueDigester) { r.values = v }

// SetCostRecorder wires the economy cost ledger. Optional: a nil recorder (the
// default) disables per-episode cost accounting with no behavior change.
func (r *Runner) SetCostRecorder(c CostRecorder) { r.cost = c }

// recordCost reports the tokens this episode consumed to the economy ledger. It
// is strictly observational and must never affect the episode: a nil recorder,
// an episode that consumed nothing, a recorder error, or even a recorder panic
// all leave the outcome untouched. The recorder is expected to return promptly
// (the production adapter persists asynchronously) so this deferred call does not
// delay Execute's return; the panic guard is defense-in-depth in case a recorder
// misbehaves synchronously — it runs before the episode's recover defer, so an
// uncontained panic here could otherwise corrupt the outcome.
func (r *Runner) recordCost(ctx context.Context, req agent.CognitiveRequest, episodeID string, used *mind.Usage) {
	// Skip when no positive tokens were counted: a zero (or, defensively, a
	// nonpositive) total means "unknown / nothing consumed", not a real $0 episode.
	if r.cost == nil || used == nil ||
		used.InputTokens+used.OutputTokens+used.CacheReadTokens+used.CacheCreationTokens <= 0 {
		return
	}
	defer func() {
		if rec := recover(); rec != nil {
			slog.Warn("episode: cost recorder panicked (ignored)", "episode", episodeID, "panic", rec)
		}
	}()
	if err := r.cost.RecordEpisodeCost(ctx, EpisodeCost{
		EpisodeID:     episodeID,
		Model:         req.Model,
		Provider:      req.Provider,
		ActivityClass: req.ActivityClass,
		Usage:         *used,
	}); err != nil {
		slog.Warn("episode: record cost failed", "episode", episodeID, "err", err)
	}
}

// Execute implements agent.CognitiveKernel.
func (r *Runner) Execute(ctx context.Context, req agent.CognitiveRequest) (result agent.CognitiveOutcome, err error) {
	if r == nil || r.provider == nil {
		return agent.CognitiveOutcome{Status: "failed", Summary: "episode provider unavailable"}, errors.New("episode provider unavailable")
	}

	// A caller-supplied EpisodeID is a deterministic idempotency key (the trigger
	// event id). If its outcome already committed, this is a re-delivery after a
	// crash that happened before the event was marked routed — skip rather than
	// re-run. A check error is non-fatal: fall through and run (at-least-once
	// bias), since the outcome write itself is idempotent.
	episodeID := req.EpisodeID
	if episodeID == "" {
		episodeID = newEpisodeID()
	} else if r.world != nil {
		if done, err := r.world.OutcomeExists(ctx, episodeID); err != nil {
			slog.Warn("episode: outcome-exists check failed; running anyway", "episode_id", episodeID, "err", err)
		} else if done {
			return agent.CognitiveOutcome{Status: "done", Summary: "already handled (idempotent replay skip)"}, nil
		}
	}

	// Invariant #3 (交账强制): a panic anywhere below — most plausibly a tool
	// dispatch (req.Invoke) that panics or is nil — must still leave a durable
	// journal trace and surface as an error, never let the episode vanish. Installed
	// after the idempotent-skip return so a recovered run always has a real
	// episodeID to account under.
	defer func() {
		if rec := recover(); rec != nil {
			result = r.failEpisode(ctx, req, episodeID, fmt.Sprintf("episode panic: %v", rec))
			err = fmt.Errorf("episode panic: %v", rec)
		}
	}()

	// Accumulate the tokens consumed across every provider call in this episode and
	// record one cost row on whichever path the episode exits (normal close, salvage,
	// stream error, or panic) — the §4.11 economy ledger. Installed after the
	// idempotent-skip return so a skipped re-delivery (which makes no provider call)
	// records nothing; recording is best-effort and never affects the outcome.
	var used mind.Usage
	defer r.recordCost(ctx, req, episodeID, &used)

	// Install a per-episode action-verification collector in the context so the
	// action interceptor reports each governed action's verified status into it.
	// Read once at close to derive the unverified-actions signal (§4.8 distill
	// candidacy). Observational: the collector never affects the episode.
	actions := &tool.ActionVerification{}
	ctx = tool.WithActionCollector(ctx, actions)

	// Expose this episode's id to the work it dispatches: a sub-agent Spawn made
	// from a tool call reads it (agent.EpisodeIDFromCtx) to link the child episode
	// back to this parent (§4.3 parent linkage). Installed after episodeID is final.
	ctx = agent.EpisodeIDToCtx(ctx, episodeID)

	system := composeSystem(ctx, req, r.world, r.identity, r.values)
	messages := append([]mind.CompletionMessage(nil), req.Transcript...)
	if len(messages) == 0 {
		messages = []mind.CompletionMessage{{Role: "user", Content: req.Trigger}}
	}
	toolDefs := append(append([]mind.ToolDefinition(nil), req.ToolDefs...), episodeCloseToolDefinition())

	var transcript []transcriptTurn
	var lastReply string
	var toolFailures int // non-close tool calls that returned an error (clean-execution signal)
	closeReminderSent := false
	toolInvoked := false // any non-close tool dispatched this episode (gates implicit close)

	for iteration := 0; iteration < defaultMaxIterations; iteration++ {
		creq := mind.CompletionRequest{
			Model:     req.Model,
			System:    system,
			Messages:  messages,
			Tools:     toolDefs,
			MaxTokens: req.MaxTokens,
		}
		start := time.Now()
		fullText, toolCalls, stopReason, callUsage, streamErr := streamCompletion(ctx, r.provider, creq)
		used.Add(callUsage)
		r.publishExchange(req, iteration, system, messages, fullText, toolCalls, stopReason, time.Since(start).Milliseconds())
		if streamErr != nil {
			// Invariant #3 (交账强制): an episode that started must leave a durable
			// Outcome even when the provider errors mid-run, so a crashed/failed
			// episode is accounted for in the world rather than vanishing.
			return r.failEpisode(ctx, req, episodeID, "episode stream error: "+streamErr.Error()), streamErr
		}

		// When the close reminder was already sent, this turn exists only because
		// the model gave a complete answer without calling episode_close; its text
		// is a throwaway acknowledgement ("ok, closing now") that must not clobber
		// the substantive reply captured before the reminder. A normal multi-step
		// episode never sets closeReminderSent (every turn calls a tool), so the
		// final answer it produces alongside episode_close is still adopted.
		if fullText != "" && !(closeReminderSent && lastReply != "") {
			lastReply = fullText
		}
		if fullText != "" || len(toolCalls) > 0 {
			messages = append(messages, mind.CompletionMessage{Role: "assistant", Content: fullText, ToolBlocks: toolCalls})
			transcript = append(transcript, transcriptTurn{Role: "assistant", Content: fullText})
		}

		if len(toolCalls) == 0 {
			// Implicit close: a no-tool turn whose text reads as a finished answer is a
			// pure conversational reply. Forcing a second main-model round-trip only to
			// obtain an episode_close call wastes a full turn (and provokes throwaway
			// filler), so synthesize the Outcome from the reply instead. Gated three ways:
			// (1) reminder not yet sent; (2) no tool used this episode — any tool use
			// means the model may have world writes/value to declare, so the strict
			// contract is kept; (3) status reads as "done" — a blocked/handed_off reply
			// falls through to the reminder so the model can declare follow_ups/an
			// open question (and is salvaged if it still never closes).
			if !closeReminderSent && !toolInvoked && inferStatus(lastReply) == "done" {
				return r.close(ctx, req, episodeID, autoCloseOutcome(lastReply), lastReply, toolFailures, unverifiedActionCount(actions)), nil
			}
			if iteration == defaultMaxIterations-1 || closeReminderSent {
				break
			}
			reminder := "You must call `episode_close` with a complete Outcome before finishing. Until you call it, the system will treat your work as incomplete."
			messages = append(messages, mind.CompletionMessage{Role: "user", Content: reminder})
			closeReminderSent = true
			continue
		}

		for _, tc := range toolCalls {
			if tc.Name == episodeCloseToolName {
				out, perr := parseOutcome(tc.Input)
				if perr != nil {
					rejection := "episode_close rejected: " + perr.Error()
					messages = append(messages, mind.CompletionMessage{Role: "user", Content: rejection, ToolUseID: tc.ID})
					transcript = append(transcript, transcriptTurn{Role: "tool", Content: rejection})
					continue
				}
				return r.close(ctx, req, episodeID, out, lastReply, toolFailures, unverifiedActionCount(actions)), nil
			}

			toolInvoked = true
			output, isErr := req.Invoke(ctx, iteration, tc)
			if isErr {
				toolFailures++
			}
			messages = append(messages, mind.CompletionMessage{Role: "user", Content: output, ToolUseID: tc.ID})
			transcript = append(transcript, transcriptTurn{Role: "tool", Content: output})
		}

		if iteration == defaultMaxIterations-1 {
			break
		}
	}

	out := r.salvage(ctx, req, system, messages, transcript, &used)
	return r.close(ctx, req, episodeID, out, lastReply, toolFailures, unverifiedActionCount(actions)), nil
}

// unverifiedActionCount is how many of the episode's governed action calls were
// not verified this run (governed minus verified). Zero means every governed
// action the episode took earned objective trust (or it took none).
func unverifiedActionCount(a *tool.ActionVerification) int {
	governed, verified := a.Snapshot()
	return governed - verified
}

// close applies the outcome's world writes plus a journal entry, plants
// follow-ups, records the turn-close event, and shapes the user-facing reply.
func (r *Runner) close(ctx context.Context, req agent.CognitiveRequest, episodeID string, out Outcome, lastReply string, toolFailures, unverifiedActions int) agent.CognitiveOutcome {
	// Expand follow-ups: watch/check persist as commitments transactionally with
	// the outcome; timer follow-ups are planted into the heart for re-entry. Kind
	// is normalized so casing/whitespace variants are not silently dropped.
	var timers []FollowUp
	for _, f := range out.FollowUps {
		f.Kind = strings.ToLower(strings.TrimSpace(f.Kind))
		switch f.Kind {
		case "timer":
			timers = append(timers, f)
		case "watch", "check":
			if mut, ok := followUpCommitment(f); ok {
				out.WorldWrites = append(out.WorldWrites, mut)
			}
		default:
			slog.Warn("episode: dropping follow-up with unknown kind", "episode", episodeID, "kind", f.Kind)
		}
	}

	if r.world != nil {
		if err := r.world.ApplyOutcome(ctx, episodeID, out.WorldWrites, out.Summary, world.OutcomeMeta{Salvaged: out.Salvaged, AutoClosed: out.AutoClosed, ToolFailures: toolFailures, UnverifiedActions: unverifiedActions, ParentEpisodeID: req.ParentEpisodeID, ValueCreatedUSD: out.ValueCreatedUSD}); err != nil {
			// Invariant #3 (交账强制): a malformed WorldWrite must not roll back the
			// episode's journal trace along with it. failEpisode re-applies the
			// outcome with no writes (idempotent), so the summary still lands and the
			// episode is accounted for rather than vanishing. The failure marker goes
			// FIRST so failEpisode's 500-char truncation can never drop it (a long
			// model summary must still be recognizable as a failed outcome downstream,
			// e.g. by the distiller).
			return r.failEpisode(ctx, req, episodeID, "[world write failed: "+err.Error()+"] "+out.Summary)
		}
	}

	// Timer follow-ups are planted best-effort after the outcome commits: the
	// planter writes to the heart's follow-up queue (a separate store), so it
	// cannot share the world transaction. A failure here loses only the re-entry
	// convenience — progress is already durable in the committed outcome and any
	// handed_off commitment, which sleep/selfops can detect. Logged at Error so the
	// loss is visible. A transactional outbox is a P1 follow-up.
	for _, f := range timers {
		if r.planter == nil {
			slog.Warn("episode: timer follow-up dropped (no planter)", "episode", episodeID, "goal", f.Goal)
			continue
		}
		if err := r.planter.Plant(ctx, episodeID, f); err != nil {
			slog.Error("episode: plant timer follow-up failed", "episode", episodeID, "goal", f.Goal, "err", err)
		}
	}

	if out.Salvaged {
		r.publishSalvaged(req.SessionID, episodeID)
	}

	reply := strings.TrimSpace(lastReply)
	if reply == "" {
		reply = out.Summary
	}
	r.publishTurnClosed(req.SessionID, reply)
	return agent.CognitiveOutcome{Status: out.Status, Reply: reply, Summary: out.Summary}
}

// followUpCommitment turns a watch/check follow-up into a commitment.create
// mutation so it persists transactionally with the outcome. Returns ok=false when
// there is nothing to record.
func followUpCommitment(f FollowUp) (world.Mutation, bool) {
	kind := "watch"
	if f.Kind == "check" {
		kind = "routine"
	}
	title := strings.TrimSpace(f.Detail)
	if title == "" {
		title = strings.TrimSpace(f.Goal)
	}
	if title == "" {
		return world.Mutation{}, false
	}
	body, err := json.Marshal(world.Commitment{
		Kind:  kind,
		Title: truncateRunes(title, 200),
		Body:  strings.TrimSpace(f.Goal),
		State: "active",
	})
	if err != nil {
		return world.Mutation{}, false
	}
	return world.Mutation{Op: "commitment.create", Body: body}, true
}

// failEpisode records a durable blocked Outcome for an episode that started but
// could not complete (a provider/stream error), then returns the failed result.
// This honors invariant #3 (every episode leaves an Outcome): without it, a
// mid-run provider error would end the episode with no journal trace. The outcome
// marker is idempotent (ApplyOutcome claims journal_outcome_<episodeID>), so a
// re-delivery does not double-record. A write failure here is logged, not fatal —
// the caller still learns the episode failed.
func (r *Runner) failEpisode(ctx context.Context, req agent.CognitiveRequest, episodeID, summary string) agent.CognitiveOutcome {
	summary = truncateRunes(strings.TrimSpace(summary), 500)
	if r.world != nil {
		// A framework-failed episode is already excluded downstream by its summary
		// marker (world.ClassifyOutcome → OutcomeFailed); leave meta empty rather than
		// ascribe tool failures, since it failed for a framework reason
		// (stream/panic/world write).
		if err := r.world.ApplyOutcome(ctx, episodeID, nil, summary, world.OutcomeMeta{ParentEpisodeID: req.ParentEpisodeID}); err != nil {
			slog.Error("episode: record blocked outcome failed", "episode", episodeID, "err", err)
		}
	}
	r.publishTurnClosed(req.SessionID, "")
	return agent.CognitiveOutcome{Status: "failed", Summary: summary}
}

func streamCompletion(ctx context.Context, p mind.Provider, req mind.CompletionRequest) (string, []mind.ToolUseBlock, string, mind.Usage, error) {
	stream, err := p.Stream(ctx, req)
	if err != nil {
		return "", nil, "", mind.Usage{}, err
	}
	defer stream.Close()

	var fullText strings.Builder
	var toolCalls []mind.ToolUseBlock
	stopReason := ""
	for {
		delta, err := stream.Next()
		if err != nil {
			return fullText.String(), toolCalls, stopReason, mind.Usage{}, err
		}
		if delta.Text != "" {
			fullText.WriteString(delta.Text)
		}
		if delta.ToolCall != nil {
			toolCalls = append(toolCalls, *delta.ToolCall)
		}
		if delta.Done {
			if len(delta.ToolCalls) > 0 {
				toolCalls = delta.ToolCalls
			}
			stopReason = string(delta.StopReason)
			// Usage is set only on the final delta; zero when the provider did not
			// report it (e.g. streamed OpenAI, which finalizes without draining).
			return fullText.String(), toolCalls, stopReason, delta.Usage, nil
		}
	}
}

func parseOutcome(raw string) (Outcome, error) {
	var out Outcome
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return Outcome{}, fmt.Errorf("decode outcome: %w", err)
	}
	// Invariant #3 (交账强制): the Outcome is schema-validated, not merely
	// non-empty. Status must be one of the declared enum values (the episode_close
	// tool schema advertises the same set); an out-of-enum status is rejected so
	// the model retries (the salvage path falls back to a heuristic Outcome).
	status := strings.TrimSpace(out.Status)
	switch status {
	case "done", "blocked", "handed_off":
		out.Status = status
	default:
		return Outcome{}, fmt.Errorf("status %q must be one of done, blocked, handed_off", out.Status)
	}
	// Summary is a required schema field (it is what lands in the journal); a blank
	// one is rejected so the model retries with a real account rather than recording
	// an empty Outcome. The salvage path constructs its Outcome directly and does not
	// pass through here, so this cannot wedge a salvaged episode.
	if strings.TrimSpace(out.Summary) == "" {
		return Outcome{}, fmt.Errorf("summary must not be empty")
	}
	if utf8.RuneCountInString(out.Summary) > 500 {
		return Outcome{}, fmt.Errorf("summary is %d chars, max 500", utf8.RuneCountInString(out.Summary))
	}
	if out.ValueCreatedUSD < 0 {
		return Outcome{}, fmt.Errorf("value_created_usd must not be negative")
	}
	return out, nil
}

type transcriptTurn struct {
	Role    string
	Content string
}

// salvage recovers an Outcome when the model never called episode_close: it
// first asks the provider for a JSON-only extraction, then falls back to a
// transcript heuristic.
func (r *Runner) salvage(ctx context.Context, req agent.CognitiveRequest, system string, messages []mind.CompletionMessage, transcript []transcriptTurn, used *mind.Usage) Outcome {
	prompt := "Extract a compact JSON episode Outcome from the transcript. Return only JSON with fields: status, summary, world_writes, receipts, follow_ups, open_question. Status must be one of done, blocked, handed_off. Summary must be <=500 chars."
	salvageMessages := append(append([]mind.CompletionMessage(nil), messages...), mind.CompletionMessage{Role: "user", Content: prompt})
	resp, err := r.provider.Complete(ctx, mind.CompletionRequest{
		Model:          req.Model,
		System:         system,
		Messages:       salvageMessages,
		MaxTokens:      1024,
		ToolChoice:     "none",
		ResponseFormat: &mind.ResponseFormat{Type: "json_object"},
	})
	if resp != nil && used != nil {
		used.Add(resp.Usage)
	}
	if err == nil && resp != nil {
		if out, perr := parseOutcome(resp.Text); perr == nil {
			out.Salvaged = true
			return out
		}
	}
	out := inferOutcomeFromTranscript(req.Goal, transcript)
	out.Salvaged = true
	return out
}

// Status signals match on word boundaries so a substring inside an unrelated word
// does not trigger ("awaiting" is not "waiting", "needed" is not a blocked signal).
// The vocabulary is deliberately high-precision incapacity language; bare "need"
// was dropped because it is common in finished answers ("you need to run make").
var (
	blockedSignal = regexp.MustCompile(`(?i)\b(blocked|waiting|stuck|unable)\b`)
	handoffSignal = regexp.MustCompile(`(?i)\bhand(ed|ing)?[ -]?off\b`)
)

// inferStatus maps free text to an episode status via the keyword heuristic the
// transcript salvage relies on: empty or an incapacity signal → blocked, a handoff
// mention → handed_off, otherwise done. It is the shared, conservative reading used
// both to salvage a non-compliant episode and to gate implicit close (only a "done"
// reply is auto-closed; anything else takes the reminder path). The heuristic is
// English-only — a non-English blocked reply may read as done and auto-close.
func inferStatus(text string) string {
	switch {
	case strings.TrimSpace(text) == "":
		return "blocked"
	case blockedSignal.MatchString(text):
		return "blocked"
	case handoffSignal.MatchString(text):
		return "handed_off"
	default:
		return "done"
	}
}

// autoCloseOutcome synthesizes a clean Outcome for a no-tool conversational turn
// the model ended without calling episode_close. The reply is the summary: a turn
// that called no tool declares no world writes, so there is nothing richer to
// capture. Marked AutoClosed (not Salvaged) so it counts as delivered value yet is
// kept out of distillation.
func autoCloseOutcome(reply string) Outcome {
	return Outcome{
		Status:     "done",
		Summary:    truncateRunes(compactWhitespace(reply), 500),
		AutoClosed: true,
	}
}

func inferOutcomeFromTranscript(goal string, transcript []transcriptTurn) Outcome {
	var lines []string
	for _, turn := range transcript {
		if strings.TrimSpace(turn.Content) == "" {
			continue
		}
		lines = append(lines, turn.Role+": "+strings.TrimSpace(turn.Content))
	}
	text := strings.Join(lines, "\n")

	status := inferStatus(text)

	summary := strings.TrimSpace(text)
	if summary == "" {
		summary = "Episode ended without a compliant episode_close call."
	}
	if goal != "" && !strings.Contains(summary, goal) {
		summary = "Goal: " + goal + "\n" + summary
	}
	return Outcome{Status: status, Summary: truncateRunes(compactWhitespace(summary), 500)}
}

func truncateRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

func compactWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func (r *Runner) publishExchange(req agent.CognitiveRequest, iteration int, system string, messages []mind.CompletionMessage, responseText string, toolCalls []mind.ToolUseBlock, stopReason string, durationMs int64) {
	if r.bus == nil {
		return
	}
	r.bus.Publish(agent.ProviderExchange{
		SessionID:     req.SessionID,
		Iteration:     iteration,
		Model:         req.Model,
		Provider:      req.Provider,
		SystemPrompt:  system,
		MessagesJSON:  marshalRaw(messages),
		ResponseText:  responseText,
		ToolCallsJSON: marshalRaw(toolCalls),
		StopReason:    stopReason,
		DurationMs:    durationMs,
	})
}

func (r *Runner) publishTurnClosed(sessionID, reply string) {
	if r.bus == nil {
		return
	}
	r.bus.Publish(agent.TurnClosed{SessionID: sessionID, FinalReply: reply})
}

func (r *Runner) publishSalvaged(sessionID, episodeID string) {
	if r.bus == nil {
		return
	}
	r.bus.Publish(agent.EpisodeSalvaged{SessionID: sessionID, EpisodeID: episodeID})
}

func marshalRaw(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return data
}

// newEpisodeID returns a lexicographically sortable, time-prefixed identifier.
func newEpisodeID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	ms := uint64(time.Now().UnixMilli())
	return fmt.Sprintf("ep_%012x%x", ms&0xffffffffffff, binary.BigEndian.Uint64(b[:8]))
}

func episodeCloseToolDefinition() mind.ToolDefinition {
	return mind.ToolDefinition{
		Name:        episodeCloseToolName,
		Description: "Close the current episode with the complete structured Outcome. This is mandatory before finishing.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status": map[string]any{
					"type":        "string",
					"enum":        []string{"done", "blocked", "handed_off"},
					"description": "Episode result status: done, blocked, or handed_off.",
				},
				"summary": map[string]any{
					"type":        "string",
					"description": "Concise journal summary of what happened, 500 characters or fewer.",
				},
				"world_writes": map[string]any{
					"type":        "array",
					"description": "World model mutations to persist from this episode.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"op":     map[string]any{"type": "string", "enum": []string{"commitment.create", "commitment.update", "journal.append", "fact.upsert"}, "description": "Mutation op. Use fact.upsert to record a durable, retrievable fact (provide body.summary; optional body.id replaces a prior fact)."},
							"target": map[string]any{"type": "string", "description": "Optional mutation target ID."},
							"body":   map[string]any{"type": "object", "description": "Mutation body."},
						},
					},
				},
				"receipts": map[string]any{
					"type":        "array",
					"description": "Episode-level action receipt IDs produced by tools.",
					"items":       map[string]any{"type": "string"},
				},
				"follow_ups": map[string]any{
					"type":        "array",
					"description": "Timers, watches, or checks to create after this episode.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"kind":   map[string]any{"type": "string", "enum": []string{"timer", "watch", "check"}},
							"detail": map[string]any{"type": "string", "description": "Timer interval, watch target, or check detail."},
							"goal":   map[string]any{"type": "string", "description": "Goal for the follow-up episode."},
						},
					},
				},
				"open_question": map[string]any{
					"type":        "string",
					"description": "If blocked, the exact question or missing input needed from the user.",
				},
				"value_created_usd": map[string]any{
					"type":        "number",
					"description": "Optional: estimated USD value or savings this episode created for the user (e.g. money saved, billable time avoided). Omit or 0 if none or unknown. Be conservative and honest.",
				},
			},
			"required": []string{"status", "summary"},
		},
	}
}
