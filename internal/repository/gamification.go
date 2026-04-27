// Репозиторий геймификации: user_progress, xp_log, badges, user_badges.
// Все операции идемпотентны: AddXP — атомарный UPSERT, AwardBadge —
// INSERT ... ON CONFLICT DO NOTHING (награждать дважды нельзя).
package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UserProgress — суммарный прогресс студента в одном курсе.
type UserProgress struct {
	UserID     uuid.UUID
	CourseID   uuid.UUID
	TotalXP    int
	Level      int
	StreakDays int
	LastActive *time.Time
	UpdatedAt  time.Time
}

// XPLogEntry — запись в журнале начислений XP.
type XPLogEntry struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	CourseID  *uuid.UUID
	AnswerID  *uuid.UUID
	Delta     int
	Reason    string
	CreatedAt time.Time
}

// Badge — справочный бейдж.
type Badge struct {
	Code        string
	Title       string
	Description string
	Icon        string
}

// UserBadge — выданный бейдж (с метаданными).
type UserBadge struct {
	Code        string
	Title       string
	Description string
	Icon        string
	CourseID    uuid.UUID
	AwardedAt   time.Time
}

// GamificationRepo — UserProgress + XPLog + UserBadges в одном репо.
type GamificationRepo struct {
	pool *pgxpool.Pool
}

func NewGamificationRepo(pool *pgxpool.Pool) *GamificationRepo {
	return &GamificationRepo{pool: pool}
}

