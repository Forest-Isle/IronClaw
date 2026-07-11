package tui

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Forest-Isle/daimon/internal/channel"
	tea "github.com/charmbracelet/bubbletea"
)

// Adapter implements channel.Channel, channel.ApprovalSender,
// channel.NotificationSender, and channel.FeedbackSender for an
// interactive terminal UI.
type Adapter struct {
	program      *tea.Program
	handler      channel.InboundHandler
	model        *Model
	stopCh       chan struct{}
	stopOnce     sync.Once
	version      string
	channelID    string // unique per launch so each TUI invocation gets a fresh session
	autoApprove  atomic.Bool
	argCompleter ArgCompleter // optional dynamic argument completer

	// approvalTimeout is the max time to wait for the user to respond
	// to an approval or reflection request.
	approvalTimeout time.Duration

	// userInputCh receives text messages typed by the user.
	// The Model writes here on Enter; the adapter's goroutine
	// converts them to InboundMessages for the gateway.
	userInputCh chan string

	// cancelMu protects cancelFn for concurrent access.
	cancelMu  sync.Mutex
	cancelFn  context.CancelFunc // cancels the in-flight agent request
	cancelGen uint64             // generation counter for cancel ownership
}

// New creates a new TUI adapter. Each invocation generates a unique channelID
// so the session manager creates a fresh session per TUI launch.
func New(version string) *Adapter {
	return &Adapter{
		version:         version,
		channelID:       fmt.Sprintf("tui_%d", time.Now().UnixNano()),
		stopCh:          make(chan struct{}),
		userInputCh:     make(chan string, 16),
		approvalTimeout: 120 * time.Second,
	}
}

// SetApprovalTimeout overrides the default approval timeout.
func (a *Adapter) SetApprovalTimeout(d time.Duration) {
	if d > 0 {
		a.approvalTimeout = d
	}
}

// SetAutoApprove disables interactive approval prompts.
func (a *Adapter) SetAutoApprove(v bool) {
	a.autoApprove.Store(v)
}

// SetModelRoles forwards model role config to the TUI model.
func (a *Adapter) SetModelRoles(provider, opus, sonnet, haiku string) {
	if a.model != nil {
		a.model.SetModelRoles(provider, opus, sonnet, haiku)
	}
}

// SetCurrentModel forwards the active model name to the TUI status bar.
func (a *Adapter) SetCurrentModel(name string) {
	if a.model != nil {
		a.model.SetCurrentModel(name)
	}
}

// SetArgCompleter injects a dynamic argument completer for slash command autocomplete.
// Should be called before Start(). If called after Start(), the completer is
// forwarded to the running Model immediately.
func (a *Adapter) SetArgCompleter(fn ArgCompleter) {
	if a.model != nil {
		a.model.SetArgCompleter(fn)
	}
	// Store for propagation in Start()
	a.argCompleter = fn
}

func (a *Adapter) Name() string { return "tui" }

