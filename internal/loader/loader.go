// Package loader walks config directories, parses definition files with
// mdfmt, resolves cross-references, and rebuilds the database via
// db.Store.RebuildDefinitions.
package loader

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/graphexec"
	"github.com/jefflinse/toasters/internal/mdfmt"
	"github.com/jefflinse/toasters/internal/prompt"
	"github.com/jefflinse/toasters/internal/provider"

	"gopkg.in/yaml.v3"
)

// Loader walks config directories and loads definitions into the database.
type Loader struct {
	store        db.Store
	configDir    string
	promptEngine *prompt.Engine // optional; for role-based team loading

	provMu    sync.RWMutex
	providers []provider.ProviderConfig

	graphsMu sync.RWMutex
	graphs   []*graphexec.Definition
}

// New creates a new Loader.
func New(store db.Store, configDir string) *Loader {
	return &Loader{store: store, configDir: configDir}
}

// SetPromptEngine sets the prompt engine for role-based team loading.
func (l *Loader) SetPromptEngine(e *prompt.Engine) {
	l.promptEngine = e
}

// Providers returns the most recently loaded provider configs.
func (l *Loader) Providers() []provider.ProviderConfig {
	l.provMu.RLock()
	defer l.provMu.RUnlock()
	out := make([]provider.ProviderConfig, len(l.providers))
	copy(out, l.providers)
	return out
}

// Graphs returns the most recently loaded graph definitions. User graphs
// shadow system graphs with the same ID.
func (l *Loader) Graphs() []*graphexec.Definition {
	l.graphsMu.RLock()
	defer l.graphsMu.RUnlock()
	out := make([]*graphexec.Definition, len(l.graphs))
	copy(out, l.graphs)
	return out
}

// GraphByID returns the graph definition with the given ID, or nil if
// none is loaded. Satisfies graphexec.GraphSource so *Loader can be
// passed directly into the executor.
func (l *Loader) GraphByID(id string) *graphexec.Definition {
	l.graphsMu.RLock()
	defer l.graphsMu.RUnlock()
	for _, g := range l.graphs {
		if g.ID == id {
			return g
		}
	}
	return nil
}

// Load walks all directories, parses definitions, resolves references,
// and rebuilds the database. It is idempotent.
func (l *Loader) Load(ctx context.Context) error {
	var skills []*db.Skill

	// 1. System skills.
	systemDir := filepath.Join(l.configDir, "system")
	systemSkills := l.loadSkills(filepath.Join(systemDir, "skills"), "system")
	skills = append(skills, systemSkills...)

	// 2. User skills shadow system skills with the same ID.
	userSkills := l.loadSkills(filepath.Join(l.configDir, "user", "skills"), "user")
	userSkillIDs := make(map[string]bool, len(userSkills))
	for _, sk := range userSkills {
		userSkillIDs[sk.ID] = true
	}
	filtered := skills[:0]
	for _, sk := range skills {
		if !userSkillIDs[sk.ID] {
			filtered = append(filtered, sk)
		}
	}
	skills = append(filtered, userSkills...)

	// 3. Load providers from providers/*.yaml.
	provs := l.loadProviders()
	l.provMu.Lock()
	l.providers = provs
	l.provMu.Unlock()

	// 4. Load graphs from system/graphs/ and user/graphs/. User graphs
	// shadow system graphs sharing an id.
	sysGraphs := l.loadGraphs(filepath.Join(l.configDir, "system", "graphs"))
	userGraphs := l.loadGraphs(filepath.Join(l.configDir, "user", "graphs"))
	merged := mergeGraphs(sysGraphs, userGraphs)
	l.graphsMu.Lock()
	l.graphs = merged
	l.graphsMu.Unlock()

	// 5. Rebuild database. Workers are no longer loaded from disk — graphs
	// are the execution primitive now.
	if err := l.store.RebuildDefinitions(ctx, skills, nil); err != nil {
		return fmt.Errorf("rebuilding definitions: %w", err)
	}

	return nil
}

// loadProviders reads all .yaml files in the providers/ directory.
func (l *Loader) loadProviders() []provider.ProviderConfig {
	dir := filepath.Join(l.configDir, "providers")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var configs []provider.ProviderConfig
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if info, err := e.Info(); err == nil && info.Size() > maxDefinitionFileSize {
			slog.Warn("skipping oversized provider file", "path", path, "size", info.Size())
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("skipping unreadable provider file", "path", path, "error", err)
			continue
		}
		var pc provider.ProviderConfig
		if err := yaml.Unmarshal(data, &pc); err != nil {
			slog.Warn("skipping unparseable provider file", "path", path, "error", err)
			continue
		}
		if pc.ID == "" && pc.Name == "" {
			slog.Warn("skipping provider file with no id or name", "path", path)
			continue
		}
		configs = append(configs, pc)
	}
	return configs
}

