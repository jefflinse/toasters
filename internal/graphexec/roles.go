package graphexec

import (
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"

	"github.com/jefflinse/rhizome"
)

// RoleBuilder constructs a rhizome NodeFunc for a single role. The same
// builder is reused across graphs — each call returns a fresh NodeFunc
// bound to the supplied TemplateConfig.
type RoleBuilder func(cfg TemplateConfig) rhizome.NodeFunc[*TaskState]

// defaultRoleBuilders is the built-in registry that maps role names to
// their Go-side node builders. The declarative compiler resolves nodes'
// `role:` field against this map to produce a NodeFunc.
//
// This is a v1 bridge: schemas, tools, and system prompts are currently
// owned by Go code (outputs.go / agent_tools.go) rather than role
// markdown. Later phases will relocate that metadata into role definition
// files so YAML graphs can pull in arbitrary user-defined roles.
var defaultRoleBuilders = map[string]RoleBuilder{
	"investigator": InvestigateNodeDynamic,
	"planner":      PlanNodeDynamic,
	"implementer":  ImplementNodeDynamic,
	"tester":       TestNodeDynamic,
	"reviewer":     ReviewNodeDynamic,
}

// RoleRegistry resolves role names to RoleBuilders. The compiler holds one
// registry per compile; tests may inject fakes via Register. Reads are
// concurrency-safe so a single registry can back parallel compiles.
type RoleRegistry struct {
	mu       sync.RWMutex
	builders map[string]RoleBuilder
}

// NewRoleRegistry returns a registry preloaded with the default builders.
// Callers may override or extend via Register.
func NewRoleRegistry() *RoleRegistry {
	r := &RoleRegistry{builders: make(map[string]RoleBuilder, len(defaultRoleBuilders))}
	maps.Copy(r.builders, defaultRoleBuilders)
	return r
}

// Register installs or replaces a RoleBuilder under name.
func (r *RoleRegistry) Register(name string, b RoleBuilder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.builders[name] = b
}

// Build returns a NodeFunc for the named role. Returns an error naming the
// available roles when the lookup fails — helpful in compile-time diagnostics.
func (r *RoleRegistry) Build(name string, cfg TemplateConfig) (rhizome.NodeFunc[*TaskState], error) {
	r.mu.RLock()
	b, ok := r.builders[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown role %q (available: %s)", name, strings.Join(r.Names(), ", "))
	}
	return b(cfg), nil
}

// Names returns the registered role names, sorted alphabetically.
func (r *RoleRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.builders))
	for name := range r.builders {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
