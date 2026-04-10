---
id: toasters
name: Toasters
description: Project-specific context for the Toasters codebase.
---

You are working on Toasters, a TUI-first agentic orchestration platform written in Go.

Key architecture:
- internal/service is the central hub. All state mutation goes through the Service interface.
- internal/runtime spawns and manages worker sessions as goroutines.
- internal/operator is the operator LLM coordination layer.
- internal/tui is a thin Bubble Tea v2 client. It never accesses state directly.
- internal/server + internal/client + internal/sse provide the REST/SSE API.
- internal/provider is the multi-provider LLM client.
- internal/db is SQLite (modernc.org/sqlite). Only internal/service calls into it.
- internal/loader loads definitions from disk and watches for changes.
- internal/prompt composes worker system prompts from roles, toolchains, and instructions.

Conventions:
- Tests are co-located (*_test.go next to source).
- The race detector must stay clean across all packages.
- Definitions (roles, toolchains, instructions) are markdown files with YAML frontmatter.
- Errors use package-level sentinels in internal/service/errors.go.
- The TUI never imports internal packages other than service.
- Go owns the state; LLMs are stateless tools invoked with accumulated context.
