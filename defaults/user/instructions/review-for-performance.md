Performance. Look for:

- Unnecessary allocations in hot paths, especially inside loops.
- N+1 patterns: repeated I/O, queries, or RPCs that could be batched.
- Redundant work: recomputing values that could be cached or hoisted out of a loop.
- Data structures or algorithms ill-suited to the access pattern (e.g. linear scans of a map-shaped lookup).

Flag only changes likely to matter at realistic scale; do not micro-optimize cold paths.
