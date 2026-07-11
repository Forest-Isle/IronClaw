package config

import (
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ConfigWatcher watches a YAML config file for changes and reloads it.
type ConfigWatcher struct {
	mu       sync.RWMutex
	current  *Config
	path     string
	watcher  *fsnotify.Watcher
	onReload []func(*Config) // called after successful reload
	done     chan struct{}
	stopOnce sync.Once
}

// NewConfigWatcher creates a watcher for the given config file path.
// The initial config is loaded from the file.
func NewConfigWatcher(path string) (*ConfigWatcher, error) {
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	// Watch the directory (not the file directly — some editors rename on save)
	if err := w.Add(path); err != nil {
		w.Close()
		return nil, err
	}

	cw := &ConfigWatcher{
		current: cfg,
		path:    path,
		watcher: w,
		done:    make(chan struct{}),
	}

	go cw.loop()

	slog.Info("config: hot-reload watcher started", "path", path)
	return cw, nil
}

// Current returns the current config snapshot (thread-safe).
func (cw *ConfigWatcher) Current() *Config {
	cw.mu.RLock()
	defer cw.mu.RUnlock()
	return cw.current
}

// OnReload registers a callback invoked after each successful config reload.
func (cw *ConfigWatcher) OnReload(fn func(*Config)) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	cw.onReload = append(cw.onReload, fn)
}

// Stop shuts down the watcher.
func (cw *ConfigWatcher) Stop() {
	cw.stopOnce.Do(func() {
		close(cw.done)
		_ = cw.watcher.Close()
	})
}

func (cw *ConfigWatcher) loop() {
	// Debounce: batch rapid writes into a single reload
	var debounce *time.Timer
	const debounceInterval = 200 * time.Millisecond

	for {
		select {
		case <-cw.done:
			return
		case event, ok := <-cw.watcher.Events:
			if !ok {
				return
			}
			// Only reload on Write or Create events
			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}
			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(debounceInterval, func() {
				cw.reload()
			})

		case err, ok := <-cw.watcher.Errors:
			if !ok {
				return
			}
			slog.Warn("config: watcher error", "err", err)
		}
	}
}

func (cw *ConfigWatcher) reload() {
	// Check that file still exists
	if _, err := os.Stat(cw.path); os.IsNotExist(err) {
		slog.Warn("config: watched file disappeared, skipping reload", "path", cw.path)
		return
	}

	newCfg, err := Load(cw.path)
	if err != nil {
		slog.Error("config: reload failed, keeping current config", "err", err, "path", cw.path)
		return
	}

	cw.mu.Lock()
	cw.current = newCfg
	callbacks := make([]func(*Config), len(cw.onReload))
	copy(callbacks, cw.onReload)
	cw.mu.Unlock()

	slog.Info("config: reloaded successfully", "path", cw.path)

	for _, fn := range callbacks {
		fn(newCfg)
	}
}
