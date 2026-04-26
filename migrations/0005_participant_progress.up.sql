-- 0005: per-student progress для timer/race-режимов.
-- В режимах timer и race каждый студент идёт по вопросам в своём
-- темпе, поэтому позиция в массиве вопросов хранится per-participant,
-- а не per-room.

ALTER TABLE participants
    ADD COLUMN current_question_idx INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN finished_at_session   TIMESTAMPTZ;

-- Индекс для запросов «кто на каком вопросе» (race-bar в реальном времени).
CREATE INDEX idx_participants_progress
    ON participants(room_id, current_question_idx);
