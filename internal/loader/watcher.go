package loader

import (
	"context"
	"log/slog"
	"os"
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
			// Dynamically watch new directories under user/teams/.
			if event.Op&fsnotify.Create != 0 {
				w.maybeWatchNewDir(event.Name)
			}
			// Only react to .md file changes.
			if strings.HasSuffix(event.Name, ".md") {
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

	// Fixed directories to watch.
	dirs := []string{
		filepath.Join(configDir, "system"),
		filepath.Join(configDir, "system", "agents"),
		filepath.Join(configDir, "system", "skills"),
		filepath.Join(configDir, "user", "skills"),
		filepath.Join(configDir, "user", "agents"),
		filepath.Join(configDir, "user", "teams"),
	}

	for _, dir := range dirs {
		_ = w.watcher.Add(dir) // best-effort; skip if missing
	}

	// Watch each team directory and its agents/ subdirectory.
	teamsDir := filepath.Join(configDir, "user", "teams")
	w.watchTeamSubdirs(teamsDir)
}

// watchTeamSubdirs adds watches for each team directory and its agents/ subdir.
func (w *Watcher) watchTeamSubdirs(teamsDir string) {
	entries, err := os.ReadDir(teamsDir)
	if err != nil {
		return // directory doesn't exist — skip
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		teamDir := filepath.Join(teamsDir, e.Name())
		_ = w.watcher.Add(teamDir)
		agentsDir := filepath.Join(teamDir, "agents")
		_ = w.watcher.Add(agentsDir)
	}
}

// maybeWatchNewDir adds a newly created directory to the watch list if it's
// under user/teams/. This handles dynamically created team directories.
func (w *Watcher) maybeWatchNewDir(path string) {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return
	}

	teamsDir := filepath.Join(w.loader.configDir, "user", "teams")
	rel, err := filepath.Rel(teamsDir, path)
	if err != nil {
		return
	}
	// Only watch directories that are under user/teams/ (not outside it).
	if strings.HasPrefix(rel, "..") {
		return
	}

	_ = w.watcher.Add(path)
}
