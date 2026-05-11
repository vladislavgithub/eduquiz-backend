-- 0009: расширение ENUM question_kind двумя типами:
--   - numerical:  числовой ответ с допустимой погрешностью (tolerance
--                 в metadata.tolerance, float). Нужно для расчётных
--                 задач в технических дисциплинах (MTBF, ВБР и т.п.).
--   - true_false: упрощённый single_choice с двумя плашками; correct
--                 хранится как ["true"] или ["false"].
-- Расширение ENUM безопасно — существующие записи не трогаются.

ALTER TYPE question_kind ADD VALUE IF NOT EXISTS 'numerical';
ALTER TYPE question_kind ADD VALUE IF NOT EXISTS 'true_false';
