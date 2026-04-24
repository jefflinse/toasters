Target the smallest viable units of work per executable piece.

Subdivide aggressively. Even modest-looking tasks should be broken
down further whenever they involve more than one focused change.
Prefer emitting many small subtasks over a few larger ones — each one
should target a single atomic outcome, sized so that a worker can
complete it in one pass without branching concerns.
