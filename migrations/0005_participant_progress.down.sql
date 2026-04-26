DROP INDEX IF EXISTS idx_participants_progress;
ALTER TABLE participants
    DROP COLUMN IF EXISTS current_question_idx,
    DROP COLUMN IF EXISTS finished_at_session;
