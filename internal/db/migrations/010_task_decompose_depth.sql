-- 010_task_decompose_depth.sql: Track how many times a task has been
-- recursively split by fine-decompose. The service increments this
-- counter each time a fine-decompose rejection produces subtasks, so
-- runaway decomposition loops can be capped at a configurable depth.

ALTER TABLE tasks ADD COLUMN decompose_depth INTEGER NOT NULL DEFAULT 0;
