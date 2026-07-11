package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Forest-Isle/daimon/internal/channel"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Adapter implements channel.Channel, channel.ApprovalSender,
// channel.FeedbackSender, and channel.NotificationSender for Telegram.
type Adapter struct {
	bot            *tgbotapi.BotAPI
	allowedUserIDs map[int64]bool
	handler        channel.InboundHandler
	stopCh         chan struct{}
	stopOnce       sync.Once
	pollTimeout    int // seconds

	// Approval tracking — moved from Gateway so the adapter fully owns the flow.
	pendingApprovals    sync.Map // key: toolName → chan bool
	pendingFeedbacks    sync.Map // key: string → chan float64
	approvalTimeoutSecs int

	// autoApprove, when true, skips interactive approval for all subsequent requests.
	autoApprove atomic.Bool

	// proposalHandler (an atomic.Value holding func(ctx, id, accept)) is invoked
	// asynchronously when the user taps a proposal's accept/dismiss button (§4.9).
	// Stored atomically because it is written by SetProposalHandler and read from
	// the update goroutine. Empty until wired; a tap with no handler is dropped
	// after acknowledgement.
	proposalHandler atomic.Value
}

// proposalHandlerFunc is the dynamic type stored in Adapter.proposalHandler.
type proposalHandlerFunc = func(ctx context.Context, id string, accept bool)

func New(token string, allowedUserIDs []int64, timeoutSecs ...int) (*Adapter, error) {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("telegram bot init: %w", err)
	}

	allowed := make(map[int64]bool, len(allowedUserIDs))
	for _, id := range allowedUserIDs {
		allowed[id] = true
	}

	pollTimeout := 30
	if len(timeoutSecs) > 0 && timeoutSecs[0] > 0 {
		pollTimeout = timeoutSecs[0]
	}

	slog.Info("telegram bot authorized", "poll_timeout", pollTimeout, "username", bot.Self.UserName)
	return &Adapter{
		bot:                 bot,
		allowedUserIDs:      allowed,
		stopCh:              make(chan struct{}),
		pollTimeout:         pollTimeout,
		approvalTimeoutSecs: 120,
	}, nil
}

// SetApprovalTimeout overrides the default approval timeout (120s).
func (a *Adapter) SetApprovalTimeout(seconds int) {
	if seconds > 0 {
		a.approvalTimeoutSecs = seconds
	}
}

func (a *Adapter) Name() string { return "telegram" }

func (a *Adapter) Start(ctx context.Context, handler channel.InboundHandler) error {
	a.handler = handler

	u := tgbotapi.NewUpdate(0)
	u.Timeout = a.pollTimeout
	updates := a.bot.GetUpdatesChan(u)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-a.stopCh:
				return
			case update := <-updates:
				a.handleUpdate(ctx, update)
			}
		}
	}()

	slog.Info("telegram channel started")
	return nil
}

func (a *Adapter) handleUpdate(ctx context.Context, update tgbotapi.Update) {
	// Handle callback queries (inline keyboard) — must be synchronous
	// so the approval/reflection channel receives the result before the update loop moves on.
	if update.CallbackQuery != nil {
		userID := update.CallbackQuery.From.ID
		if !a.allowedUserIDs[userID] {
			return
		}

		data := update.CallbackQuery.Data
		a.handleCallback(ctx, data)

		// Acknowledge callback
		callback := tgbotapi.NewCallback(update.CallbackQuery.ID, "")
		_, _ = a.bot.Request(callback)
		return
	}

	if update.Message == nil {
		return
	}

	userID := update.Message.From.ID
	if !a.allowedUserIDs[userID] {
		slog.Warn("unauthorized user", "user_id", userID, "username", update.Message.From.UserName)
		return
	}

	chatID := update.Message.Chat.ID
	// Process message asynchronously so the update loop stays unblocked.
	// This is critical: if HandleMessage waits for tool approval, a synchronous
	// call would deadlock because the callback query could never be received.
	go a.handler(ctx, channel.InboundMessage{
		Channel:   "telegram",
		ChannelID: strconv.FormatInt(chatID, 10),
		UserID:    strconv.FormatInt(userID, 10),
		UserName:  update.Message.From.UserName,
		Text:      update.Message.Text,
		MessageID: strconv.Itoa(update.UpdateID),
	})
}

