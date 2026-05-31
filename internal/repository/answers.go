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

// AdminAnswerRow — расширенная запись ответа для админ-панели:
// включает текст вопроса и nickname участника.
type AdminAnswerRow struct {
	ID            uuid.UUID
	QuestionID    uuid.UUID
	QuestionText  string
	ParticipantID uuid.UUID
	Nickname      string
	Value         json.RawMessage
	IsCorrect     *bool
	AwardedXP     int
	ElapsedMs     int
	CreatedAt     time.Time
}

// ListByRoomAdmin — все ответы внутри комнаты (по всем участникам) с join'ами
// на questions и participants. Для админ-просмотра «что кто ответил».
func (r *AnswersRepo) ListByRoomAdmin(ctx context.Context, roomID uuid.UUID) ([]AdminAnswerRow, error) {
	const sql = `
        SELECT a.id, a.question_id, q.text,
               a.participant_id, p.nickname,
               a.value, a.is_correct, a.awarded_xp, a.elapsed_ms, a.created_at
        FROM answers a
        JOIN questions q    ON q.id = a.question_id
        JOIN participants p ON p.id = a.participant_id
        WHERE a.room_id = $1
        ORDER BY a.created_at ASC`
	rows, err := r.pool.Query(ctx, sql, roomID)
	if err != nil {
		return nil, fmt.Errorf("list admin answers: %w", err)
	}
	defer rows.Close()
	out := make([]AdminAnswerRow, 0, 32)
	for rows.Next() {
		var x AdminAnswerRow
		if err := rows.Scan(&x.ID, &x.QuestionID, &x.QuestionText,
			&x.ParticipantID, &x.Nickname,
			&x.Value, &x.IsCorrect, &x.AwardedXP, &x.ElapsedMs, &x.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan admin answer: %w", err)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// DeleteAnswer удаляет одиночный ответ. Для админа: «снять» некорректный ответ.
func (r *AnswersRepo) DeleteAnswer(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM answers WHERE id = $1`
	tag, err := r.pool.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("delete answer: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
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

// ParticipantAnswer — ответ участника с информацией о вопросе для отображения у учителя.
type ParticipantAnswer struct {
	QuestionID   uuid.UUID
	QuestionText string
	QuestionKind string
	Options      json.RawMessage // [{id, text}]
	Correct      json.RawMessage // правильный ответ (для подсветки в UI)
	Value        json.RawMessage // что выбрал/написал участник
	IsCorrect    *bool           // nil для open_text/qna до ручной проверки
	AwardedXP    int
	ElapsedMs    int
	CreatedAt    time.Time
}

// ListByParticipant возвращает все ответы участника в комнате с расширенной
// информацией о вопросе. Используется для teacher-view "история ответов студента".
func (r *AnswersRepo) ListByParticipant(ctx context.Context, roomID, participantID uuid.UUID) ([]ParticipantAnswer, error) {
	const sql = `
        SELECT a.question_id, q.text, q.kind::text, q.options, q.correct,
               a.value, a.is_correct, a.awarded_xp, a.elapsed_ms, a.created_at
        FROM answers a
        JOIN questions q ON q.id = a.question_id
        WHERE a.room_id = $1 AND a.participant_id = $2
        ORDER BY a.created_at ASC`
	rows, err := r.pool.Query(ctx, sql, roomID, participantID)
	if err != nil {
		return nil, fmt.Errorf("list participant answers: %w", err)
	}
	defer rows.Close()

	out := make([]ParticipantAnswer, 0, 8)
	for rows.Next() {
		var pa ParticipantAnswer
		if err := rows.Scan(
			&pa.QuestionID, &pa.QuestionText, &pa.QuestionKind, &pa.Options, &pa.Correct,
			&pa.Value, &pa.IsCorrect, &pa.AwardedXP, &pa.ElapsedMs, &pa.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan participant answer: %w", err)
		}
		out = append(out, pa)
	}
	return out, rows.Err()
}

// PsychometricRow — сырая (participant, question, is_correct)-тройка для
// классической теории тестов. Race-mode: только текущие attempts
// (сброшенные DELETED).
type PsychometricRow struct {
	ParticipantID uuid.UUID
	QuestionID    uuid.UUID
	IsCorrect     *bool
}

// PsychometricsRaw — возвращает все ответы комнаты для CTT-агрегации
// на стороне handler (α Кронбаха, p, r_pb).
func (r *AnswersRepo) PsychometricsRaw(ctx context.Context, roomID uuid.UUID) ([]PsychometricRow, error) {
	const sql = `
        SELECT participant_id, question_id, is_correct
        FROM answers
        WHERE room_id = $1`
	rows, err := r.pool.Query(ctx, sql, roomID)
	if err != nil {
		return nil, fmt.Errorf("psychometrics raw: %w", err)
	}
	defer rows.Close()
	out := make([]PsychometricRow, 0, 64)
	for rows.Next() {
		var p PsychometricRow
		if err := rows.Scan(&p.ParticipantID, &p.QuestionID, &p.IsCorrect); err != nil {
			return nil, fmt.Errorf("scan psychometric: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// LeaderboardEntry — строка таблицы лидеров по сумме XP в комнате.
type LeaderboardEntry struct {
	ParticipantID      uuid.UUID
	UserID             uuid.UUID
	Nickname           string
	TotalXP            int
	Correct            int
	Total              int
	Attempts           int  // суммарное число попыток (включая сброшенные в race)
	CurrentQuestionIdx int  // в timer/race: на каком вопросе сейчас (для race-bar)
	IsFinished         bool // прошёл ли всю сессию (solo-режимы)
	ResetCount         int  // race-режим: сколько раз сбрасывался прогресс
	// Время от старта сессии (room.current_started_at) до финиша
	// студента (participant.finished_at_session) в миллисекундах.
	// nil если студент ещё не финишировал или сессия не стартовала.
	TimeToFinishMs *int64
}

// Leaderboard агрегирует результаты комнаты в порядке убывания XP.
func (r *AnswersRepo) Leaderboard(ctx context.Context, roomID uuid.UUID) ([]LeaderboardEntry, error) {
	const sql = `
        SELECT p.id, p.user_id, p.nickname,
               COALESCE(SUM(a.awarded_xp), 0)::int                                AS xp,
               COALESCE(SUM(CASE WHEN a.is_correct THEN 1 ELSE 0 END), 0)::int    AS correct,
               COALESCE(COUNT(a.id), 0)::int                                      AS total,
               COALESCE(COUNT(a.id), 0)::int + p.reset_count                      AS attempts,
               p.current_question_idx,
               (p.finished_at_session IS NOT NULL)                                AS finished,
               p.reset_count,
               CASE
                   WHEN p.finished_at_session IS NOT NULL AND r.current_started_at IS NOT NULL
                   THEN (EXTRACT(EPOCH FROM (p.finished_at_session - r.current_started_at)) * 1000)::bigint
                   ELSE NULL
               END                                                                AS time_to_finish_ms
        FROM participants p
        LEFT JOIN answers a ON a.participant_id = p.id
        JOIN rooms r ON r.id = p.room_id
        WHERE p.room_id = $1
          AND p.left_at IS NULL
        GROUP BY p.id, p.user_id, p.nickname, p.current_question_idx,
                 p.finished_at_session, p.joined_at, p.reset_count, r.current_started_at
        ORDER BY (p.finished_at_session IS NULL),
                 p.finished_at_session ASC NULLS LAST,
                 xp DESC, p.joined_at ASC`
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
			&e.TotalXP, &e.Correct, &e.Total, &e.Attempts,
			&e.CurrentQuestionIdx, &e.IsFinished, &e.ResetCount,
			&e.TimeToFinishMs,
		); err != nil {
			return nil, fmt.Errorf("scan leaderboard: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
