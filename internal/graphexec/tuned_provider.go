package graphexec

import (
	"context"
	"strings"

	"github.com/jefflinse/toasters/internal/prompt"
	"github.com/jefflinse/toasters/internal/provider"
)

// tunedProvider wraps a Provider and applies per-role sampling/thinking
// overrides on every ChatStream call. Keeping the override at the wrapper
// layer is what lets a single provider instance serve nodes with different
// temperatures and thinking modes without each call site having to know
// the role-resolution rules.
//
// Thinking is conveyed via a leading `/no_think` or `/think` token in the
// system prompt. That convention is what Qwen3-family models (and several
// other open-weights families) recognize via their chat template; providers
// that don't understand the token simply ignore the leading line, which is
// the right no-op for older or remote-only models.
type tunedProvider struct {
	inner       provider.Provider
	temperature *float64
	thinking    *bool
}

// newTunedProvider returns a wrapper that injects the given temperature and
// thinking values into every ChatStream call. A nil pointer means "leave
// the field alone": a graph node with no role override and no global value
// shouldn't surface as a request body that pins temperature to zero.
func newTunedProvider(inner provider.Provider, thinkingEnabled bool, temperature float64) provider.Provider {
	temp := temperature
	think := thinkingEnabled
	return &tunedProvider{
		inner:       inner,
		temperature: &temp,
		thinking:    &think,
	}
}

// Name proxies to the inner provider so logs and event payloads still
// report the underlying provider's identity rather than a wrapper-specific
// name.
func (t *tunedProvider) Name() string { return t.inner.Name() }

// Models proxies to the inner provider unchanged.
func (t *tunedProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	return t.inner.Models(ctx)
}

// ChatStream forwards the request to the inner provider after layering on
// the temperature override (if the request didn't already pin one) and
// prepending a thinking-mode token to the system prompt.
func (t *tunedProvider) ChatStream(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	if t.temperature != nil && req.Temperature == nil {
		v := *t.temperature
		req.Temperature = &v
	}
	if t.thinking != nil && providerHonorsThinkingToken(t.inner) {
		token := "/no_think"
		if *t.thinking {
			token = "/think"
		}
		req.System = applyThinkingToken(req.System, token)
	}
	return t.inner.ChatStream(ctx, req)
}

// providerHonorsThinkingToken reports whether the inner provider's chat
// template recognizes the `/think` and `/no_think` markers. The convention
// originated with Qwen3 and has been adopted by several other open-weights
// chat templates served through OpenAI-compatible endpoints (LM Studio,
// vLLM, llama.cpp). The Anthropic API does not — extended thinking there
// is controlled by a structured `thinking` request field — so we skip
// injection rather than dump a stray token into the system prompt where
// Claude would just treat it as text.
func providerHonorsThinkingToken(p provider.Provider) bool {
	return p.Name() != "anthropic"
}

// applyThinkingToken prepends the given thinking token to the system
// prompt, replacing any existing leading /think or /no_think marker so a
// global default doesn't stack on top of a role-supplied one when the
// wrapper is composed.
func applyThinkingToken(system, token string) string {
	trimmed := strings.TrimLeft(system, " \t")
	for _, marker := range []string{"/no_think", "/think"} {
		if strings.HasPrefix(trimmed, marker) {
			rest := strings.TrimLeft(trimmed[len(marker):], " \t\r\n")
			if rest == "" {
				return token
			}
			return token + "\n\n" + rest
		}
	}
	if system == "" {
		return token
	}
	return token + "\n\n" + system
}

// effectiveWorkerDefaults resolves the thinking/temperature pair for a
// single node by layering role-level frontmatter overrides on top of the
// graph-wide template defaults.
func effectiveWorkerDefaults(cfg TemplateConfig, role *prompt.Role) (bool, float64) {
	thinking := cfg.WorkerThinkingEnabled
	temperature := cfg.WorkerTemperature
	if role != nil {
		if role.Thinking != nil {
			thinking = *role.Thinking
		}
		if role.Temperature != nil {
			temperature = *role.Temperature
		}
	}
	return thinking, temperature
}
