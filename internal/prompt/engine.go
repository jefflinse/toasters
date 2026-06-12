// Package prompt composes worker system prompts from reusable templates.
//
// The composition model has three kinds of definitions:
//
//   - Roles (roles/*.md): worker identity + domain behavior. The top-level
//     template that references toolchains and instructions via {{ }} syntax.
//   - Toolchains (toolchains/*.md): language/framework knowledge with typed
//     vars that can be overridden at composition time.
//   - Instructions (instructions/*.md): reusable behavioral directives.
//     Plain markdown, no frontmatter, no vars.
//
// Template syntax uses {{ root.name }} references. Three roots are reserved
// for special lookups; every other reference is a data lookup into the
// per-compose data bag (task fields, node outputs, the current time, …):
//
//	{{ toolchains.go }}          → inlines the Go toolchain body   (reserved)
//	{{ instructions.do-exact }}  → inlines the instruction body    (reserved)
//	{{ slots.lens }}             → a slot bound at compose time     (reserved)
//	{{ task.description }}        → a data value (any non-reserved root)
//	{{ now.year }}               → a data value (current year)
//	{{ fanout.candidates }}      → a data value (reducer-injected)
//
// (Within a toolchain body, {{ vars.version }} resolves the toolchain's vars.)
package prompt

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Engine loads and composes worker prompts from roles, toolchains, and instructions.
//
// Concurrency: all maps are guarded by mu. The definition maps (roles,
// toolchains, instructions, schemas) are mutated during LoadDir and swapped
// wholesale by Reload — never mutated in place after that — so readers may
// snapshot a map reference under the lock and keep using it lock-free.
type Engine struct {
	mu           sync.RWMutex
	roles        map[string]*Role
	toolchains   map[string]*Toolchain
	instructions map[string]string // name → body (plain text)
	schemas      map[string]*Schema
	globals      map[string]string // caller-set globals (e.g. config values)

	// dirs records every LoadDir call so Reload can re-read the same
	// directories in the same precedence order.
	dirs []loadedDir
	// synthetic holds instructions registered at runtime via SetInstruction
	// (e.g. the selected granularity level). Overlaid on top of disk
	// instructions after a Reload so they survive file-watcher refreshes.
	synthetic map[string]string
}

// loadedDir is one recorded LoadDir invocation.
type loadedDir struct {
	dir    string
	source string
}

// Role is a worker definition with template references.
type Role struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Mode        string   `yaml:"mode"`
	Tools       []string `yaml:"tools"`
	// Output names a Schema registered in the engine. Graph nodes use this
	// to constrain the LLM's terminal output. Empty means the default
	// one-field summary schema (see prompt.DefaultSchemaName).
	Output string `yaml:"output"`
	// Access selects the toolset a graph node built from this role gets at
	// run time. One of "readonly" (default), "write", "test", or "all".
	Access string `yaml:"access"`
	// MaxTurns bounds the number of model round-trips (assistant →
	// tool-dispatch → assistant …) in a single node execution. Zero falls
	// back to the mycelium default (agent.DefaultMaxTurns = 20). Roles
	// with heavy tool-call budgets — scaffolders, coders, testers —
	// should set this higher; pure analytical roles (investigator,
	// planner, reviewer) are fine at the default.
	MaxTurns int `yaml:"max_turns"`
	// Thinking, when set, overrides the global worker_thinking_enabled
	// default for this role. Pointer so that "absent" (use global) is
	// distinguishable from "explicitly false".
	Thinking *bool `yaml:"thinking,omitempty"`
	// Temperature, when set, overrides the global worker_temperature
	// default for this role. Pointer so that 0.0 is distinguishable from
	// "use global default".
	Temperature *float64 `yaml:"temperature,omitempty"`
	// Slots names parameterized fillers that callers must bind at compose
	// time. Each name corresponds to a {{ slots.<name> }} reference in the
	// body. The bound value is either a toolchain id (which inlines that
	// toolchain's resolved body) or arbitrary text (substituted literally).
	Slots  []string `yaml:"slots,omitempty"`
	Body   string   `yaml:"-"` // template text after frontmatter
	Source string   `yaml:"-"` // "system" or "user" — set by LoadDir caller
}

// Toolchain is language/framework knowledge with typed variables.
type Toolchain struct {
	ID          string            `yaml:"id"`
	Name        string            `yaml:"name"`
	Description string            `yaml:"description"`
	Vars        map[string]VarDef `yaml:"vars"`
	Body        string            `yaml:"-"` // template text after frontmatter
}

