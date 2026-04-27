// Репозиторий ответов студентов на вопросы интерактивной сессии.
// Защита от двойных отправок — через UNIQUE (room_id, question_id, participant_id).
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Answer — ответ участника на конкретный вопрос внутри комнаты.
type Answer struct {
	ID            uuid.UUID
	RoomID        uuid.UUID
	QuestionID    uuid.UUID
	ParticipantID uuid.UUID
	Value         json.RawMessage // зависит от типа вопроса
	IsCorrect     *bool           // nil для open_text/qna до ручной проверки
	ElapsedMs     int
	AwardedXP     int
	CreatedAt     time.Time
}

// ErrDuplicateAnswer — повторная отправка ответа на тот же вопрос.
var ErrDuplicateAnswer = errors.New("answer already submitted")

// AnswersRepo — репозиторий ответов.
type AnswersRepo struct {
	pool *pgxpool.Pool
}

func NewAnswersRepo(pool *pgxpool.Pool) *AnswersRepo {
	return &AnswersRepo{pool: pool}
}

// Insert сохраняет ответ. При повторе — ErrDuplicateAnswer.
func (r *AnswersRepo) Insert(ctx context.Context, a *Answer) error {
	const sql = `
        INSERT INTO answers (room_id, question_id, participant_id, value,
                             is_correct, elapsed_ms, awarded_xp)
        VALUES ($1, $2, $3, $4, $5, $6, $7)
        RETURNING id, created_at`
	err := r.pool.QueryRow(ctx, sql,
		a.RoomID, a.QuestionID, a.ParticipantID, a.Value,
		a.IsCorrect, a.ElapsedMs, a.AwardedXP,
	).Scan(&a.ID, &a.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrDuplicateAnswer
		}
		return fmt.Errorf("insert answer: %w", err)
	}
	return nil
}

// DeleteByParticipant удаляет все ответы участника в комнате.
// Используется при reset в race-режиме, чтобы можно было пройти банк
// заново без UNIQUE-конфликта на (room_id, question_id, participant_id).
func (r *AnswersRepo) DeleteByParticipant(ctx context.Context, roomID, participantID uuid.UUID) error {
	const sql = `DELETE FROM answers WHERE room_id = $1 AND participant_id = $2`
	_, err := r.pool.Exec(ctx, sql, roomID, participantID)
	return err
}

// CountForQuestion возвращает количество ответов на текущий вопрос
// (для отображения «X из Y участников ответили» в реальном времени).
func (r *AnswersRepo) CountForQuestion(ctx context.Context, roomID, questionID uuid.UUID) (int, error) {
	const sql = `
        SELECT COUNT(*)
        FROM answers
        WHERE room_id = $1 AND question_id = $2`
	var n int
	err := r.pool.QueryRow(ctx, sql, roomID, questionID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count answers: %w", err)
	}
	return n, nil
}

// LeaderboardEntry — строка таблицы лидеров по сумме XP в комнате.
type LeaderboardEntry struct {
	ParticipantID      uuid.UUID
	UserID             uuid.UUID
	Nickname           string
	TotalXP            int
	Correct            int
	Total              int
	CurrentQuestionIdx int  // в timer/race: на каком вопросе сейчас (для race-bar)
	IsFinished         bool // прошёл ли всю сессию (solo-режимы)
}

// Leaderboard агрегирует результаты комнаты в порядке убывания XP.
func (r *AnswersRepo) Leaderboard(ctx context.Context, roomID uuid.UUID) ([]LeaderboardEntry, error) {
	const sql = `
        SELECT p.id, p.user_id, p.nickname,
               COALESCE(SUM(a.awarded_xp), 0)::int                                AS xp,
               COALESCE(SUM(CASE WHEN a.is_correct THEN 1 ELSE 0 END), 0)::int    AS correct,
               COALESCE(COUNT(a.id), 0)::int                                      AS total,
               p.current_question_idx,
               (p.finished_at_session IS NOT NULL)                                AS finished
        FROM participants p
        LEFT JOIN answers a ON a.participant_id = p.id
        WHERE p.room_id = $1
        GROUP BY p.id, p.user_id, p.nickname, p.current_question_idx,
                 p.finished_at_session, p.joined_at
        ORDER BY xp DESC, p.joined_at ASC`
	rows, err := r.pool.Query(ctx, sql, roomID)
	if err != nil {
		return nil, fmt.Errorf("leaderboard: %w", err)
	}
	defer rows.Close()

	out := make([]LeaderboardEntry, 0, 16)
	for rows.Next() {
		var e LeaderboardEntry
		if err := rows.Scan(
			&e.ParticipantID, &e.UserID, &e.Nickname,
			&e.TotalXP, &e.Correct, &e.Total,
			&e.CurrentQuestionIdx, &e.IsFinished,
		); err != nil {
			return nil, fmt.Errorf("scan leaderboard: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
