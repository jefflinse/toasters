---
name: Orchestration
description: Coordination patterns for managing teams, tasks, and the job lifecycle
---
# Orchestration

This skill covers the patterns and norms for coordinating work across teams in toasters.

## Task Lifecycle

Tasks move through these statuses:

1. **pending** — Created but not yet started. Waiting for assignment or for a preceding task to complete.
2. **in_progress** — A team is actively working on it. Progress reports should flow during this phase.
3. **completed** — Work is done and the result is available. The task summary should describe what was produced.
4. **failed** — The task could not be completed. The failure reason should be captured in the status update.
5. **blocked** — The team cannot proceed. A blocker report should accompany this status with details on what's needed.
6. **cancelled** — The task was cancelled, typically because the plan changed or the job was restructured.

## System Tools

System agents use these tools to coordinate work:

- **consult_worker**: Spawn a system worker session for planning, scheduling, or blocker triage. The worker runs to completion and returns its result.
- **create_job**: Create a new top-level job with a description and workspace directory.
- **create_task**: Add a task to a job. Tasks have descriptions and are assigned to teams.
- **assign_task**: Route a task to a specific team for execution.
- **query_job_context**: Get the current state of a job including task statuses, progress, and artifacts.
- **surface_to_user**: Relay information to the user when their attention or a decision is needed.

## Coordination Norms

- **Serial execution**: Tasks within a job execute one at a time. Order them so each task can build on the results of previous tasks.
- **Self-contained task descriptions**: Each task description should provide enough context for the assigned team to begin work without reading the full job history.
- **Progress visibility**: Teams should report progress regularly so the system can track job health and surface status to the user.
- **Fail fast on blockers**: If a team is blocked, report it immediately rather than attempting workarounds that may produce incorrect results.
- **Minimal escalation**: Resolve issues at the lowest level possible. Only escalate to the user when a genuine decision or external input is required.