// VarDef defines a toolchain variable with a description and default value.
type VarDef struct {
	Description string `yaml:"description"`
	Default     string `yaml:"default"`
}

// templateRef matches {{ category.name }} and {{ category.name.subname }}.
var templateRef = regexp.MustCompile(`\{\{\s*([\w-]+)\.([\w.-]+)\s*\}\}`)

// NewEngine creates an empty Engine.
func NewEngine() *Engine {
	return &Engine{
		roles:        make(map[string]*Role),
		toolchains:   make(map[string]*Toolchain),
		instructions: make(map[string]string),
		schemas:      make(map[string]*Schema),
		globals:      make(map[string]string),
		synthetic:    make(map[string]string),
	}
}

// SetGlobal registers a global template variable that will be available as
// {{ <key> }} in role templates. Safe to call concurrently with
// Compose.
func (e *Engine) SetGlobal(key, value string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.globals[key] = value
}

// SetInstruction registers a synthetic instruction body under name so that
// role templates referencing {{ instructions.<name> }} resolve to body.
// Intended for runtime-injected instructions whose content comes from
// configuration (e.g. the selected worker-granularity level). Safe to call
// concurrently with Compose.
func (e *Engine) SetInstruction(name, body string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	trimmed := strings.TrimSpace(body)
	e.instructions[name] = trimmed
	e.synthetic[name] = trimmed
}

// Instruction returns the body of the instruction registered under name, if
// any. The second return is false when no such instruction was loaded.
func (e *Engine) Instruction(name string) (string, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	body, ok := e.instructions[name]
	return body, ok
}

// LoadDir loads all definitions from a directory containing roles/, toolchains/,
// and instructions/ subdirectories. Missing subdirectories are silently skipped.
// The source tag ("system" or "user") is set on all loaded roles for access control.
// The directory is recorded so Reload can re-read it later.
func (e *Engine) LoadDir(dir, source string) error {
	if err := e.loadRoles(filepath.Join(dir, "roles"), source); err != nil {
		return fmt.Errorf("loading roles: %w", err)
	}
	if err := e.loadToolchains(filepath.Join(dir, "toolchains")); err != nil {
		return fmt.Errorf("loading toolchains: %w", err)
	}
	if err := e.loadInstructions(filepath.Join(dir, "instructions")); err != nil {
		return fmt.Errorf("loading instructions: %w", err)
	}
	if err := e.loadSchemas(filepath.Join(dir, "schemas")); err != nil {
		return fmt.Errorf("loading schemas: %w", err)
	}
	e.mu.Lock()
	e.dirs = append(e.dirs, loadedDir{dir: dir, source: source})
	e.mu.Unlock()
	return nil
}

// Reload re-reads every directory previously registered via LoadDir into
// fresh definition maps and swaps them in atomically. Synthetic instructions
// registered via SetInstruction are re-overlaid so runtime-injected content
// (granularity levels) survives. Globals are untouched. On error the
// previous definitions remain in effect.
//
// This is what makes the file watcher's "definitions reloaded" true for
// roles, toolchains, instructions, and schemas — without it the prompt
// engine loaded once at boot and edits silently did nothing until restart.
func (e *Engine) Reload() error {
	e.mu.RLock()
	dirs := make([]loadedDir, len(e.dirs))
	copy(dirs, e.dirs)
	synthetic := make(map[string]string, len(e.synthetic))
	for k, v := range e.synthetic {
		synthetic[k] = v
	}
	e.mu.RUnlock()

	fresh := NewEngine()
	for _, d := range dirs {
		if err := fresh.LoadDir(d.dir, d.source); err != nil {
			return fmt.Errorf("reloading %s definitions from %s: %w", d.source, d.dir, err)
		}
	}
	for k, v := range synthetic {
		fresh.instructions[k] = v
	}

	e.mu.Lock()
	e.roles = fresh.roles
	e.toolchains = fresh.toolchains
	e.instructions = fresh.instructions
	e.schemas = fresh.schemas
	e.mu.Unlock()
	return nil
}

