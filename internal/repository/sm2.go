// Репозиторий состояний spaced repetition (SM-2).
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SM2State — состояние одной карточки (user, question).
type SM2State struct {
	UserID         uuid.UUID
	QuestionID     uuid.UUID
	EF             float64
	IntervalDays   int
	Repetitions    int
	DueDate        time.Time
	LastReviewedAt *time.Time
}

// DueCard — вопрос на сегодняшнее повторение со всеми данными,
// нужными клиенту, чтобы показать карточку.
type DueCard struct {
	QuestionID   uuid.UUID
	BankID       uuid.UUID
	CourseID     uuid.UUID
	BankTitle    string
	Kind         string
	Text         string
	Options      []byte // raw JSON, отдадим клиенту как есть
	TimeLimitSec int
	Difficulty   int
	DueDate      time.Time
}

type SM2Repo struct {
	pool *pgxpool.Pool
}

func NewSM2Repo(pool *pgxpool.Pool) *SM2Repo {
	return &SM2Repo{pool: pool}
}

// Get возвращает текущее состояние карточки или ErrNotFound, если её нет.
func (r *SM2Repo) Get(ctx context.Context, userID, questionID uuid.UUID) (SM2State, error) {
	const sql = `
		SELECT user_id, question_id, ef, interval_days, repetitions, due_date, last_reviewed_at
		FROM sm2_states
		WHERE user_id = $1 AND question_id = $2`
	var s SM2State
	err := r.pool.QueryRow(ctx, sql, userID, questionID).Scan(
		&s.UserID, &s.QuestionID, &s.EF, &s.IntervalDays, &s.Repetitions,
		&s.DueDate, &s.LastReviewedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return SM2State{}, ErrNotFound
	}
	return s, err
}

// Upsert сохраняет/обновляет состояние карточки.
func (r *SM2Repo) Upsert(ctx context.Context, s SM2State) error {
	const sql = `
		INSERT INTO sm2_states (user_id, question_id, ef, interval_days, repetitions, due_date, last_reviewed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (user_id, question_id) DO UPDATE SET
			ef               = EXCLUDED.ef,
			interval_days    = EXCLUDED.interval_days,
			repetitions      = EXCLUDED.repetitions,
			due_date         = EXCLUDED.due_date,
			last_reviewed_at = EXCLUDED.last_reviewed_at`
	_, err := r.pool.Exec(ctx, sql,
		s.UserID, s.QuestionID, s.EF, s.IntervalDays, s.Repetitions,
		s.DueDate, s.LastReviewedAt,
	)
	return err
}

// ListDue возвращает карточки, у которых due_date <= today, для юзера.
// Лимит ограничивает максимум одной сессии повторения, чтобы не выгружать
// 500 карточек разом.
func (r *SM2Repo) ListDue(ctx context.Context, userID uuid.UUID, limit int) ([]DueCard, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	const sql = `
		SELECT s.question_id, q.bank_id, b.course_id, b.title,
		       q.kind::text, q.text, q.options, q.time_limit_sec, q.difficulty,
		       s.due_date
		FROM sm2_states s
		JOIN questions q       ON q.id = s.question_id
		JOIN question_banks b  ON b.id = q.bank_id
		WHERE s.user_id = $1
		  AND s.due_date <= CURRENT_DATE
		ORDER BY s.due_date ASC, s.repetitions ASC
		LIMIT $2`
	rows, err := r.pool.Query(ctx, sql, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DueCard
	for rows.Next() {
		var c DueCard
		if err := rows.Scan(
			&c.QuestionID, &c.BankID, &c.CourseID, &c.BankTitle,
			&c.Kind, &c.Text, &c.Options, &c.TimeLimitSec, &c.Difficulty,
			&c.DueDate,
		); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountDue возвращает число карточек на сегодня (для бейджа на иконке
// «Повторение» в навигации студента).
func (r *SM2Repo) CountDue(ctx context.Context, userID uuid.UUID) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*)::int FROM sm2_states WHERE user_id = $1 AND due_date <= CURRENT_DATE`,
		userID,
	).Scan(&n)
	return n, err
}
