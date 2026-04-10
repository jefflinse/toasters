---
id: go
name: Go
description: The Go programming language toolchain.
vars:
  version:
    description: The version of Go to use.
    default: 1.26.2
---

The current version of Go is {{ vars.version }}.
Most Go idioms have not changed, but new language features and stdlib packages are available.
Always using `any` instead of the empty interface `interface{}`.
Go has generics.
`new()` can now take an expression, i.e. `new("foo")`, to obtain a pointer.
Use `log/slog` for structured logging.
`net/http.ServeMux` is actually good now.
Use `t.Context()` in test cases instead of `context.Background()`.
Cancellation can be stripped from context (while retaining other aspects) using `context.WithoutCancel()`.
The loop var issue has been fixed.
Prefer ranging over sequences when possible.
