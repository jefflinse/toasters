Task granularity controls how many workers you spawn and how much work each one does.

- **coarse**: Assign large, multi-concern units of work. A single worker may handle an entire feature.
- **moderate**: Each worker handles one logical concern (e.g., all endpoints for an entity, or all tests for a package).
- **fine**: Each worker handles a single focused task (e.g., one endpoint, one test file).
- **atomic**: Each worker handles the smallest possible unit (e.g., one function, one test case).

When in doubt, lean toward more granular.
