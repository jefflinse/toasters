package loader

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

const debounceDuration = 200 * time.Millisecond

// Watcher watches config directories for changes and triggers reloads.
type Watcher struct {
	loader   *Loader
	watcher  *fsnotify.Watcher
	debounce time.Duration
	onChange func() // callback when definitions are reloaded
}

// NewWatcher creates a file watcher that triggers loader.Load on changes.
// The onChange callback is invoked after a successful reload.
func NewWatcher(loader *Loader, onChange func()) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &Watcher{
		loader:   loader,
		watcher:  fw,
		debounce: debounceDuration,
		onChange: onChange,
	}, nil
}

// Start begins watching config directories for .md file changes.
// It blocks until ctx is cancelled. Call Stop to release resources.
func (w *Watcher) Start(ctx context.Context) error {
	w.addWatchDirs()

	// Use a stopped timer for debouncing. Reset it on each .md change.
	// The reload runs on this goroutine — never concurrently with itself.
	debounceTimer := time.NewTimer(0)
	if !debounceTimer.Stop() {
		<-debounceTimer.C
	}
	defer debounceTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-w.watcher.Events:
			if !ok {
				return nil
			}
			// React to .md file changes (definitions) and .yaml file changes
			// in providers/ or {system,user}/graphs/.
			isProviderYAML := strings.HasSuffix(event.Name, ".yaml") &&
				strings.HasPrefix(event.Name, filepath.Join(w.loader.configDir, "providers"))
			isGraphYAML := strings.HasSuffix(event.Name, ".yaml") &&
				(strings.HasPrefix(event.Name, filepath.Join(w.loader.configDir, "system", "graphs")) ||
					strings.HasPrefix(event.Name, filepath.Join(w.loader.configDir, "user", "graphs")))
			if strings.HasSuffix(event.Name, ".md") || isProviderYAML || isGraphYAML {
				if !debounceTimer.Stop() {
					select {
					case <-debounceTimer.C:
					default:
					}
				}
				debounceTimer.Reset(w.debounce)
			}
		case <-debounceTimer.C:
			if err := w.loader.Load(ctx); err != nil {
				slog.Error("loader watcher reload failed", "error", err)
				continue
			}
			if w.onChange != nil {
				w.onChange()
			}
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return nil
			}
			slog.Error("loader watcher error", "error", err)
		}
	}
}

// Stop stops the watcher and releases resources.
func (w *Watcher) Stop() error {
	return w.watcher.Close()
}

// addWatchDirs adds all relevant config directories to the fsnotify watcher.
// Directories that don't exist are silently skipped.
func (w *Watcher) addWatchDirs() {
	configDir := w.loader.configDir

	dirs := []string{
		filepath.Join(configDir, "system"),
		filepath.Join(configDir, "system", "skills"),
		filepath.Join(configDir, "system", "graphs"),
		filepath.Join(configDir, "system", "roles"),
		filepath.Join(configDir, "system", "instructions"),
		filepath.Join(configDir, "user", "skills"),
		filepath.Join(configDir, "user", "graphs"),
		filepath.Join(configDir, "user", "roles"),
		filepath.Join(configDir, "user", "instructions"),
		filepath.Join(configDir, "providers"),
	}

	for _, dir := range dirs {
		_ = w.watcher.Add(dir) // best-effort; skip if missing
	}
}
