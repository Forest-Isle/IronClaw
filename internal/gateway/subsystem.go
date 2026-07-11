package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// Subsystem is a self-contained module with its own lifecycle.
type Subsystem interface {
	Name() string
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// Subsystems is an ordered collection of Subsystem instances.
type Subsystems []Subsystem

// StartAll starts each subsystem in order. If any subsystem fails, it returns
// the error immediately; previously started subsystems are NOT stopped (the
// caller should call StopAll on error to clean up).
func (ss Subsystems) StartAll(ctx context.Context) error {
	for _, s := range ss {
		if err := s.Start(ctx); err != nil {
			return fmt.Errorf("subsystem %s: %w", s.Name(), err)
		}
		slog.Debug("subsystem started", "name", s.Name())
	}
	return nil
}

// StopAll stops each subsystem in reverse order and joins any errors after all
// subsystems have had a chance to shut down.
func (ss Subsystems) StopAll(ctx context.Context) error {
	var errs []error
	for i := len(ss) - 1; i >= 0; i-- {
		if err := ss[i].Stop(ctx); err != nil {
			slog.Warn("subsystem stop error", "name", ss[i].Name(), "error", err)
			errs = append(errs, fmt.Errorf("subsystem %s: %w", ss[i].Name(), err))
		} else {
			slog.Debug("subsystem stopped", "name", ss[i].Name())
		}
	}
	return errors.Join(errs...)
}
