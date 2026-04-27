// Аналитика банка: матрица «участник × вопрос» правильных/неправильных
// ответов. Каждый участник — последний (по времени) ответ на каждый
// вопрос банка; вопросы, на которые он не отвечал, игнорируются —
// caller потом отфильтрует или подставит 0.
package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AnalyticsRepo struct {
	pool *pgxpool.Pool
}

func NewAnalyticsRepo(pool *pgxpool.Pool) *AnalyticsRepo {
	return &AnalyticsRepo{pool: pool}
}

// BankResponseMatrix возвращает матрицу ответов по банку.
//   - questionIDs — id вопросов банка в детерминированном порядке;
//   - participants — id уникальных участников, ответивших хотя бы на
//     один вопрос банка (стабильный порядок: по participants.joined_at);
//   - matrix[i][j] = 1, если participant[i] ответил правильно на
//     question[j]; 0 — иначе (включая «не отвечал»).
//
// Используется для расчёта α Кронбаха и item statistics.
func (r *AnalyticsRepo) BankResponseMatrix(
	ctx context.Context, bankID uuid.UUID,
) (questionIDs []uuid.UUID, participants []uuid.UUID, matrix [][]int, err error) {
	// 1. Берём вопросы банка (стабильный порядок по created_at).
	rows, err := r.pool.Query(ctx, `
		SELECT id FROM questions
		WHERE bank_id = $1
		ORDER BY created_at, id`, bankID)
	if err != nil {
		return
	}
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return
		}
		questionIDs = append(questionIDs, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return
	}
	if len(questionIDs) == 0 {
		return
	}

	// 2. Берём всех уникальных участников комнат, использовавших
	// этот банк. participants_id — это participants.user_id
	// (один человек = одна строка, даже если он играл в нескольких
	// сессиях). Берём агрегацию по user_id, чтобы один и тот же
	// студент не учитывался дважды.
	rows, err = r.pool.Query(ctx, `
		SELECT DISTINCT p.user_id
		FROM participants p
		JOIN rooms r ON r.id = p.room_id
		WHERE r.bank_id = $1
		ORDER BY p.user_id`, bankID)
	if err != nil {
		return
	}
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return
		}
		participants = append(participants, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return
	}
	if len(participants) == 0 {
		return
	}

	// 3. Составляем индексы для быстрого построения матрицы.
	qIdx := make(map[uuid.UUID]int, len(questionIDs))
	for i, id := range questionIDs {
		qIdx[id] = i
	}
	pIdx := make(map[uuid.UUID]int, len(participants))
	for i, id := range participants {
		pIdx[id] = i
	}

	matrix = make([][]int, len(participants))
	for i := range matrix {
		matrix[i] = make([]int, len(questionIDs))
	}

	// 4. Тянем все ответы — для каждой пары (user, question)
	// берём ПОСЛЕДНИЙ по answered_at, чтобы переответы тоже учитывались.
	// is_correct=true считаем за 1, остальное (false, NULL) — за 0.
	const ansSQL = `
		SELECT DISTINCT ON (p.user_id, a.question_id)
		       p.user_id, a.question_id, a.is_correct
		FROM answers a
		JOIN participants p ON p.id = a.participant_id
		JOIN rooms r        ON r.id = p.room_id
		WHERE r.bank_id = $1
		ORDER BY p.user_id, a.question_id, a.created_at DESC`
	rows, err = r.pool.Query(ctx, ansSQL, bankID)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var uID, qID uuid.UUID
		var correct *bool
		if err = rows.Scan(&uID, &qID, &correct); err != nil {
			return
		}
		i, okI := pIdx[uID]
		j, okJ := qIdx[qID]
		if !okI || !okJ {
			continue
		}
		if correct != nil && *correct {
			matrix[i][j] = 1
		}
	}
	err = rows.Err()
	return
}
