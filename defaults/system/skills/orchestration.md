---
name: Orchestration
description: Coordination patterns for managing graphs, tasks, and the job lifecycle
---
# Orchestration

This skill covers the patterns and norms for coordinating work in toasters. Work is organized as jobs containing tasks; each task runs on a declarative graph whose nodes are role-bound workers.

## Task Lifecycle

Tasks move through these statuses:

1. **pending** — Created but not yet started. Waiting for graph selection, for a dependency, or for a preceding task to complete.
2. **in_progress** — A graph is actively executing it. Progress reports should flow during this phase.
3. **completed** — Work is done and the result is available. The task summary should describe what was produced.
4. **failed** — The task could not be completed. The failure reason should be captured in the status update.
5. **blocked** — The work cannot proceed. A blocker report should accompany this status with details on what's needed.
6. **cancelled** — The task was cancelled, typically because the plan changed or the job was cancelled.

## System Tools

System workers use these tools to coordinate work:

- **create_job**: Create a new top-level job with a description and workspace directory. Decomposition into tasks happens automatically after this. Pass `workspace_of_job` with an existing job's ID to share that job's workspace for follow-up work on its files.
- **create_task**: Add a follow-up task to an existing job. The framework selects a graph for it automatically unless one is given.
- **retry_task**: Re-run a failed task in place on its graph instead of recreating work in a new job.
- **query_job**: Get the full current state of a job including every task's status, graph, and result summary.
- **query_graphs**: List the available graphs and what class of work each executes.
- **surface_to_user**: Relay information to the user when their attention or a decision is needed.
- **ask_user**: Ask the user questions and wait for answers when a genuine decision or missing input blocks progress.

## Coordination Norms

- **Serial execution**: Tasks within a job execute one at a time. Order them so each task can build on the results of previous tasks.
- **Self-contained task descriptions**: Each task description should provide enough context for the workers executing it to begin without reading the full job history.
- **Progress visibility**: Workers should report progress regularly so the system can track job health and surface status to the user.
- **Fail fast on blockers**: If work is blocked, report it immediately rather than attempting workarounds that may produce incorrect results.
- **Minimal escalation**: Resolve issues at the lowest level possible. Only escalate to the user when a genuine decision or external input is required.