// loadGraphs reads all .yaml files in dir as graphexec.Definitions. Invalid
// or oversized files are skipped with a warning so one bad graph can't
// break the whole load.
func (l *Loader) loadGraphs(dir string) []*graphexec.Definition {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var graphs []*graphexec.Definition
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if info, err := e.Info(); err == nil && info.Size() > maxDefinitionFileSize {
			slog.Warn("skipping oversized graph file", "path", path, "size", info.Size())
			continue
		}
		def, err := graphexec.LoadDefinition(path)
		if err != nil {
			slog.Warn("skipping invalid graph file", "path", path, "error", err)
			continue
		}
		graphs = append(graphs, def)
	}
	return graphs
}

// mergeGraphs composes system and user graph lists, with user entries
// shadowing system entries of the same id. Preserves user order after
// appending system entries that survived shadowing.
func mergeGraphs(system, user []*graphexec.Definition) []*graphexec.Definition {
	userIDs := make(map[string]struct{}, len(user))
	for _, g := range user {
		userIDs[g.ID] = struct{}{}
	}
	out := make([]*graphexec.Definition, 0, len(system)+len(user))
	for _, g := range system {
		if _, shadowed := userIDs[g.ID]; shadowed {
			continue
		}
		out = append(out, g)
	}
	out = append(out, user...)
	return out
}

// maxDefinitionFileSize is the maximum size (in bytes) for agent/skill/team
// definition files. Files larger than this are skipped to prevent excessive
// memory allocation from malicious or accidentally large files.
const maxDefinitionFileSize = 1 << 20 // 1 MiB

// loadSkills parses all .md files in dir as SkillDefs.
// Uses ParseSkill directly since directory context determines the type.
func (l *Loader) loadSkills(dir, source string) []*db.Skill {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Directory doesn't exist — skip silently.
		return nil
	}

	var skills []*db.Skill
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		// Skip symlinks to prevent reading files outside the config directory.
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
			slog.Warn("skipping symlink in skills directory", "path", path)
			continue
		}
		if info, err := e.Info(); err == nil && info.Size() > maxDefinitionFileSize {
			slog.Warn("skipping oversized definition file", "path", path, "size", info.Size())
			continue
		}
		sd, err := mdfmt.ParseSkill(path)
		if err != nil {
			slog.Warn("skipping unparseable skill file", "path", path, "error", err)
			continue
		}
		skills = append(skills, convertSkill(sd, source, path))
	}
	return skills
}

// --- Conversion helpers ---

func convertSkill(sd *mdfmt.SkillDef, source, path string) *db.Skill {
	return &db.Skill{
		ID:          Slugify(sd.Name),
		Name:        sd.Name,
		Description: sd.Description,
		Tools:       marshalJSON(sd.Tools),
		Prompt:      sd.Body,
		Source:      source,
		SourcePath:  path,
	}
}

// marshalJSON marshals v to json.RawMessage. Returns nil for nil/empty values.
func marshalJSON(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	// Don't store empty arrays/objects/null.
	s := string(data)
	if s == "null" || s == "[]" || s == "{}" {
		return nil
	}
	return data
}

// --- Slugify ---

var (
	nonAlphanumHyphen = regexp.MustCompile(`[^a-z0-9-]`)
	multipleHyphens   = regexp.MustCompile(`-{2,}`)
)

// Slugify converts a human-readable name to a filesystem-safe slug suitable for
// use as a filename. It lowercases the input, replaces spaces with hyphens, strips
// non-alphanumeric characters, and collapses consecutive hyphens.
//
// Examples: "Go Development" → "go-development", "Senior Go Dev" → "senior-go-dev".
//
// Slugify is exported because TUI CRUD operations in internal/tui use it to generate
// consistent filenames when creating skills, agents, and teams on disk.
func Slugify(name string) string {
	s := strings.ToLower(name)
	s = strings.ReplaceAll(s, " ", "-")
	s = nonAlphanumHyphen.ReplaceAllString(s, "")
	s = multipleHyphens.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}