// Compose resolves a role's template references and returns the fully composed
// system prompt. Overrides are passed to toolchain var resolution (e.g.
// {"go.version": "1.25"} overrides the Go toolchain's version var). Slots
// binds each name declared in the role's frontmatter to a concrete value
// (currently always a toolchain id); every declared slot must have a
// binding or Compose returns an error. A slot bound to a loaded toolchain id
// inlines that toolchain's body at {{ slots.<name> }}; any other binding is
// substituted as literal text.
func (e *Engine) Compose(roleName string, overrides map[string]string, slots map[string]string) (string, error) {
	e.mu.RLock()
	role, ok := e.roles[roleName]
	if !ok {
		e.mu.RUnlock()
		return "", fmt.Errorf("role %q not found", roleName)
	}

	// Snapshot the runtime-mutable maps under the lock; compose against the
	// snapshot so the lock isn't held during regex work. toolchains is a map
	// reference — safe because Reload swaps the map instead of mutating it.
	now := time.Now()
	globals := make(map[string]string, len(e.globals)+3)
	for k, v := range e.globals {
		globals[k] = v
	}
	instructions := make(map[string]string, len(e.instructions))
	for k, v := range e.instructions {
		instructions[k] = v
	}
	toolchains := e.toolchains
	e.mu.RUnlock()

	globals["now.month"] = now.Format("January")
	globals["now.year"] = fmt.Sprintf("%d", now.Year())
	globals["now.date"] = now.Format("2006-01-02")
	// Per-call overrides take highest precedence (also used for toolchain vars).
	for k, v := range overrides {
		globals[k] = v
	}

	// Pre-resolve all toolchain bodies with their vars.
	resolvedToolchains := make(map[string]string, len(toolchains))
	for id, tc := range toolchains {
		resolved, err := e.resolveToolchain(tc, overrides)
		if err != nil {
			return "", fmt.Errorf("resolving toolchain %q: %w", id, err)
		}
		resolvedToolchains[id] = resolved
	}

	// Strict validation: every slot the role declares must have a binding,
	// and every binding must point to a loaded toolchain. We catch this up
	// front so jobs fail before spending tokens.
	declared := make(map[string]struct{}, len(role.Slots))
	for _, name := range role.Slots {
		declared[name] = struct{}{}
		if _, ok := slots[name]; !ok {
			return "", fmt.Errorf("role %q: slot %q declared but not bound", roleName, name)
		}
	}
	for name := range slots {
		if _, ok := declared[name]; !ok {
			slog.Warn("ignoring slot binding for undeclared slot", "role", roleName, "slot", name)
		}
	}

	// Resolve the role body.
	var resolveErr error
	result := templateRef.ReplaceAllStringFunc(role.Body, func(match string) string {
		if resolveErr != nil {
			return match
		}
		parts := templateRef.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		category, name := parts[1], parts[2]

		switch category {
		case "toolchains":
			if body, ok := resolvedToolchains[name]; ok {
				return strings.TrimSpace(body)
			}
			slog.Warn("unresolved toolchain reference; substituting empty", "role", roleName, "ref", name)
			return ""
		case "instructions":
			if body, ok := instructions[name]; ok {
				return strings.TrimSpace(body)
			}
			slog.Warn("unresolved instruction reference; substituting empty", "role", roleName, "ref", name)
			return ""
		case "slots":
			if _, ok := declared[name]; !ok {
				resolveErr = fmt.Errorf("role %q references slot %q not declared in frontmatter", roleName, name)
				return match
			}
			val := slots[name]
			// A slot value that names a loaded toolchain inlines that
			// toolchain's resolved body; any other value is literal text.
			if body, ok := resolvedToolchains[val]; ok {
				return strings.TrimSpace(body)
			}
			return val
		default:
			// toolchains/instructions/slots are the reserved roots; every
			// other reference is a data lookup keyed by the full dotted name
			// (e.g. task.description, fanout.candidates, now.year, job.title).
			key := category + "." + name
			if val, ok := globals[key]; ok {
				return val
			}
			slog.Warn("unresolved data reference; substituting empty", "role", roleName, "ref", key)
			return ""
		}
	})
	if resolveErr != nil {
		return "", resolveErr
	}

	return strings.TrimSpace(result), nil
}

// Role returns a role by name, or nil if not found. The returned Role is an
// immutable snapshot — Reload swaps the map rather than mutating entries.
func (e *Engine) Role(name string) *Role {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.roles[name]
}

// Roles returns all loaded role names.
func (e *Engine) Roles() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	names := make([]string, 0, len(e.roles))
	for name := range e.roles {
		names = append(names, name)
	}
	return names
}

