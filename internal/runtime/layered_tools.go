package runtime

import (
	"context"
	"encoding/json"
	"errors"
)

// LayeredToolExecutor wraps a base ToolExecutor with an overlay that takes
// dispatch priority. When Execute is called, the overlay is tried first;
// if it returns ErrUnknownTool, the call falls through to the base.
// Definitions() returns the union of both layers, with overlay definitions
// taking precedence for duplicate names.
type LayeredToolExecutor struct {
	overlay ToolExecutor
	base    ToolExecutor
}

// NewLayeredToolExecutor creates a LayeredToolExecutor where overlay gets
// dispatch priority over base.
func NewLayeredToolExecutor(base, overlay ToolExecutor) *LayeredToolExecutor {
	return &LayeredToolExecutor{overlay: overlay, base: base}
}

// Execute tries the overlay first. If it returns ErrUnknownTool, the call
// falls through to the base executor.
func (l *LayeredToolExecutor) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	result, err := l.overlay.Execute(ctx, name, args)
	if errors.Is(err, ErrUnknownTool) {
		return l.base.Execute(ctx, name, args)
	}
	return result, err
}

// Definitions returns the union of both layers. Overlay definitions take
// precedence for duplicate names.
func (l *LayeredToolExecutor) Definitions() []ToolDef {
	baseDefs := l.base.Definitions()
	overlayDefs := l.overlay.Definitions()

	// Build a set of overlay names for dedup.
	overlayNames := make(map[string]struct{}, len(overlayDefs))
	for _, td := range overlayDefs {
		overlayNames[td.Name] = struct{}{}
	}

	// Start with overlay defs, then append base defs that aren't shadowed.
	merged := make([]ToolDef, 0, len(overlayDefs)+len(baseDefs))
	merged = append(merged, overlayDefs...)
	for _, td := range baseDefs {
		if _, shadowed := overlayNames[td.Name]; !shadowed {
			merged = append(merged, td)
		}
	}
	return merged
}
