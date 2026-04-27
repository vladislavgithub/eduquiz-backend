-- Down: каталог не удаляем, иначе FK user_badges.badge_code сорвётся.
-- Удаление миграции = удалить только новые коды через явный список.
DELETE FROM badges WHERE code IN (
    'first_correct', 'perfect_round', 'quick_thinker',
    'streak_3', 'streak_7', 'level_5', 'level_10',
    'xp_1000', 'reliability_apprentice'
);
