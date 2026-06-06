Correctness. Look for:

- Logic errors and wrong conditions (inverted booleans, off-by-one, wrong operators).
- Nil/null dereferences and unchecked type assertions or casts.
- Unhandled or swallowed errors, and errors returned without context.
- Data races and unsynchronized access to shared state.
- Edge cases: empty inputs, zero values, boundary and overflow conditions.
- Resource handling: leaked files, connections, goroutines, or unclosed handles.
