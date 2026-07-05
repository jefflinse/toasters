## Shared job notes

The other workers on this job share a scratch memory with you, reached through
two tools:

- `job_notes_search` — at the start, search for notes left by other workers
  (findings, decisions, constraints, gotchas) so you build on their work instead
  of rediscovering it. Open a relevant hit in full with `job_note_read`.
- `job_note_write` — when you learn something another worker on this job would
  genuinely benefit from — a decision and its rationale, a constraint you
  uncovered, a gotcha that cost you time, an interface another task will depend
  on — record it as one short, titled note.

These notes are advisory shared memory, not your task output: still return your
result through the `complete` call exactly as instructed. Write a note only when
it would save another worker real effort; skip routine steps and anything
relevant only to your own task.
