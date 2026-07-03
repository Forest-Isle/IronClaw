package heart

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestCalendarSourcePollEmitsUpcomingEventShape(t *testing.T) {
	lookahead := 2 * time.Hour
	startText := "2026-07-03T09:30:00Z"
	endText := "2026-07-03T10:00:00Z"
	src := &CalendarSource{
		Lookahead: lookahead,
		Fetch: func(ctx context.Context, start, end time.Time) ([]CalendarEvent, error) {
			return []CalendarEvent{{
				UID:      "uid-1",
				Summary:  "Planning",
				Location: "HQ",
				Start:    startText,
				End:      endText,
			}}, nil
		},
	}

	var events []Event
	if err := src.pollOnce(context.Background(), func(ev Event) error {
		events = append(events, ev)
		return nil
	}); err != nil {
		t.Fatalf("pollOnce() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	ev := events[0]
	if ev.Kind != "calendar.upcoming" {
		t.Fatalf("Kind = %q, want calendar.upcoming", ev.Kind)
	}
	if ev.DedupKey != "uid-1|"+startText {
		t.Fatalf("DedupKey = %q, want %q", ev.DedupKey, "uid-1|"+startText)
	}

	var payload struct {
		UID      string `json:"uid"`
		Summary  string `json:"summary"`
		Location string `json:"location"`
		Start    string `json:"start"`
		End      string `json:"end"`
	}
	if err := json.Unmarshal([]byte(ev.Payload), &payload); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if payload.UID != "uid-1" || payload.Summary != "Planning" || payload.Location != "HQ" || payload.Start != startText || payload.End != endText {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestCalendarSourcePollWindowUsesLookahead(t *testing.T) {
	lookahead := 90 * time.Minute
	var gotStart, gotEnd time.Time
	src := &CalendarSource{
		Lookahead: lookahead,
		Fetch: func(ctx context.Context, start, end time.Time) ([]CalendarEvent, error) {
			gotStart = start
			gotEnd = end
			return nil, nil
		},
	}

	before := time.Now()
	if err := src.pollOnce(context.Background(), func(ev Event) error {
		t.Fatalf("emit called: %+v", ev)
		return nil
	}); err != nil {
		t.Fatalf("pollOnce() error = %v", err)
	}
	after := time.Now()

	if gotStart.Before(before) || gotStart.After(after) {
		t.Fatalf("start = %v, want between %v and %v", gotStart, before, after)
	}
	if gotEnd.Sub(gotStart) != lookahead {
		t.Fatalf("window = %v, want %v", gotEnd.Sub(gotStart), lookahead)
	}
}

func TestCalendarSourcePollSkipsEventsWithoutUID(t *testing.T) {
	src := &CalendarSource{
		Fetch: func(ctx context.Context, start, end time.Time) ([]CalendarEvent, error) {
			return []CalendarEvent{
				{Summary: "No UID", Start: "2026-07-03T09:00:00Z"},
				{UID: "uid-2", Summary: "Has UID", Start: "2026-07-03T10:00:00Z"},
			}, nil
		},
	}

	var events []Event
	if err := src.pollOnce(context.Background(), func(ev Event) error {
		events = append(events, ev)
		return nil
	}); err != nil {
		t.Fatalf("pollOnce() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].DedupKey != "uid-2|2026-07-03T10:00:00Z" {
		t.Fatalf("DedupKey = %q", events[0].DedupKey)
	}
}

func TestCalendarSourceRunFetchErrorContinuesUntilCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := make(chan int, 3)
	var count atomic.Int32
	src := &CalendarSource{
		PollInterval: time.Millisecond,
		Fetch: func(ctx context.Context, start, end time.Time) ([]CalendarEvent, error) {
			n := int(count.Add(1))
			calls <- n
			if n >= 2 {
				cancel()
			}
			return nil, errors.New("fetch failed")
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- src.Run(ctx, func(ev Event) error {
			t.Errorf("emit called on fetch error: %+v", ev)
			return nil
		})
	}()

	for want := 1; want <= 2; want++ {
		select {
		case got := <-calls:
			if got != want {
				t.Fatalf("call = %d, want %d", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for fetch call %d", want)
		}
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after cancel")
	}
}

func TestCalendarSourceRunNilFetchBlocksUntilCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	src := &CalendarSource{PollInterval: time.Hour}

	go func() {
		done <- src.Run(ctx, func(ev Event) error {
			t.Errorf("emit called with nil fetch")
			return nil
		})
	}()
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after cancel")
	}
}
