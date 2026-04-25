package prompt

import (
	"fmt"
	"log/slog"
)

// ApplyGranularity wires the currently selected granularity level for a given
// decomposition stage (kind = "coarse" or "fine") into the engine.
//
// It looks up the per-level instruction loaded from disk
// (`<kind>-granularity-<level>`) and registers its body under the synthetic
// instruction `<kind>-granularity` so role templates can reference
// `{{ instructions.coarse-granularity }}` or `{{ instructions.fine-granularity }}`
// without knowing which level is active. It also exposes the raw level
// string as `{{ globals.<kind>.granularity }}` for roles that want the
// value directly.
//
// Returns an error only when the per-level instruction file was not loaded.
// Callers at startup should log and continue rather than fail, since roles
// that don't reference the instruction are unaffected.
func ApplyGranularity(e *Engine, kind, level string) error {
	if e == nil {
		return fmt.Errorf("nil engine")
	}
	if kind == "" {
		return fmt.Errorf("empty granularity kind")
	}
	e.SetGlobal(kind+".granularity", level)

	srcName := kind + "-granularity-" + level
	body, ok := e.Instruction(srcName)
	if !ok {
		slog.Warn("granularity instruction not found", "name", srcName)
		return fmt.Errorf("instruction %q not loaded", srcName)
	}
	e.SetInstruction(kind+"-granularity", body)
	return nil
}
