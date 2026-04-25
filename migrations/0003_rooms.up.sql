-- 0003_rooms: интерактивные сессии (комнаты), участники, ответы.

CREATE TYPE room_status AS ENUM (
    'created',  -- комната заведена, код выпущен
    'waiting',  -- студенты подключаются
    'active',   -- активен один из вопросов
    'review',   -- показ результатов текущего вопроса
    'finished'  -- сессия завершена
);

CREATE TABLE rooms (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    course_id       UUID NOT NULL REFERENCES courses(id) ON DELETE CASCADE,
    bank_id         UUID NOT NULL REFERENCES question_banks(id),
    -- Шестизначный код для подключения студентов
    code            CHAR(6) NOT NULL UNIQUE,
    title           TEXT NOT NULL,
    status          room_status NOT NULL DEFAULT 'created',
    current_question_id UUID REFERENCES questions(id),
    -- Время активации текущего вопроса (для расчёта XP по скорости)
    current_started_at  TIMESTAMPTZ,
    -- Очерёдность вопросов: массив question_id в порядке предъявления
    question_order  UUID[] NOT NULL DEFAULT '{}',
    -- Пройденные вопросы
    asked_question_ids UUID[] NOT NULL DEFAULT '{}',
    -- Настройки: показывать ли результаты студентам, разрешена ли анонимность и т.п.
    settings        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at      TIMESTAMPTZ,
    finished_at     TIMESTAMPTZ
);

CREATE INDEX idx_rooms_status ON rooms(status) WHERE status IN ('waiting', 'active', 'review');
CREATE INDEX idx_rooms_course ON rooms(course_id);

CREATE TABLE participants (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    room_id     UUID NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    nickname    TEXT NOT NULL,
    joined_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    left_at     TIMESTAMPTZ,
    UNIQUE (room_id, user_id)
);

CREATE INDEX idx_participants_room ON participants(room_id);
CREATE INDEX idx_participants_user ON participants(user_id);

CREATE TABLE answers (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    room_id         UUID NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    question_id     UUID NOT NULL REFERENCES questions(id),
    participant_id  UUID NOT NULL REFERENCES participants(id),
    -- Значение ответа в JSON (зависит от типа вопроса)
    value           JSONB NOT NULL,
    is_correct      BOOLEAN,
    elapsed_ms      INTEGER NOT NULL,
    awarded_xp      INTEGER NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Один ответ на (комната, вопрос, участник) — защита от дублей
    UNIQUE (room_id, question_id, participant_id)
);

CREATE INDEX idx_answers_room ON answers(room_id);
