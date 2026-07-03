package heart

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Forest-Isle/daimon/internal/netdial"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
)

// CalendarEvent is one CalDAV VEVENT occurrence projected into the heart.
type CalendarEvent struct {
	UID      string
	Summary  string
	Location string
	Start    string // RFC3339
	End      string // RFC3339, empty when absent
}

// CalendarSource polls upcoming calendar events and emits calendar.upcoming
// heart events.
type CalendarSource struct {
	Fetch        func(ctx context.Context, start, end time.Time) ([]CalendarEvent, error)
	PollInterval time.Duration
	Lookahead    time.Duration
}

func (c *CalendarSource) Name() string {
	return "calendar"
}

func (c *CalendarSource) Run(ctx context.Context, emit func(Event) error) error {
	poll := c.PollInterval
	if poll <= 0 {
		poll = 300 * time.Second
	}
	if c.Fetch == nil {
		<-ctx.Done()
		return ctx.Err()
	}

	if err := c.pollOnce(ctx, emit); err != nil {
		slog.Warn("calendar: poll failed", "err", err)
	}

	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := c.pollOnce(ctx, emit); err != nil {
				slog.Warn("calendar: poll failed", "err", err)
			}
		}
	}
}

func (c *CalendarSource) pollOnce(ctx context.Context, emit func(Event) error) error {
	lookahead := c.Lookahead
	if lookahead <= 0 {
		lookahead = 24 * time.Hour
	}
	now := time.Now()
	events, err := c.Fetch(ctx, now, now.Add(lookahead))
	if err != nil {
		return err
	}

	for _, event := range events {
		if event.UID == "" {
			slog.Debug("calendar: skip event without uid", "summary", event.Summary, "start", event.Start)
			continue
		}
		payload, err := json.Marshal(struct {
			UID      string `json:"uid"`
			Summary  string `json:"summary"`
			Location string `json:"location"`
			Start    string `json:"start"`
			End      string `json:"end"`
		}{
			UID:      event.UID,
			Summary:  event.Summary,
			Location: event.Location,
			Start:    event.Start,
			End:      event.End,
		})
		if err != nil {
			slog.Debug("calendar: marshal event failed", "uid", event.UID, "start", event.Start, "err", err)
			continue
		}
		dedupKey := event.UID + "|" + event.Start
		// Calendar keeps no high-water state: every poll re-emits the current
		// upcoming window, and the store's UNIQUE(source,dedup_key) collapses
		// repeats. If a crash happens before persist, the next poll re-fetches
		// the same window, so delivery stays crash-safe at-least-once.
		if err := emit(Event{Kind: "calendar.upcoming", Payload: string(payload), DedupKey: dedupKey}); err != nil {
			slog.Warn("calendar: emit event failed", "uid", event.UID, "start", event.Start, "err", err)
		}
	}
	return nil
}

// CalDAVFetch returns a CalDAV fetch function for CalendarSource.
func CalDAVFetch(serverURL, username, password string, calendarPaths []string) func(ctx context.Context, start, end time.Time) ([]CalendarEvent, error) {
	return func(ctx context.Context, start, end time.Time) ([]CalendarEvent, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		httpClient := &http.Client{Transport: &http.Transport{
			DialContext:           netdial.DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			ExpectContinueTimeout: time.Second,
			IdleConnTimeout:       90 * time.Second,
		}}
		authClient := webdav.HTTPClientWithBasicAuth(httpClient, username, password)
		client, err := caldav.NewClient(authClient, serverURL)
		if err != nil {
			return nil, fmt.Errorf("calendar: create caldav client: %w", err)
		}

		paths := append([]string(nil), calendarPaths...)
		if len(paths) == 0 {
			paths, err = discoverCalDAVCalendars(ctx, client)
			if err != nil {
				return nil, err
			}
		}

		query := calendarQuery(start, end)
		var out []CalendarEvent
		var errs []error
		successes := 0
		for _, path := range paths {
			objects, err := client.QueryCalendar(ctx, path, query)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", path, err))
				slog.Warn("calendar: query calendar failed", "path", path, "err", err)
				continue
			}
			successes++
			out = append(out, calendarEventsFromObjects(objects)...)
		}
		if len(paths) > 0 && successes == 0 && len(errs) > 0 {
			return nil, fmt.Errorf("calendar: query calendars: %w", errors.Join(errs...))
		}
		return out, nil
	}
}

