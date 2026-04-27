-- 0006: каталог стандартных бейджей.
-- Бейджи идемпотентны: ON CONFLICT DO UPDATE — позволяет править
-- title/description/icon в этой миграции и через replay получать актуал.
-- Иконки — material-codepoints (используются на фронте через Icons.<name>).

INSERT INTO badges (code, title, description, icon) VALUES
    ('first_correct',
        'Первый шаг',
        'Дал первый правильный ответ в курсе',
        'flag_rounded'),
    ('perfect_round',
        'Идеальный раунд',
        'Ответил правильно на все вопросы в одной сессии',
        'star_rounded'),
    ('quick_thinker',
        'Молниеносный',
        'Ответил правильно меньше чем за треть времени',
        'bolt_rounded'),
    ('streak_3',
        'На волне',
        '3 дня подряд с активностью',
        'local_fire_department_rounded'),
    ('streak_7',
        'Неделя в потоке',
        '7 дней подряд с активностью',
        'whatshot_rounded'),
    ('level_5',
        'Уровень 5',
        'Достиг 5-го уровня в курсе',
        'military_tech_rounded'),
    ('level_10',
        'Уровень 10',
        'Достиг 10-го уровня в курсе',
        'workspace_premium_rounded'),
    ('xp_1000',
        '1 000 XP',
        'Накопил 1 000 XP в одном курсе',
        'emoji_events_rounded'),
    ('reliability_apprentice',
        'Подмастерье надёжности',
        'Прошёл оба банка по теории надёжности без единой ошибки',
        'verified_rounded')
ON CONFLICT (code) DO UPDATE SET
    title       = EXCLUDED.title,
    description = EXCLUDED.description,
    icon        = EXCLUDED.icon;
