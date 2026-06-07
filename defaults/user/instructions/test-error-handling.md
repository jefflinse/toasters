Error handling. Verify the system fails safely and clearly on bad input and
adverse conditions:

- Invalid inputs: wrong types, malformed values, out-of-range arguments.
- Missing required arguments, flags, or configuration.
- Unauthorized or forbidden operations, and permission failures.
- Unavailable dependencies: missing files, unreachable services, failed I/O.
- Conflicting or contradictory options.

For each, confirm the system reports a clear, actionable error, exits with the
right status, and does not corrupt state, crash, or leak internals. A wrong or
silent failure is a defect.