// AddXP атомарно начисляет дельту XP за курс.
// Возвращает обновлённое UserProgress.
//
// Семантика:
//
//   - total_xp += delta;
//   - level пересчитывается по правилу floor(sqrt(xp/100)) — делается
//     не в SQL (флаг сложности), а в caller через services.LevelFromXP;
//     этот метод обновляет уровень тем значением, которое передал caller;
//   - streak_days: если last_active = вчера → +1; если = сегодня → без
//     изменений; иначе сброс в 1.
//
// Также пишет запись в xp_log в одной транзакции.
func (r *GamificationRepo) AddXP(
	ctx context.Context,
	userID, courseID uuid.UUID,
	delta int,
	level int,
	answerID *uuid.UUID,
	reason string,
) (UserProgress, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return UserProgress{}, err
	}
	defer tx.Rollback(ctx)

	const upsertSQL = `
		INSERT INTO user_progress (user_id, course_id, total_xp, level, streak_days, last_active, updated_at)
		VALUES ($1, $2, $3, $4, 1, CURRENT_DATE, now())
		ON CONFLICT (user_id, course_id) DO UPDATE SET
			total_xp    = user_progress.total_xp + EXCLUDED.total_xp,
			level       = $4,
			streak_days = CASE
				WHEN user_progress.last_active = CURRENT_DATE             THEN user_progress.streak_days
				WHEN user_progress.last_active = CURRENT_DATE - INTERVAL '1 day' THEN user_progress.streak_days + 1
				ELSE 1
			END,
			last_active = CURRENT_DATE,
			updated_at  = now()
		RETURNING user_id, course_id, total_xp, level, streak_days, last_active, updated_at`
	var p UserProgress
	if err := tx.QueryRow(ctx, upsertSQL, userID, courseID, delta, level).Scan(
		&p.UserID, &p.CourseID, &p.TotalXP, &p.Level, &p.StreakDays, &p.LastActive, &p.UpdatedAt,
	); err != nil {
		return UserProgress{}, err
	}

	if delta != 0 {
		const logSQL = `
			INSERT INTO xp_log (user_id, course_id, answer_id, delta, reason)
			VALUES ($1, $2, $3, $4, $5)`
		if _, err := tx.Exec(ctx, logSQL, userID, courseID, answerID, delta, reason); err != nil {
			return UserProgress{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return UserProgress{}, err
	}
	return p, nil
}

// ListProgress возвращает прогресс юзера по всем курсам.
func (r *GamificationRepo) ListProgress(ctx context.Context, userID uuid.UUID) ([]UserProgress, error) {
	const sql = `
		SELECT user_id, course_id, total_xp, level, streak_days, last_active, updated_at
		FROM user_progress
		WHERE user_id = $1
		ORDER BY total_xp DESC`
	rows, err := r.pool.Query(ctx, sql, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserProgress
	for rows.Next() {
		var p UserProgress
		if err := rows.Scan(&p.UserID, &p.CourseID, &p.TotalXP, &p.Level, &p.StreakDays, &p.LastActive, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// XPHistogramByDay возвращает суммарный XP по дням за последние N дней
// для одного юзера в одном курсе. Используется для графика на профиле.
func (r *GamificationRepo) XPHistogramByDay(
	ctx context.Context,
	userID, courseID uuid.UUID,
	days int,
) (map[string]int, error) {
	if days <= 0 {
		days = 14
	}
	const sql = `
		SELECT to_char(created_at, 'YYYY-MM-DD') AS day, SUM(delta)::int
		FROM xp_log
		WHERE user_id = $1 AND course_id = $2
		  AND created_at >= now() - ($3 || ' days')::interval
		GROUP BY day ORDER BY day`
	rows, err := r.pool.Query(ctx, sql, userID, courseID, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var d string
		var v int
		if err := rows.Scan(&d, &v); err != nil {
			return nil, err
		}
		out[d] = v
	}
	return out, rows.Err()
}

// AwardBadge выдаёт бейдж студенту в курсе. Идемпотентен —
// повторный вызов не создаёт дубликата (PK на (user, badge, course)).
// Возвращает true, если бейдж был именно сейчас выдан (не уже был).
func (r *GamificationRepo) AwardBadge(
	ctx context.Context,
	userID uuid.UUID,
	badgeCode string,
	courseID uuid.UUID,
) (bool, error) {
	const sql = `
		INSERT INTO user_badges (user_id, badge_code, course_id)
		VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING
		RETURNING true`
	var inserted bool
	err := r.pool.QueryRow(ctx, sql, userID, badgeCode, courseID).Scan(&inserted)
	if err != nil {
		// Нет строки в RETURNING — значит конфликт, бейдж уже был.
		return false, nil
	}
	return inserted, nil
}

// ListUserBadges возвращает все бейджи юзера со справочной информацией
// (название, описание, иконка), отсортированные по дате получения.
func (r *GamificationRepo) ListUserBadges(ctx context.Context, userID uuid.UUID) ([]UserBadge, error) {
	const sql = `
		SELECT b.code, b.title, b.description, COALESCE(b.icon, ''),
		       ub.course_id, ub.awarded_at
		FROM user_badges ub
		JOIN badges b ON b.code = ub.badge_code
		WHERE ub.user_id = $1
		ORDER BY ub.awarded_at DESC`
	rows, err := r.pool.Query(ctx, sql, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserBadge
	for rows.Next() {
		var b UserBadge
		if err := rows.Scan(&b.Code, &b.Title, &b.Description, &b.Icon, &b.CourseID, &b.AwardedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ListAllBadges возвращает справочник всех бейджей (для отображения
// «заработанных» и «нераскрытых» в одной сетке на профиле).
func (r *GamificationRepo) ListAllBadges(ctx context.Context) ([]Badge, error) {
	rows, err := r.pool.Query(ctx, `SELECT code, title, description, COALESCE(icon, '') FROM badges ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Badge
	for rows.Next() {
		var b Badge
		if err := rows.Scan(&b.Code, &b.Title, &b.Description, &b.Icon); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// CountCorrectInCourse возвращает число правильных ответов студента
// в курсе (идёт в проверку «первый правильный ответ» / «1000 XP» и т.п.).
func (r *GamificationRepo) CountCorrectInCourse(ctx context.Context, userID, courseID uuid.UUID) (int, error) {
	const sql = `
		SELECT COUNT(*)::int
		FROM answers a
		JOIN participants p ON p.id = a.participant_id
		JOIN rooms r        ON r.id = p.room_id
		WHERE p.user_id = $1 AND r.course_id = $2 AND a.is_correct = true`
	var n int
	err := r.pool.QueryRow(ctx, sql, userID, courseID).Scan(&n)
	return n, err
}

// CountCorrectInRoom — сколько правильных у юзера в одной комнате.
// Используется для проверки бейджа «идеальный раунд» (correct == total).
func (r *GamificationRepo) CountCorrectInRoom(
	ctx context.Context,
	userID, roomID uuid.UUID,
) (correct, total int, err error) {
	const sql = `
		SELECT
			COALESCE(SUM(CASE WHEN a.is_correct = true THEN 1 ELSE 0 END), 0)::int,
			COUNT(*)::int
		FROM answers a
		JOIN participants p ON p.id = a.participant_id
		WHERE p.user_id = $1 AND p.room_id = $2`
	err = r.pool.QueryRow(ctx, sql, userID, roomID).Scan(&correct, &total)
	return
}