// Instructions returns a copy of the loaded instruction bodies, keyed by name.
// Used by callers that resolve {{ instructions.<name> }} references outside the
// role-body template — e.g. an instruction-valued slot binding.
func (e *Engine) Instructions() map[string]string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make(map[string]string, len(e.instructions))
	for k, v := range e.instructions {
		out[k] = v
	}
	return out
}

// Toolchains returns all loaded toolchain ids in stable (sorted) order.
// Used by callers that need to enumerate the catalog (e.g. fine-decompose
// surfacing valid toolchain ids to the LLM).
func (e *Engine) Toolchains() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	ids := make([]string, 0, len(e.toolchains))
	for id := range e.toolchains {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// resolveToolchain resolves {{ vars.X }} references in a toolchain body.
func (e *Engine) resolveToolchain(tc *Toolchain, overrides map[string]string) (string, error) {
	return templateRef.ReplaceAllStringFunc(tc.Body, func(match string) string {
		parts := templateRef.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		category, name := parts[1], parts[2]

		if category != "vars" {
			return match // only vars are resolved in toolchains
		}

		// Check overrides first (format: "toolchainID.varName").
		overrideKey := tc.ID + "." + name
		if val, ok := overrides[overrideKey]; ok {
			return val
		}

		// Fall back to default.
		if varDef, ok := tc.Vars[name]; ok {
			return varDef.Default
		}

		slog.Warn("unresolved var reference", "toolchain", tc.ID, "var", name)
		return match
	}), nil
}

// loadRoles loads all .md files from the roles directory.
func (e *Engine) loadRoles(dir, source string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("skipping unreadable role file", "path", path, "error", err)
			continue
		}

		role := &Role{}
		body, err := parseFrontmatter(data, role)
		if err != nil {
			slog.Warn("skipping unparseable role file", "path", path, "error", err)
			continue
		}
		role.Body = body
		role.Source = source

		// Use filename stem as key if no name in frontmatter.
		key := strings.TrimSuffix(entry.Name(), ".md")
		if role.Name != "" {
			// Also register by slugified name for lookup.
			e.roles[slugify(role.Name)] = role
		}
		e.roles[key] = role
	}
	return nil
}

// loadToolchains loads all .md files from the toolchains directory.
func (e *Engine) loadToolchains(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("skipping unreadable toolchain file", "path", path, "error", err)
			continue
		}

		tc := &Toolchain{}
		body, err := parseFrontmatter(data, tc)
		if err != nil {
			slog.Warn("skipping unparseable toolchain file", "path", path, "error", err)
			continue
		}
		tc.Body = body

		// Use ID from frontmatter, falling back to filename stem.
		key := tc.ID
		if key == "" {
			key = strings.TrimSuffix(entry.Name(), ".md")
			tc.ID = key
		}
		e.toolchains[key] = tc
	}
	return nil
}

// loadInstructions loads all .md files from the instructions directory.
// Instructions are plain text — no YAML frontmatter.
func (e *Engine) loadInstructions(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("skipping unreadable instruction file", "path", path, "error", err)
			continue
		}

		key := strings.TrimSuffix(entry.Name(), ".md")
		e.instructions[key] = strings.TrimSpace(string(data))
	}
	return nil
}

// parseFrontmatter splits a markdown file into YAML frontmatter and body.
// The frontmatter is unmarshaled into dest. Returns the body text.
func parseFrontmatter(data []byte, dest any) (string, error) {
	content := string(data)

	if !strings.HasPrefix(content, "---\n") {
		// No frontmatter — entire content is body.
		return content, nil
	}

	rest := content[4:] // skip opening "---\n"
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return content, nil // malformed — treat as body
	}

	fm := rest[:idx]
	body := rest[idx+4:] // skip "\n---"

	if err := yaml.Unmarshal([]byte(fm), dest); err != nil {
		return "", fmt.Errorf("parsing frontmatter: %w", err)
	}

	return strings.TrimSpace(body), nil
}

// slugify converts a name to a filesystem-safe lowercase slug.
func slugify(name string) string {
	s := strings.ToLower(name)
	s = strings.ReplaceAll(s, " ", "-")
	// Remove anything that's not alphanumeric or hyphen.
	var buf strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			buf.WriteRune(r)
		}
	}
	return buf.String()
}
