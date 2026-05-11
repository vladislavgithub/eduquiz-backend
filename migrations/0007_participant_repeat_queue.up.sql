-- 0007: персональная очередь повтора в race-режиме.
-- Quizlet-style soft repeat: вопрос с ошибкой уходит в конец личной
-- очереди студента вместо полного сброса прогресса. После того как
-- основная очередь (rooms.question_order) пройдена, бэк выдаёт вопросы
-- из этого массива — пока он не опустеет (тогда finished_at_session).

ALTER TABLE participants
    ADD COLUMN pending_repeat_question_ids UUID[] NOT NULL DEFAULT '{}';
