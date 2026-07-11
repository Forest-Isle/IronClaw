package tui

import (
	"context"
	"testing"
)

func TestStopIsIdempotent(t *testing.T) {
	a := New("test")

	if err := a.Stop(context.Background()); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := a.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}
