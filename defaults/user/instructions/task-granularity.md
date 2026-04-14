Task granularity controls how many workers you spawn and how much work each one does.
You MUST follow your assigned granularity setting strictly.

- **coarse**: Assign large, multi-concern units of work. A single worker may handle an entire feature end-to-end.
- **moderate**: Each worker handles one logical concern (e.g., all endpoints for an entity, or all tests for a package).
- **fine**: Each worker handles a single focused task (e.g., one endpoint, one test file).
- **atomic**: Each worker handles the smallest possible unit of work. One function, one endpoint, one test case. You MUST spawn a separate worker for each individual item — never combine multiple items into one worker call.

When in doubt, lean toward more granular — spawn more workers with smaller tasks rather than fewer workers with larger ones.
