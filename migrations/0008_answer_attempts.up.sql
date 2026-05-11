-- 0008: счётчик попыток на ответ. Используется в race-режиме с
-- soft-repeat: студент может ответить на тот же вопрос несколько раз
-- (упорядочено через pending_repeat_question_ids в участнике), и нам
-- нужно для статистики уметь различать «правильно с первой попытки»
-- vs «правильно после повтора». Upsert инкрементирует счётчик при
-- ON CONFLICT.

ALTER TABLE answers
    ADD COLUMN attempts INTEGER NOT NULL DEFAULT 1;