// handleCallback routes inline keyboard callback data to the appropriate
// pending approval or reflection channel.
func (a *Adapter) handleCallback(ctx context.Context, data string) {
	parts := strings.SplitN(data, ":", 2)
	if len(parts) != 2 {
		return
	}

	action, key := parts[0], parts[1]

	// Handle proposal decisions (§4.9). These are fire-and-forget: the handler
	// fires an episode (accept) or records feedback (dismiss), both potentially
	// slow, so it runs in a goroutine to keep the update loop responsive. key is
	// the proposal id.
	switch action {
	case "proposal_accept", "proposal_dismiss":
		if v := a.proposalHandler.Load(); v != nil {
			h := v.(proposalHandlerFunc)
			accept := action == "proposal_accept"
			go h(ctx, key, accept)
		}
		return
	}

	// Handle feedback responses
	switch action {
	case "feedback_yes", "feedback_no":
		var score float64
		if action == "feedback_yes" {
			score = 1.0
		} else {
			score = -1.0
		}
		if v, ok := a.pendingFeedbacks.Load(key); ok {
			ch := v.(chan float64)
			select {
			case ch <- score:
			default:
			}
		}
		return
	}

	// Handle always-approve: set flag and approve current request
	if action == "always_approve" {
		a.autoApprove.Store(true)
		if v, ok := a.pendingApprovals.Load(key); ok {
			ch := v.(chan bool)
			select {
			case ch <- true:
			default:
			}
		}
		return
	}

	// Handle tool approval
	approved := action == "approve"
	if v, ok := a.pendingApprovals.Load(key); ok {
		ch := v.(chan bool)
		select {
		case ch <- approved:
		default:
		}
	}
}

func (a *Adapter) Send(ctx context.Context, msg channel.OutboundMessage) error {
	chatID, err := strconv.ParseInt(msg.ChannelID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid chat id: %w", err)
	}

	tgMsg := tgbotapi.NewMessage(chatID, msg.Text)
	if msg.ParseMode != "" {
		tgMsg.ParseMode = msg.ParseMode
	}
	if msg.ReplyMarkup != nil {
		tgMsg.ReplyMarkup = msg.ReplyMarkup
	}

	_, err = a.bot.Send(tgMsg)
	return err
}

func (a *Adapter) SendStreaming(ctx context.Context, target channel.MessageTarget) (channel.StreamUpdater, error) {
	chatID, err := strconv.ParseInt(target.ChannelID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid chat id: %w", err)
	}

	// Send initial placeholder message
	msg := tgbotapi.NewMessage(chatID, "⏳ Thinking...")
	sent, err := a.bot.Send(msg)
	if err != nil {
		return nil, err
	}

	return &streamUpdater{
		bot:    a.bot,
		chatID: chatID,
		msgID:  sent.MessageID,
	}, nil
}

func (a *Adapter) Stop(_ context.Context) error {
	a.stopOnce.Do(func() {
		close(a.stopCh)
		a.bot.StopReceivingUpdates()
		slog.Info("telegram channel stopped")
	})
	return nil
}

// ---------- channel.ApprovalSender ----------

