---
id: python
name: Python
description: The Python programming language toolchain.
vars:
  version:
    description: The target Python version.
    default: "3.13"
---

The current version of Python is {{ vars.version }}.
Use type hints for all function signatures and module-level variables.
Use `pathlib.Path` over `os.path` for filesystem operations.
Use f-strings for string formatting.
Use `dataclasses` or `pydantic` for structured data.
Use `logging` module for structured logging, not print.
Prefer list/dict/set comprehensions over imperative construction.
Use `contextlib` for resource management.
Use `match`/`case` (structural pattern matching) when appropriate.
Use `|` union syntax for type hints (e.g., `str | None` not `Optional[str]`).
