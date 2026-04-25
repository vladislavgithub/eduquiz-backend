-- 0004_gamification: XP, бейджи, серии, состояние SM-2.

CREATE TABLE xp_log (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    course_id       UUID REFERENCES courses(id) ON DELETE CASCADE,
    answer_id       UUID REFERENCES answers(id) ON DELETE SET NULL,
    delta           INTEGER NOT NULL,
    -- 'answer' | 'streak_bonus' | 'badge_bonus' | 'manual_adjust'
    reason          TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_xp_log_user_course ON xp_log(user_id, course_id);

-- Денормализованный агрегат: текущие XP и уровень в курсе.
-- Обновляется триггером или сервисным слоем при записи в xp_log.
CREATE TABLE user_progress (
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    course_id   UUID NOT NULL REFERENCES courses(id) ON DELETE CASCADE,
    total_xp    INTEGER NOT NULL DEFAULT 0,
    level       INTEGER NOT NULL DEFAULT 0,
    streak_days INTEGER NOT NULL DEFAULT 0,
    last_active DATE,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, course_id)
);

CREATE TABLE badges (
    code        TEXT PRIMARY KEY,
    title       TEXT NOT NULL,
    description TEXT NOT NULL,
    icon        TEXT
);

INSERT INTO badges (code, title, description, icon) VALUES
    ('first_room',    'Первая комната',     'Первое участие в интерактивной сессии', '🚪'),
    ('streak_5',      'Серия из 5',         'Пять правильных ответов подряд в одной сессии', '🔥'),
    ('speed_runner',  'Скоростной ответ',   '10 ответов быстрее половины максимального времени', '⚡'),
    ('persistence_7', 'Постоянство',        '7 дней активности подряд', '📅'),
    ('examiner',      'Экзаменатор',        'Завершение всех тем курса', '🎓');

CREATE TABLE user_badges (
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    badge_code  TEXT NOT NULL REFERENCES badges(code),
    course_id   UUID REFERENCES courses(id) ON DELETE CASCADE,
    awarded_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, badge_code, course_id)
);

-- SM-2: состояние повторения для каждой пары (студент, вопрос).
CREATE TABLE sm2_states (
    user_id          UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    question_id      UUID NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
    -- Easiness factor (изначально 2.5, минимум 1.3)
    ef               REAL NOT NULL DEFAULT 2.5,
    -- Текущий интервал в днях
    interval_days    INTEGER NOT NULL DEFAULT 0,
    -- Число последовательных успешных повторений
    repetitions      INTEGER NOT NULL DEFAULT 0,
    -- Дата следующего планового повторения
    due_date         DATE NOT NULL DEFAULT CURRENT_DATE,
    last_reviewed_at TIMESTAMPTZ,
    PRIMARY KEY (user_id, question_id)
);

CREATE INDEX idx_sm2_due ON sm2_states(user_id, due_date);
