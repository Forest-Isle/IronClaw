package canary

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/Forest-Isle/daimon/internal/mind"
)

type countingFakeProvider struct {
	caps             mind.Caps
	completeRequests int
	streamRequests   int
	completeResp     *mind.CompletionResponse
	completeErr      error
	stream           mind.StreamIterator
	streamErr        error
}

func (p *countingFakeProvider) Complete(_ context.Context, req mind.CompletionRequest) (*mind.CompletionResponse, error) {
	p.completeRequests++
	if req.Model != "model-a" {
		return nil, errors.New("request not delegated")
	}
	return p.completeResp, p.completeErr
}

func (p *countingFakeProvider) Stream(_ context.Context, req mind.CompletionRequest) (mind.StreamIterator, error) {
	p.streamRequests++
	if req.Model != "model-a" {
		return nil, errors.New("request not delegated")
	}
	return p.stream, p.streamErr
}

func (p *countingFakeProvider) Capabilities() mind.Caps {
	return p.caps
}

type countingFakeStream struct {
	deltas []mind.StreamDelta
	closed bool
}

func (s *countingFakeStream) Next() (mind.StreamDelta, error) {
	if len(s.deltas) == 0 {
		return mind.StreamDelta{}, io.EOF
	}
	delta := s.deltas[0]
	s.deltas = s.deltas[1:]
	return delta, nil
}

func (s *countingFakeStream) Close() {
	s.closed = true
}

func TestCountingProviderDelegatesAndCountsUsage(t *testing.T) {
	stream := &countingFakeStream{deltas: []mind.StreamDelta{
		{Text: "partial", Usage: mind.Usage{InputTokens: 3, OutputTokens: 4}},
		{Done: true, StopReason: mind.StopEndTurn, Usage: mind.Usage{InputTokens: 5, OutputTokens: 6}},
	}}
	inner := &countingFakeProvider{
		caps:         mind.Caps{CacheBreakpoints: 2},
		completeResp: &mind.CompletionResponse{Text: "ok", Usage: mind.Usage{InputTokens: 7, OutputTokens: 8}},
		stream:       stream,
	}
	counting := &countingProvider{Provider: inner}

	resp, err := counting.Complete(context.Background(), mind.CompletionRequest{Model: "model-a"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text != "ok" || inner.completeRequests != 1 {
		t.Fatalf("Complete was not faithfully delegated: resp=%+v requests=%d", resp, inner.completeRequests)
	}

	iter, err := counting.Stream(context.Background(), mind.CompletionRequest{Model: "model-a"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for {
		if _, err := iter.Next(); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("Next: %v", err)
		}
	}
	iter.Close()

	if got := counting.Capabilities(); got != inner.caps {
		t.Fatalf("Capabilities = %+v, want %+v", got, inner.caps)
	}
	if inner.streamRequests != 1 || !stream.closed {
		t.Fatalf("Stream was not faithfully delegated: requests=%d closed=%t", inner.streamRequests, stream.closed)
	}
	if got, want := counting.inputTokens(), int64(15); got != want {
		t.Fatalf("input tokens = %d, want %d", got, want)
	}
	if got, want := counting.outputTokens(), int64(18); got != want {
		t.Fatalf("output tokens = %d, want %d", got, want)
	}
}

func TestFinalizeTaskResultSumsTrialTokens(t *testing.T) {
	result := TaskResult{Results: []TrialResult{
		{Passed: true, InputTokens: 10, OutputTokens: 2},
		{Passed: false, InputTokens: 30, OutputTokens: 4},
		{Passed: true, InputTokens: 50, OutputTokens: 6},
	}}

	finalizeTaskResult(&result)

	if result.Trials != 3 {
		t.Fatalf("Trials = %d, want 3", result.Trials)
	}
	if result.PassRate != 0 {
		t.Fatalf("PassRate = %f before Passes set, want 0", result.PassRate)
	}
	if result.InputTokens != 90 || result.OutputTokens != 12 {
		t.Fatalf("tokens = %d/%d, want 90/12", result.InputTokens, result.OutputTokens)
	}
}