// SendApprovalRequest sends a Telegram inline keyboard for tool approval and
// blocks until the user responds or the timeout expires.
func (a *Adapter) SendApprovalRequest(ctx context.Context, target channel.MessageTarget, toolName string, input string) (bool, error) {
	if a.autoApprove.Load() {
		return true, nil
	}

	chatID, err := strconv.ParseInt(target.ChannelID, 10, 64)
	if err != nil || chatID == 0 {
		return true, nil // fallback: auto-approve
	}

	text := fmt.Sprintf("🔧 Tool: *%s*\n```\n%s\n```\nApprove execution?", toolName, FormatForTelegram(input))

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ Approve", "approve:"+toolName),
			tgbotapi.NewInlineKeyboardButtonData("❌ Deny", "deny:"+toolName),
			tgbotapi.NewInlineKeyboardButtonData("🔓 Always Approve", "always_approve:"+toolName),
		),
	)

	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = keyboard

	if _, err := a.bot.Send(msg); err != nil {
		return false, err
	}

	// Wait for callback
	resultCh := make(chan bool, 1)
	a.pendingApprovals.Store(toolName, resultCh)
	defer a.pendingApprovals.Delete(toolName)

	timeout := time.Duration(a.approvalTimeoutSecs) * time.Second
	select {
	case approved := <-resultCh:
		return approved, nil
	case <-time.After(timeout):
		slog.Info("telegram: approval timed out, defaulting to deny", "tool", toolName)
		return false, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// ---------- channel.FeedbackSender ----------

// SendFeedbackRequest sends a Telegram inline keyboard asking "Was this helpful?"
// and blocks until the user responds or the timeout expires.
// Returns 1.0 (positive), -1.0 (negative), or 0.0 on timeout.
func (a *Adapter) SendFeedbackRequest(ctx context.Context, target channel.MessageTarget) (float64, error) {
	chatID, err := strconv.ParseInt(target.ChannelID, 10, 64)
	if err != nil || chatID == 0 {
		return 0, nil // fallback: neutral
	}

	key := fmt.Sprintf("feedback_%s_%d", target.ChannelID, time.Now().UnixNano())

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("👍", "feedback_yes:"+key),
			tgbotapi.NewInlineKeyboardButtonData("👎", "feedback_no:"+key),
		),
	)

	msg := tgbotapi.NewMessage(chatID, "Was this helpful?")
	msg.ReplyMarkup = keyboard

	if _, err := a.bot.Send(msg); err != nil {
		slog.Warn("telegram: failed to send feedback request", "err", err)
		return 0, nil
	}

	// Wait for callback
	resultCh := make(chan float64, 1)
	a.pendingFeedbacks.Store(key, resultCh)
	defer a.pendingFeedbacks.Delete(key)

	timeout := time.Duration(a.approvalTimeoutSecs) * time.Second
	select {
	case score := <-resultCh:
		slog.Info("telegram: feedback received", "score", score)
		return score, nil
	case <-time.After(timeout):
		slog.Info("telegram: feedback timed out, defaulting to neutral")
		return 0, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// ---------- channel.NotificationSender ----------

// SendNotification sends a silent notification message to the Telegram chat.
func (a *Adapter) SendNotification(_ context.Context, target channel.MessageTarget, text string) error {
	chatID, err := strconv.ParseInt(target.ChannelID, 10, 64)
	if err != nil || chatID == 0 {
		return nil
	}

	msg := tgbotapi.NewMessage(chatID, text)
	msg.DisableNotification = true
	_, err = a.bot.Send(msg)
	return err
}

// ---------- channel.ProposalSender ----------

// SetProposalHandler registers the async callback invoked when the user taps a
// proposal's accept/dismiss button. Set once at startup, before the update loop
// begins, so callbacks never race a nil handler.
func (a *Adapter) SetProposalHandler(h proposalHandlerFunc) {
	a.proposalHandler.Store(h)
}

// SendProposal pushes one anticipation proposal with inline [✅ 做 / ❌ 不做]
// buttons and returns immediately (the decision arrives later via callback). The
// callback data carries the proposal id; ParseMode is left plain so an
// LLM-authored title/body cannot trigger a Telegram markdown parse error.
func (a *Adapter) SendProposal(_ context.Context, target channel.MessageTarget, id, title, body string) error {
	chatID, err := strconv.ParseInt(target.ChannelID, 10, 64)
	if err != nil || chatID == 0 {
		return fmt.Errorf("invalid proposal target chat id %q: %w", target.ChannelID, err)
	}

	text := "💡 " + title
	if strings.TrimSpace(body) != "" {
		text += "\n\n" + body
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ 做", "proposal_accept:"+id),
			tgbotapi.NewInlineKeyboardButtonData("❌ 不做", "proposal_dismiss:"+id),
		),
	)

	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyMarkup = keyboard
	if _, err := a.bot.Send(msg); err != nil {
		return fmt.Errorf("send proposal: %w", err)
	}
	return nil
}

// ---------- streamUpdater ----------

type streamUpdater struct {
	bot    *tgbotapi.BotAPI
	chatID int64
	msgID  int
	mu     sync.Mutex
	last   string
	lastAt time.Time
}

const minUpdateInterval = 1 * time.Second

func (s *streamUpdater) Update(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Rate-limit edits to avoid Telegram API throttling
	if time.Since(s.lastAt) < minUpdateInterval && s.last != "" {
		return nil
	}

	formatted := FormatForTelegram(text)
	if formatted == s.last {
		return nil
	}

	edit := tgbotapi.NewEditMessageText(s.chatID, s.msgID, formatted)
	_, err := s.bot.Send(edit)
	if err != nil {
		// Telegram returns error if message content hasn't changed
		return nil
	}

	s.last = formatted
	s.lastAt = time.Now()
	return nil
}

func (s *streamUpdater) Finish(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	formatted := FormatForTelegram(text)
	edit := tgbotapi.NewEditMessageText(s.chatID, s.msgID, formatted)
	_, err := s.bot.Send(edit)
	return err
}
