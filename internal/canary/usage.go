package canary

import (
	"context"
	"sync/atomic"

	"github.com/Forest-Isle/daimon/internal/mind"
)

type countingProvider struct {
	mind.Provider
	in  atomic.Int64
	out atomic.Int64
}

func (p *countingProvider) Complete(ctx context.Context, req mind.CompletionRequest) (*mind.CompletionResponse, error) {
	resp, err := p.Provider.Complete(ctx, req)
	if resp != nil {
		p.add(resp.Usage)
	}
	return resp, err
}

func (p *countingProvider) Stream(ctx context.Context, req mind.CompletionRequest) (mind.StreamIterator, error) {
	stream, err := p.Provider.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	return &countingStream{inner: stream, provider: p}, nil
}

func (p *countingProvider) add(usage mind.Usage) {
	p.in.Add(int64(usage.InputTokens))
	p.out.Add(int64(usage.OutputTokens))
}

func (p *countingProvider) inputTokens() int64 {
	return p.in.Load()
}

func (p *countingProvider) outputTokens() int64 {
	return p.out.Load()
}

type countingStream struct {
	inner    mind.StreamIterator
	provider *countingProvider
}

func (s *countingStream) Next() (mind.StreamDelta, error) {
	delta, err := s.inner.Next()
	if err == nil {
		s.provider.add(delta.Usage)
	}
	return delta, err
}

func (s *countingStream) Close() {
	s.inner.Close()
}