func (a *Adapter) Start(ctx context.Context, handler channel.InboundHandler) error {
	a.handler = handler

	username := getUsername()
	cwd, _ := os.Getwd()

	m := NewModel(a.version, username, cwd)
	if a.argCompleter != nil {
		m.SetArgCompleter(a.argCompleter)
	}
	a.model = &m

	// Create a custom model wrapper that captures user input
	wrapper := &modelWrapper{
		Model:       &m,
		adapter:     a,
		userInputCh: a.userInputCh,
	}

	a.program = tea.NewProgram(
		wrapper,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	// Route user input to the gateway in a background goroutine
	go a.routeInput(ctx)

	return nil
}

// Run starts the Bubble Tea event loop. This blocks until the user quits.
// Call after Start().
func (a *Adapter) Run() error {
	if a.program == nil {
		return fmt.Errorf("tui: Start() must be called before Run()")
	}
	_, err := a.program.Run()
	return err
}

// routeInput reads user text from userInputCh and dispatches to the gateway handler.
func (a *Adapter) routeInput(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.stopCh:
			return
		case text := <-a.userInputCh:
			if a.handler == nil {
				continue
			}
			// Cancel any in-flight request before starting a new one.
			a.cancelCurrentRequest()

			reqCtx, cancel := context.WithCancel(ctx)
			a.cancelMu.Lock()
			a.cancelFn = cancel
			a.cancelGen++
			gen := a.cancelGen
			a.cancelMu.Unlock()

			go func() {
				defer func() {
					a.cancelMu.Lock()
					if a.cancelGen == gen {
						a.cancelFn = nil
					}
					a.cancelMu.Unlock()
					cancel()
				}()
				// MessageID is intentionally empty: the local TUI has no redelivery
				// semantics, and its per-session counter resets on restart — using it
				// as a heart dedup key could collide with a pre-restart event and
				// silently drop a new message. Telegram (update_id) is the channel that
				// needs and has a durable, monotonic id.
				a.handler(reqCtx, channel.InboundMessage{
					Channel:   "tui",
					ChannelID: a.channelID,
					UserID:    "local",
					UserName:  "local",
					Text:      text,
				})
			}()
		}
	}
}

// cancelCurrentRequest cancels the in-flight agent request, if any.
func (a *Adapter) cancelCurrentRequest() {
	a.cancelMu.Lock()
	fn := a.cancelFn
	a.cancelFn = nil
	a.cancelMu.Unlock()
	if fn != nil {
		fn()
	}
}

func (a *Adapter) Send(_ context.Context, msg channel.OutboundMessage) error {
	if a.program == nil {
		return nil
	}
	a.program.Send(agentResponseMsg{text: msg.Text})
	return nil
}

func (a *Adapter) SendStreaming(_ context.Context, target channel.MessageTarget) (channel.StreamUpdater, error) {
	if a.program == nil {
		return nil, fmt.Errorf("tui: program not started")
	}

	id := fmt.Sprintf("stream_%d", time.Now().UnixNano())

	su := &tuiStreamUpdater{
		program: a.program,
		id:      id,
		done:    make(chan struct{}),
	}

	// Background goroutine pushes throttled updates to the Bubble Tea loop
	go su.pump()

	return su, nil
}

func (a *Adapter) Stop(_ context.Context) error {
	a.stopOnce.Do(func() {
		close(a.stopCh)
		if a.program != nil {
			a.program.Quit()
		}
		slog.Info("tui channel stopped")
	})
	return nil
}

// ---------- channel.ApprovalSender ----------

