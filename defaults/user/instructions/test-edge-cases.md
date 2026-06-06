Edge cases. Probe the boundaries of valid usage:

- Boundary values: zero, one, the maximum, and just past the maximum.
- Empty and minimal inputs: empty strings, empty lists, missing optional fields.
- Large or long inputs: maximum lengths, many items, deep nesting.
- Unusual but valid combinations of options and flags.
- Ordering and idempotency: repeated operations, out-of-order steps where allowed.

These are still valid inputs — the system should handle them correctly, not
reject them as errors.
