// Репозиторий вопросов внутри банка.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Question — вопрос из банка. Поля Options и Correct хранятся в БД
// как JSONB; в Go это сырое json.RawMessage, чтобы транзитом
// прокидывать через API без лишнего парсинга.
type Question struct {
	ID           uuid.UUID
	BankID       uuid.UUID
	Kind         string // 'single_choice' | 'multi_choice' | 'open_text' | 'rating' | 'qna'
	Text         string
	Options      json.RawMessage
	Correct      json.RawMessage
	Difficulty   int    // 1..5
	Topic        string // optional
	TimeLimitSec int
	Metadata     json.RawMessage
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// QuestionsRepo — репозиторий вопросов.
type QuestionsRepo struct {
	pool *pgxpool.Pool
}

func NewQuestionsRepo(pool *pgxpool.Pool) *QuestionsRepo {
	return &QuestionsRepo{pool: pool}
}

// Insert добавляет один вопрос в банк.
func (r *QuestionsRepo) Insert(ctx context.Context, q *Question) error {
	const sql = `
        INSERT INTO questions (bank_id, kind, text, options, correct, difficulty, topic, time_limit_sec, metadata)
        VALUES ($1, $2, $3, COALESCE($4, '[]'::jsonb), $5, $6, NULLIF($7, ''), $8, COALESCE($9, '{}'::jsonb))
        RETURNING id, created_at, updated_at`
	if q.Difficulty == 0 {
		q.Difficulty = 3
	}
	if q.TimeLimitSec == 0 {
		q.TimeLimitSec = 30
	}
	return r.pool.QueryRow(ctx, sql,
		q.BankID, q.Kind, q.Text,
		nullableJSON(q.Options), nullableJSON(q.Correct),
		q.Difficulty, q.Topic, q.TimeLimitSec,
		nullableJSON(q.Metadata),
	).Scan(&q.ID, &q.CreatedAt, &q.UpdatedAt)
}

// ListByBank возвращает все вопросы банка в стабильном порядке (по created_at).
func (r *QuestionsRepo) ListByBank(ctx context.Context, bankID uuid.UUID) ([]Question, error) {
	const sql = `
        SELECT id, bank_id, kind::text, text, options, COALESCE(correct, 'null'::jsonb),
               difficulty, COALESCE(topic, ''), time_limit_sec, metadata, created_at, updated_at
        FROM questions
        WHERE bank_id = $1
        ORDER BY created_at ASC`
	rows, err := r.pool.Query(ctx, sql, bankID)
	if err != nil {
		return nil, fmt.Errorf("list questions: %w", err)
	}
	defer rows.Close()

	out := make([]Question, 0, 32)
	for rows.Next() {
		var q Question
		if err := rows.Scan(&q.ID, &q.BankID, &q.Kind, &q.Text, &q.Options, &q.Correct,
			&q.Difficulty, &q.Topic, &q.TimeLimitSec, &q.Metadata, &q.CreatedAt, &q.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan question: %w", err)
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// GetByID возвращает вопрос по uuid.
func (r *QuestionsRepo) GetByID(ctx context.Context, id uuid.UUID) (*Question, error) {
	const sql = `
        SELECT id, bank_id, kind::text, text, options, COALESCE(correct, 'null'::jsonb),
               difficulty, COALESCE(topic, ''), time_limit_sec, metadata, created_at, updated_at
        FROM questions
        WHERE id = $1`
	var q Question
	err := r.pool.QueryRow(ctx, sql, id).Scan(
		&q.ID, &q.BankID, &q.Kind, &q.Text, &q.Options, &q.Correct,
		&q.Difficulty, &q.Topic, &q.TimeLimitSec, &q.Metadata, &q.CreatedAt, &q.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get question: %w", err)
	}
	return &q, nil
}

// nullableJSON отдаёт nil, если payload пустой — в SQL это разворачивается
// в COALESCE-дефолт ('[]' / '{}'). Иначе pgx ожидал бы валидный jsonb.
func nullableJSON(p json.RawMessage) any {
	if len(p) == 0 {
		return nil
	}
	return p
}
