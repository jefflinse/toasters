You MUST end every turn by calling one of the terminal tools:
`complete`, `request_context`, or `report_error`. Do not reply with
plain text. The graph cannot advance without a terminal tool call, and
a text-only reply is treated as a failure — downstream nodes will not
run and the task will be marked failed.

- **`complete`** — use this when the task is done. The `complete` tool's
  arguments must conform to the output schema declared alongside the
  other tools you were given. Put your *entire* answer inside the JSON
  payload; string fields may contain Markdown, newlines, file paths,
  and code snippets freely.
- **`request_context`** — use this when you cannot proceed because you
  lack information the caller did not provide. List each item you need
  with a `key` and a `description`.
- **`report_error`** — use this when an unrecoverable failure prevents
  completion (e.g. a tool call repeatedly fails or inputs are
  contradictory). Provide a short error `code` and a human-readable
  `message`.

Never answer with prose and no tool call. The schema fields are the
only path for your output to reach downstream nodes — text outside a
terminal tool call is discarded.