func (a *Adapter) SendApprovalRequest(ctx context.Context, target channel.MessageTarget, toolName string, input string) (bool, error) {
	if a.autoApprove.Load() {
		return true, nil
	}
	if a.program == nil {
		return true, nil
	}

	resultCh := make(chan bool, 1)
	a.program.Send(approvalRequestMsg{
		toolName: toolName,
		input:    input,
		resultCh: resultCh,
	})

	select {
	case approved := <-resultCh:
		return approved, nil
	case <-time.After(a.approvalTimeout):
		slog.Info("tui: approval timed out, defaulting to deny", "tool", toolName)
		return false, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// ---------- channel.FeedbackSender ----------

// SendFeedbackRequest displays a "Was this helpful? (y/n)" dialog in the TUI
// and blocks until the user responds, the approvalTimeout expires, or ctx is
// cancelled.  Returns 1.0 (helpful), -1.0 (not helpful), or 0.0 on timeout.
func (a *Adapter) SendFeedbackRequest(ctx context.Context, target channel.MessageTarget) (float64, error) {
	if a.program == nil {
		return 0, nil
	}

	resultCh := make(chan float64, 1)
	a.program.Send(feedbackRequestMsg{
		resultCh: resultCh,
	})

	select {
	case feedback := <-resultCh:
		return feedback, nil
	case <-time.After(a.approvalTimeout):
		slog.Info("tui: feedback timed out, defaulting to neutral")
		return 0, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// ---------- channel.ToolActivitySender ----------

// SendToolActivity surfaces live tool-execution activity in the TUI status
// line. It is non-blocking and best-effort.
func (a *Adapter) SendToolActivity(_ context.Context, _ channel.MessageTarget, act channel.ToolActivity) error {
	if a.program == nil {
		return nil
	}
	a.program.Send(toolActivityMsg{
		callID:        act.CallID,
		tool:          act.ToolName,
		arg:           act.ArgSummary,
		done:          act.Done,
		ok:            act.OK,
		resultSummary: act.ResultSummary,
		output:        act.Output,
		duration:      act.Duration,
		depth:         act.Depth,
	})
	return nil
}

// ---------- channel.NotificationSender ----------

// SendNotification displays a dim status notification in the TUI output area.
func (a *Adapter) SendNotification(_ context.Context, target channel.MessageTarget, text string) error {
	if a.program == nil {
		return nil
	}
	a.program.Send(notificationMsg{text: text})
	return nil
}

// ---------- modelWrapper ----------

// modelWrapper wraps Model and captures user input on Enter.
type modelWrapper struct {
	*Model
	adapter     *Adapter
	userInputCh chan string
}

func (w *modelWrapper) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Intercept setAutoApproveMsg to enable auto-approve in the adapter
	if _, ok := msg.(setAutoApproveMsg); ok {
		w.adapter.autoApprove.Store(true)
		slog.Info("tui: auto-approve enabled by user")
	}

	// Intercept cancelRequestMsg to cancel the in-flight agent request
	if _, ok := msg.(cancelRequestMsg); ok {
		w.adapter.cancelCurrentRequest()
		slog.Info("tui: user cancelled current request")
	}

	// Intercept Enter in chat mode to capture user input text.
	// Apply any currently-selected autocomplete suggestion before forwarding,
	// so the gateway receives the same text that the Model will display.
	// Only forward to the agent if it is NOT a local slash command.
	if keyMsg, ok := msg.(tea.KeyMsg); ok && w.mode == modeChat && keyMsg.Type == tea.KeyEnter {
		text := strings.TrimSpace(w.textarea.Value())
		if w.showingSuggestions && w.selectedSuggestion >= 0 && w.selectedSuggestion < len(w.suggestions) {
			applied := strings.TrimSpace(ApplySuggestion(text, w.suggestions[w.selectedSuggestion]))
			if applied != "" {
				text = applied
			}
		}
		if text != "" && !isLocalCommand(text) {
			w.userInputCh <- text
		}
	}

	m, cmd := w.Model.Update(msg)
	if model, ok := m.(*Model); ok {
		w.Model = model
	}
	return w, cmd
}

func (w *modelWrapper) View() string {
	return w.Model.View()
}

func (w *modelWrapper) Init() tea.Cmd {
	return w.Model.Init()
}

// ---------- tuiStreamUpdater ----------

type tuiStreamUpdater struct {
	program *tea.Program
	id      string
	latest  atomic.Value // string
	done    chan struct{}
}

const streamThrottle = 50 * time.Millisecond

func (s *tuiStreamUpdater) Update(text string) error {
	s.latest.Store(text)
	return nil
}

func (s *tuiStreamUpdater) Finish(text string) error {
	close(s.done)
	s.program.Send(streamFinishMsg{id: s.id, text: text})
	return nil
}

// pump periodically sends the latest text to the Bubble Tea loop.
func (s *tuiStreamUpdater) pump() {
	ticker := time.NewTicker(streamThrottle)
	defer ticker.Stop()

	var lastSent string
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			v := s.latest.Load()
			if v == nil {
				continue
			}
			text := v.(string)
			if text != lastSent {
				s.program.Send(streamUpdateMsg{id: s.id, text: text})
				lastSent = text
			}
		}
	}
}

// getUsername returns the current OS username, or "you" if unavailable.
func getUsername() string {
	u, err := user.Current()
	if err != nil || u.Username == "" {
		return "you"
	}
	return u.Username
}
