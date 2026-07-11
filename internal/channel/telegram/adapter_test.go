package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestStopIsIdempotent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"test","username":"test_bot"}}`))
	}))
	defer server.Close()

	bot, err := tgbotapi.NewBotAPIWithAPIEndpoint("token", server.URL+"/bot%s/%s")
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	a := &Adapter{bot: bot, stopCh: make(chan struct{})}

	if err := a.Stop(context.Background()); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := a.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

// TestHandleCallbackRoutesProposalDecisions verifies the inline accept/dismiss
// buttons reach the registered handler with the right id and verdict. handleCallback
// invokes the handler in a goroutine, so each case waits on a channel.
func TestHandleCallbackRoutesProposalDecisions(t *testing.T) {
	a := &Adapter{}
	type decision struct {
		id     string
		accept bool
	}
	got := make(chan decision, 1)
	a.SetProposalHandler(func(_ context.Context, id string, accept bool) {
		got <- decision{id, accept}
	})

	a.handleCallback(context.Background(), "proposal_accept:proposal_abc")
	select {
	case d := <-got:
		if d.id != "proposal_abc" || !d.accept {
			t.Fatalf("accept routed wrong: %+v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("accept callback not routed to handler")
	}

	a.handleCallback(context.Background(), "proposal_dismiss:proposal_abc")
	select {
	case d := <-got:
		if d.id != "proposal_abc" || d.accept {
			t.Fatalf("dismiss routed wrong: %+v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dismiss callback not routed to handler")
	}
}

// TestHandleCallbackProposalNoHandlerNoPanic ensures a tap that arrives before a
// handler is registered (or in a channel that never sets one) is dropped quietly.
func TestHandleCallbackProposalNoHandlerNoPanic(t *testing.T) {
	a := &Adapter{}
	// Must not panic and must not route anywhere.
	a.handleCallback(context.Background(), "proposal_accept:p1")
}

// TestHandleCallbackProposalDoesNotLeakToOtherHandlers proves a valid proposal
// callback returns early: it fires the proposal handler and never signals a
// pendingApproval/pendingFeedback channel keyed to the same id.
func TestHandleCallbackProposalDoesNotLeakToOtherHandlers(t *testing.T) {
	a := &Adapter{}
	fired := make(chan bool, 1)
	a.SetProposalHandler(func(_ context.Context, _ string, accept bool) { fired <- accept })

	// Seed approval+feedback channels keyed to the same id the callback carries.
	approvalCh := make(chan bool, 1)
	feedbackCh := make(chan float64, 1)
	a.pendingApprovals.Store("p1", approvalCh)
	a.pendingFeedbacks.Store("p1", feedbackCh)

	a.handleCallback(context.Background(), "proposal_accept:p1")

	select {
	case accept := <-fired:
		if !accept {
			t.Fatal("expected accept=true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("proposal handler was not invoked")
	}
	// The approval/feedback channels must be untouched (proposal returned early).
	if len(approvalCh) != 0 {
		t.Fatal("proposal callback leaked into pendingApprovals")
	}
	if len(feedbackCh) != 0 {
		t.Fatal("proposal callback leaked into pendingFeedbacks")
	}
}

// TestProposalHandlerConcurrentSetAndCall stresses the atomic handler field: many
// goroutines set and invoke it at once. Run under -race, it proves the field is
// safe to write while the update goroutine reads it.
func TestProposalHandlerConcurrentSetAndCall(t *testing.T) {
	a := &Adapter{}
	a.SetProposalHandler(func(context.Context, string, bool) {})

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); a.SetProposalHandler(func(context.Context, string, bool) {}) }()
		go func() { defer wg.Done(); a.handleCallback(context.Background(), "proposal_accept:p1") }()
	}
	wg.Wait()
}