func discoverCalDAVCalendars(ctx context.Context, client *caldav.Client) ([]string, error) {
	principal, err := client.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return nil, fmt.Errorf("calendar: find current user principal: %w", err)
	}
	homeSet, err := client.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		return nil, fmt.Errorf("calendar: find calendar home set: %w", err)
	}
	calendars, err := client.FindCalendars(ctx, homeSet)
	if err != nil {
		return nil, fmt.Errorf("calendar: find calendars: %w", err)
	}

	var paths []string
	for _, calendar := range calendars {
		if calendarSupportsVEVENT(calendar.SupportedComponentSet) {
			paths = append(paths, calendar.Path)
		}
	}
	return paths, nil
}

func calendarSupportsVEVENT(components []string) bool {
	for _, component := range components {
		if strings.EqualFold(component, ical.CompEvent) {
			return true
		}
	}
	return false
}

func calendarQuery(start, end time.Time) *caldav.CalendarQuery {
	return &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{
			Name:     ical.CompCalendar,
			AllProps: true,
			AllComps: true,
			Expand:   &caldav.CalendarExpandRequest{Start: start, End: end},
		},
		CompFilter: caldav.CompFilter{
			Name: ical.CompCalendar,
			Comps: []caldav.CompFilter{{
				Name:  ical.CompEvent,
				Start: start,
				End:   end,
			}},
		},
	}
}

func calendarEventsFromObjects(objects []caldav.CalendarObject) []CalendarEvent {
	var out []CalendarEvent
	for _, object := range objects {
		if object.Data == nil {
			slog.Debug("calendar: skip empty calendar object", "path", object.Path)
			continue
		}
		// Expand asks the server to unfold recurring events. Some CalDAV servers
		// ignore it and return the master VEVENT; v1 emits that as-is instead of
		// doing client-side RRULE expansion.
		for _, event := range object.Data.Events() {
			calEvent, err := calendarEventFromVEvent(event)
			if err != nil {
				slog.Debug("calendar: parse event failed", "path", object.Path, "err", err)
				continue
			}
			out = append(out, calEvent)
		}
	}
	return out
}

func calendarEventFromVEvent(event ical.Event) (CalendarEvent, error) {
	uid, err := textProp(event.Props, ical.PropUID)
	if err != nil {
		return CalendarEvent{}, fmt.Errorf("uid: %w", err)
	}
	summary, err := textProp(event.Props, ical.PropSummary)
	if err != nil {
		return CalendarEvent{}, fmt.Errorf("summary: %w", err)
	}
	location, err := textProp(event.Props, ical.PropLocation)
	if err != nil {
		return CalendarEvent{}, fmt.Errorf("location: %w", err)
	}
	start, err := event.DateTimeStart(time.Local)
	if err != nil {
		return CalendarEvent{}, fmt.Errorf("dtstart: %w", err)
	}

	endText := ""
	if event.Props.Get(ical.PropDateTimeEnd) != nil {
		end, err := event.DateTimeEnd(time.Local)
		if err != nil {
			return CalendarEvent{}, fmt.Errorf("dtend: %w", err)
		}
		endText = end.Format(time.RFC3339)
	}

	return CalendarEvent{
		UID:      uid,
		Summary:  summary,
		Location: location,
		Start:    start.Format(time.RFC3339),
		End:      endText,
	}, nil
}

func textProp(props ical.Props, name string) (string, error) {
	if props.Get(name) == nil {
		return "", nil
	}
	return props.Text(name)
}
