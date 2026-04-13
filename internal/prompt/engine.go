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
// Template syntax uses {{ category.name }} references:
//
//	{{ toolchains.go }}                       → inlines the Go toolchain body
//	{{ instructions.do-exact }}               → inlines the instruction body
//	{{ globals.now.month }}                   → runtime value (current month name)
//	{{ globals.now.year }}                    → runtime value (current year)
//	{{ vars.version }}                        → toolchain variable (within toolchain body)
package prompt

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Engine loads and composes worker prompts from roles, toolchains, and instructions.
type Engine struct {
	roles        map[string]*Role
	toolchains   map[string]*Toolchain
	instructions map[string]string // name → body (plain text)
}

// Role is a worker definition with template references.
type Role struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Mode        string   `yaml:"mode"`
	Tools       []string `yaml:"tools"`
	Body        string   `yaml:"-"` // template text after frontmatter
	Source      string   `yaml:"-"` // "system" or "user" — set by LoadDir caller
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
	}
}

// LoadDir loads all definitions from a directory containing roles/, toolchains/,
// and instructions/ subdirectories. Missing subdirectories are silently skipped.
// The source tag ("system" or "user") is set on all loaded roles for access control.
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
	return nil
}

// Compose resolves a role's template references and returns the fully composed
// system prompt. Overrides are passed to toolchain var resolution (e.g.
// {"go.version": "1.25"} overrides the Go toolchain's version var).
func (e *Engine) Compose(roleName string, overrides map[string]string) (string, error) {
	role, ok := e.roles[roleName]
	if !ok {
		return "", fmt.Errorf("role %q not found", roleName)
	}

	// Build the globals map.
	now := time.Now()
	globals := map[string]string{
		"now.month": now.Format("January"),
		"now.year":  fmt.Sprintf("%d", now.Year()),
		"now.date":  now.Format("2006-01-02"),
	}

	// Pre-resolve all toolchain bodies with their vars.
	resolvedToolchains := make(map[string]string, len(e.toolchains))
	for id, tc := range e.toolchains {
		resolved, err := e.resolveToolchain(tc, overrides)
		if err != nil {
			return "", fmt.Errorf("resolving toolchain %q: %w", id, err)
		}
		resolvedToolchains[id] = resolved
	}

	// Resolve the role body.
	result := templateRef.ReplaceAllStringFunc(role.Body, func(match string) string {
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
			slog.Warn("unresolved toolchain reference", "role", roleName, "ref", name)
			return match
		case "instructions":
			if body, ok := e.instructions[name]; ok {
				return strings.TrimSpace(body)
			}
			slog.Warn("unresolved instruction reference", "role", roleName, "ref", name)
			return match
		case "globals":
			if val, ok := globals[name]; ok {
				return val
			}
			slog.Warn("unresolved global reference", "role", roleName, "ref", name)
			return match
		default:
			slog.Warn("unknown template category", "role", roleName, "category", category)
			return match
		}
	})

	return strings.TrimSpace(result), nil
}

// Role returns a role by name, or nil if not found.
func (e *Engine) Role(name string) *Role {
	return e.roles[name]
}

// Roles returns all loaded role names.
func (e *Engine) Roles() []string {
	names := make([]string, 0, len(e.roles))
	for name := range e.roles {
		names = append(names, name)
	}
	return names
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
