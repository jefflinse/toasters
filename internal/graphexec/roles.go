package graphexec

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/jefflinse/rhizome"
	"github.com/jefflinse/toasters/internal/prompt"
)

// RoleBuilder constructs a rhizome NodeFunc for a single role. The same
// builder is reused across graphs — each call returns a fresh NodeFunc
// bound to the supplied TemplateConfig and node id.
type RoleBuilder func(cfg TemplateConfig, nodeID string) rhizome.NodeFunc[*TaskState]

// RoleRegistry resolves role names to RoleBuilders. The compiler holds one
// registry per compile; tests may inject fakes via Register. Reads are
// concurrency-safe so a single registry can back parallel compiles.
//
// By default the registry has no explicit entries — unregistered roles are
// resolved against the prompt engine supplied in TemplateConfig at compile
// time. Callers that want to pre-validate a role set can Register them, and
// tests can override behavior by shadowing a name with a stub builder.
type RoleRegistry struct {
	mu       sync.RWMutex
	builders map[string]RoleBuilder
}

// NewRoleRegistry returns an empty registry. At compile time unknown names
// fall through to the dynamic builder that reads the role definition from
// the prompt engine.
func NewRoleRegistry() *RoleRegistry {
	return &RoleRegistry{builders: make(map[string]RoleBuilder)}
}

// Register installs or replaces a RoleBuilder under name. Primarily useful
// for tests that want to stub a role's behavior without touching the
// prompt engine.
func (r *RoleRegistry) Register(name string, b RoleBuilder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.builders[name] = b
}

// Build returns a NodeFunc for the named role bound to the supplied nodeID.
// Registered builders take precedence; unregistered names resolve against
// the prompt engine in cfg. Returns an error when neither path yields a
// role definition.
func (r *RoleRegistry) Build(name, nodeID string, cfg TemplateConfig) (rhizome.NodeFunc[*TaskState], error) {
	r.mu.RLock()
	b, ok := r.builders[name]
	r.mu.RUnlock()
	if ok {
		return b(cfg, nodeID), nil
	}
	if cfg.PromptEngine == nil {
		return nil, fmt.Errorf("unknown role %q (no prompt engine configured; registered: %s)", name, strings.Join(r.registered(), ", "))
	}
	role := cfg.PromptEngine.Role(name)
	if role == nil {
		return nil, fmt.Errorf("unknown role %q (not in prompt engine; loaded: %s)", name, strings.Join(cfg.PromptEngine.Roles(), ", "))
	}
	return RoleNode(cfg, role, nodeID), nil
}

// Names returns the explicitly registered role names, sorted alphabetically.
// Dynamic roles resolved through the prompt engine are not included.
func (r *RoleRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.registered()
}

func (r *RoleRegistry) registered() []string {
	out := make([]string, 0, len(r.builders))
	for name := range r.builders {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ResolveSchema returns the JSON Schema for a role's declared output. When
// the role leaves `output:` empty the default summary schema is used.
// Returns an error when neither the named nor the default schema is loaded.
func ResolveSchema(engine *prompt.Engine, role *prompt.Role) ([]byte, *prompt.Schema, error) {
	if engine == nil {
		return nil, nil, fmt.Errorf("no prompt engine configured")
	}
	name := role.Output
	if name == "" {
		name = prompt.DefaultSchemaName
	}
	s := engine.Schema(name)
	if s == nil {
		return nil, nil, fmt.Errorf("role %q references unknown schema %q", role.Name, name)
	}
	raw, err := engine.SchemaJSON(name)
	if err != nil {
		return nil, nil, err
	}
	return raw, s, nil
}
