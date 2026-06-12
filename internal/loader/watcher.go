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
			// A definition directory created after startup (e.g. the user
			// adds user/toolchains/ for the first time) must join the watch
			// set, or files written into it are invisible until restart.
			// Its appearance also schedules a reload — the creating tool
			// typically writes files into it within the debounce window.
			if event.Has(fsnotify.Create) && w.isWatchableDir(event.Name) {
				_ = w.watcher.Add(event.Name)
				if !debounceTimer.Stop() {
					select {
					case <-debounceTimer.C:
					default:
					}
				}
				debounceTimer.Reset(w.debounce)
				continue
			}
			// React to .md file changes (definitions) and .yaml/.yml changes
			// in providers/, {system,user}/graphs/, and {system,user}/schemas/.
			isYAML := strings.HasSuffix(event.Name, ".yaml") || strings.HasSuffix(event.Name, ".yml")
			inYAMLDir := false
			if isYAML {
				for _, d := range []string{
					filepath.Join(w.loader.configDir, "providers"),
					filepath.Join(w.loader.configDir, "system", "graphs"),
					filepath.Join(w.loader.configDir, "user", "graphs"),
					filepath.Join(w.loader.configDir, "system", "schemas"),
					filepath.Join(w.loader.configDir, "user", "schemas"),
				} {
					if strings.HasPrefix(event.Name, d) {
						inYAMLDir = true
						break
					}
				}
			}
			if strings.HasSuffix(event.Name, ".md") || inYAMLDir {
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

// watchDirs returns every directory the watcher cares about. The configDir
// root and the system/user roots are included so that definition directories
// created AFTER startup produce Create events the watcher can react to.
func (w *Watcher) watchDirs() []string {
	configDir := w.loader.configDir
	return []string{
		configDir,
		filepath.Join(configDir, "system"),
		filepath.Join(configDir, "system", "skills"),
		filepath.Join(configDir, "system", "graphs"),
		filepath.Join(configDir, "system", "roles"),
		filepath.Join(configDir, "system", "instructions"),
		filepath.Join(configDir, "system", "toolchains"),
		filepath.Join(configDir, "system", "schemas"),
		filepath.Join(configDir, "user"),
		filepath.Join(configDir, "user", "skills"),
		filepath.Join(configDir, "user", "graphs"),
		filepath.Join(configDir, "user", "roles"),
		filepath.Join(configDir, "user", "instructions"),
		filepath.Join(configDir, "user", "toolchains"),
		filepath.Join(configDir, "user", "schemas"),
		filepath.Join(configDir, "providers"),
	}
}

// isWatchableDir reports whether path is one of the definition directories
// the watcher wants in its watch set.
func (w *Watcher) isWatchableDir(path string) bool {
	for _, d := range w.watchDirs() {
		if path == d {
			return true
		}
	}
	return false
}

// addWatchDirs adds all relevant config directories to the fsnotify watcher.
// Directories that don't exist are silently skipped; they are picked up by
// the Create handling in Start if they appear later.
func (w *Watcher) addWatchDirs() {
	for _, dir := range w.watchDirs() {
		_ = w.watcher.Add(dir) // best-effort; skip if missing
	}
}
