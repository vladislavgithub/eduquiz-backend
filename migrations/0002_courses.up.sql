-- 0002_courses: курсы преподавателя и банки вопросов.

CREATE TABLE courses (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    teacher_id  UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title       TEXT NOT NULL,
    description TEXT,
    is_archived BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_courses_teacher ON courses(teacher_id) WHERE NOT is_archived;

CREATE TABLE question_banks (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    course_id   UUID NOT NULL REFERENCES courses(id) ON DELETE CASCADE,
    title       TEXT NOT NULL,
    -- Источник: 'manual' | 'edu_gubkin' | 'moodle_xml' | 'gift'
    source      TEXT NOT NULL DEFAULT 'manual',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_question_banks_course ON question_banks(course_id);

CREATE TYPE question_kind AS ENUM (
    'single_choice',
    'multi_choice',
    'open_text',
    'rating',
    'qna'
);

CREATE TABLE questions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    bank_id         UUID NOT NULL REFERENCES question_banks(id) ON DELETE CASCADE,
    kind            question_kind NOT NULL,
    text            TEXT NOT NULL,
    -- Варианты ответа в JSON (для choice-типов): [{id, text, correct?}]
    options         JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- Правильные id вариантов для choice-типов; для open_text — шаблон
    correct         JSONB,
    -- 1..5 (используется в формуле начисления XP и SM-2)
    difficulty      SMALLINT NOT NULL DEFAULT 3 CHECK (difficulty BETWEEN 1 AND 5),
    -- Тема в рамках банка (свободный текст), для группировки
    topic           TEXT,
    -- Время на ответ (секунды), 0 = неограниченно
    time_limit_sec  INTEGER NOT NULL DEFAULT 30,
    -- Метаданные (origin id из edu.gubkin, теги, IRT-параметры и т.д.)
    metadata        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_questions_bank ON questions(bank_id);
CREATE INDEX idx_questions_topic ON questions(bank_id, topic);
