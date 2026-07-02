-- Worker-session compaction: when a session compacts its in-memory history
-- (tier 2 summarize-and-continue), the transcript rows that no longer feed
-- the live conversation are flagged rather than deleted, so `sqlite3 ...
-- session_messages` debugging still shows the full history with an honest
-- marker of what the model can actually still see.

ALTER TABLE session_messages ADD COLUMN superseded INTEGER NOT NULL DEFAULT 0;
