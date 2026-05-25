// Репозиторий интерактивных комнат: создание, переходы статусов,
// перечисление участников, шестизначный код доступа.
package repository

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Room — состояние интерактивной сессии. JSON-поля settings и метаданные
// хранятся как json.RawMessage для прозрачного транзита через API.
type Room struct {
	ID                uuid.UUID
	CourseID          uuid.UUID
	BankID            uuid.UUID
	Code              string // ровно 6 цифр
	Title             string
	Status            string // см. ENUM room_status
	CurrentQuestionID *uuid.UUID
	CurrentStartedAt  *time.Time
	QuestionOrder     []uuid.UUID
	AskedQuestionIDs  []uuid.UUID
	Settings          json.RawMessage
	CreatedAt         time.Time
	StartedAt         *time.Time
	FinishedAt        *time.Time
}

// Participant — связь пользователя с комнатой.
type Participant struct {
	ID                 uuid.UUID
	RoomID             uuid.UUID
	UserID             uuid.UUID
	Nickname           string
	JoinedAt           time.Time
	LeftAt             *time.Time
	CurrentQuestionIdx int        // в timer/race-режимах: какой вопрос сейчас у этого студента
	FinishedAtSession  *time.Time // в solo-режимах: когда участник прошёл всю сессию
	ResetCount         int        // race-режим: сколько раз сбрасывался прогресс
}

// Доменные ошибки.
var (
	ErrInvalidStatus = errors.New("invalid room status transition")
	ErrAlreadyJoined = errors.New("participant already joined")
)

// RoomsRepo — репозиторий комнат.
type RoomsRepo struct {
	pool *pgxpool.Pool
}

func NewRoomsRepo(pool *pgxpool.Pool) *RoomsRepo {
	return &RoomsRepo{pool: pool}
}

// Create создаёт комнату со случайным шестизначным кодом. На случай
// коллизии (1 на ~миллион) делает несколько попыток.
func (r *RoomsRepo) Create(ctx context.Context, room *Room) error {
	const sql = `
        INSERT INTO rooms (course_id, bank_id, code, title, question_order, settings)
        VALUES ($1, $2, $3, $4, $5, COALESCE($6, '{}'::jsonb))
        RETURNING id, status, created_at`

	for attempt := 0; attempt < 5; attempt++ {
		code, err := newRoomCode()
		if err != nil {
			return fmt.Errorf("generate code: %w", err)
		}
		room.Code = code

		err = r.pool.QueryRow(ctx, sql,
			room.CourseID, room.BankID, code, room.Title,
			room.QuestionOrder, nullableJSON(room.Settings),
		).Scan(&room.ID, &room.Status, &room.CreatedAt)
		if err == nil {
			return nil
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// Коллизия по UNIQUE (code) — повторяем с новым кодом.
			continue
		}
		return fmt.Errorf("create room: %w", err)
	}
	return errors.New("failed to allocate unique room code after 5 attempts")
}

// GetByID возвращает комнату по uuid.
func (r *RoomsRepo) GetByID(ctx context.Context, id uuid.UUID) (*Room, error) {
	return r.scanRoom(ctx, "id = $1", id)
}

// GetByCode возвращает комнату по шестизначному коду.
func (r *RoomsRepo) GetByCode(ctx context.Context, code string) (*Room, error) {
	return r.scanRoom(ctx, "code = $1", code)
}

// AdminRoomRow — комната с инфой о владельце-курсе и счётчиками для админ-панели.
type AdminRoomRow struct {
	Room
	CourseTitle       string
	TeacherID         uuid.UUID
	TeacherName       string
	ParticipantsCount int
}

// ListAllRooms возвращает все комнаты (без owner-фильтра) с фильтром по статусу
// и пагинацией. Для админ-панели.
func (r *RoomsRepo) ListAllRooms(ctx context.Context, status string, limit, offset int) ([]AdminRoomRow, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	const countSQL = `
        SELECT COUNT(*) FROM rooms r
        WHERE $1 = '' OR r.status::text = $1`
	const listSQL = `
        SELECT r.id, r.course_id, r.bank_id, r.code, r.title, r.status::text,
               r.current_question_id, r.current_started_at,
               r.question_order, r.asked_question_ids, r.settings,
               r.created_at, r.started_at, r.finished_at,
               c.title AS course_title,
               c.teacher_id, u.full_name,
               COALESCE((SELECT COUNT(*) FROM participants p WHERE p.room_id = r.id), 0)
        FROM rooms r
        JOIN courses c ON c.id = r.course_id
        JOIN users   u ON u.id = c.teacher_id
        WHERE $1 = '' OR r.status::text = $1
        ORDER BY r.created_at DESC
        LIMIT $2 OFFSET $3`
	var total int
	if err := r.pool.QueryRow(ctx, countSQL, status).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count rooms: %w", err)
	}
	rows, err := r.pool.Query(ctx, listSQL, status, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list all rooms: %w", err)
	}
	defer rows.Close()
	out := make([]AdminRoomRow, 0, limit)
	for rows.Next() {
		var x AdminRoomRow
		if err := rows.Scan(
			&x.ID, &x.CourseID, &x.BankID, &x.Code, &x.Title, &x.Status,
			&x.CurrentQuestionID, &x.CurrentStartedAt,
			&x.QuestionOrder, &x.AskedQuestionIDs, &x.Settings,
			&x.CreatedAt, &x.StartedAt, &x.FinishedAt,
			&x.CourseTitle, &x.TeacherID, &x.TeacherName, &x.ParticipantsCount,
		); err != nil {
			return nil, 0, fmt.Errorf("scan admin room: %w", err)
		}
		out = append(out, x)
	}
	return out, total, rows.Err()
}

// DeleteRoom удаляет комнату. Участники и ответы каскадно (FK ON DELETE CASCADE).
func (r *RoomsRepo) DeleteRoom(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM rooms WHERE id = $1`
	tag, err := r.pool.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("delete room: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ForceFinish переводит комнату в finished независимо от текущего статуса.
// Используется админом для принудительного завершения.
func (r *RoomsRepo) ForceFinish(ctx context.Context, id uuid.UUID) error {
	const q = `
        UPDATE rooms
        SET status = 'finished'::room_status,
            finished_at = COALESCE(finished_at, now())
        WHERE id = $1`
	tag, err := r.pool.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("force finish room: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *RoomsRepo) scanRoom(ctx context.Context, where string, arg any) (*Room, error) {
	sql := `
        SELECT id, course_id, bank_id, code, title, status::text,
               current_question_id, current_started_at,
               question_order, asked_question_ids, settings,
               created_at, started_at, finished_at
        FROM rooms
        WHERE ` + where
	var room Room
	err := r.pool.QueryRow(ctx, sql, arg).Scan(
		&room.ID, &room.CourseID, &room.BankID, &room.Code, &room.Title, &room.Status,
		&room.CurrentQuestionID, &room.CurrentStartedAt,
		&room.QuestionOrder, &room.AskedQuestionIDs, &room.Settings,
		&room.CreatedAt, &room.StartedAt, &room.FinishedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get room: %w", err)
	}
	return &room, nil
}

// SetStatus переводит комнату в новый статус и фиксирует временные метки.
// from — ожидаемый текущий статус (для оптимистичной проверки FSM).
//
// Параметры $2/$3 явно кастуются в room_status: pgx передаёт строку
// как text, а ENUM-сравнение без каста в PostgreSQL не находит совпадений
// и UPDATE возвращает 0 rows. Без каста — баг: статус не меняется,
// и комната «зависает» в active навечно.
func (r *RoomsRepo) SetStatus(ctx context.Context, id uuid.UUID, from, to string) error {
	const sql = `
        UPDATE rooms
        SET status      = $3::room_status,
            started_at  = CASE WHEN $3::room_status = 'active'   AND started_at  IS NULL THEN now() ELSE started_at  END,
            finished_at = CASE WHEN $3::room_status = 'finished' AND finished_at IS NULL THEN now() ELSE finished_at END
        WHERE id = $1 AND status = $2::room_status`
	tag, err := r.pool.Exec(ctx, sql, id, from, to)
	if err != nil {
		return fmt.Errorf("set room status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidStatus
	}
	return nil
}

// SetCurrentQuestion фиксирует активный вопрос и время активации.
// asked — обновлённый список заданных вопросов (передаётся вызывающим).
func (r *RoomsRepo) SetCurrentQuestion(ctx context.Context, id uuid.UUID, qID uuid.UUID, asked []uuid.UUID) error {
	const sql = `
        UPDATE rooms
        SET current_question_id = $2,
            current_started_at  = now(),
            asked_question_ids  = $3,
            status              = 'active'
        WHERE id = $1`
	_, err := r.pool.Exec(ctx, sql, id, qID, asked)
	if err != nil {
		return fmt.Errorf("set current question: %w", err)
	}
	return nil
}

// RestartRoom — комплексный апдейт «новый раунд внутри той же комнаты».
// Применяется когда preподаватель хочет повторно прогнать тест с теми
// же студентами, но возможно другим банком/режимом. Делает в одной
// транзакции:
//
//   - DELETE FROM answers WHERE room_id;
//   - UPDATE participants SET current_question_idx=0, finished_at_session=NULL;
//   - UPDATE rooms SET bank_id, question_order, settings, asked_question_ids=[],
//     current_question_id=NULL, status='waiting'.
//
// asked очищается, статус становится waiting (preподаватель потом
// делает /start чтобы перейти в active).
func (r *RoomsRepo) RestartRoom(
	ctx context.Context,
	roomID uuid.UUID,
	bankID uuid.UUID,
	questionOrder []uuid.UUID,
	settings []byte,
) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`DELETE FROM answers WHERE room_id = $1`, roomID); err != nil {
		return fmt.Errorf("clear answers: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE participants
		SET current_question_idx = 0, finished_at_session = NULL
		WHERE room_id = $1`, roomID); err != nil {
		return fmt.Errorf("reset participants: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE rooms
		SET bank_id             = $2,
		    question_order      = $3,
		    asked_question_ids  = ARRAY[]::uuid[],
		    current_question_id = NULL,
		    current_started_at  = NULL,
		    settings            = $4,
		    status              = 'waiting'
		WHERE id = $1`, roomID, bankID, questionOrder, settings); err != nil {
		return fmt.Errorf("update room: %w", err)
	}
	return tx.Commit(ctx)
}

// AddParticipant подключает пользователя к комнате. Повторное подключение
// того же user_id к той же комнате — ErrAlreadyJoined (UNIQUE-нарушение).
func (r *RoomsRepo) AddParticipant(ctx context.Context, p *Participant) error {
	const sql = `
        INSERT INTO participants (room_id, user_id, nickname)
        VALUES ($1, $2, $3)
        RETURNING id, joined_at`
	err := r.pool.QueryRow(ctx, sql, p.RoomID, p.UserID, p.Nickname).
		Scan(&p.ID, &p.JoinedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrAlreadyJoined
		}
		return fmt.Errorf("add participant: %w", err)
	}
	return nil
}

// ListParticipants возвращает всех (включая ушедших) участников комнаты.
func (r *RoomsRepo) ListParticipants(ctx context.Context, roomID uuid.UUID) ([]Participant, error) {
	const sql = `
        SELECT id, room_id, user_id, nickname, joined_at, left_at,
               current_question_idx, finished_at_session
        FROM participants
        WHERE room_id = $1
        ORDER BY joined_at ASC`
	rows, err := r.pool.Query(ctx, sql, roomID)
	if err != nil {
		return nil, fmt.Errorf("list participants: %w", err)
	}
	defer rows.Close()

	out := make([]Participant, 0, 16)
	for rows.Next() {
		var p Participant
		if err := rows.Scan(
			&p.ID, &p.RoomID, &p.UserID, &p.Nickname, &p.JoinedAt, &p.LeftAt,
			&p.CurrentQuestionIdx, &p.FinishedAtSession,
		); err != nil {
			return nil, fmt.Errorf("scan participant: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// FindParticipant возвращает participant_id пользователя в комнате.
// Используется при отправке ответа: участник идентифицируется
// парой (room, user), а в answers пишется participant_id.
func (r *RoomsRepo) FindParticipant(ctx context.Context, roomID, userID uuid.UUID) (*Participant, error) {
	const sql = `
        SELECT id, room_id, user_id, nickname, joined_at, left_at,
               current_question_idx, finished_at_session
        FROM participants
        WHERE room_id = $1 AND user_id = $2`
	var p Participant
	err := r.pool.QueryRow(ctx, sql, roomID, userID).Scan(
		&p.ID, &p.RoomID, &p.UserID, &p.Nickname, &p.JoinedAt, &p.LeftAt,
		&p.CurrentQuestionIdx, &p.FinishedAtSession,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("find participant: %w", err)
	}
	return &p, nil
}

// AdvanceParticipant двигает указатель студента на следующий вопрос
// (current_question_idx += 1). Используется в timer/race-режимах
// после submit'а ответа. Возвращает обновлённого участника.
//
// Если новый idx >= total — это «студент закончил», ставим
// finished_at_session=now() и не двигаем дальше idx.
func (r *RoomsRepo) AdvanceParticipant(ctx context.Context, participantID uuid.UUID, total int) (*Participant, error) {
	const sql = `
        UPDATE participants
        SET current_question_idx = LEAST(current_question_idx + 1, $2),
            finished_at_session  = CASE
                WHEN current_question_idx + 1 >= $2 AND finished_at_session IS NULL
                    THEN now()
                ELSE finished_at_session
            END
        WHERE id = $1
        RETURNING id, room_id, user_id, nickname, joined_at, left_at,
                  current_question_idx, finished_at_session`
	var p Participant
	err := r.pool.QueryRow(ctx, sql, participantID, total).Scan(
		&p.ID, &p.RoomID, &p.UserID, &p.Nickname, &p.JoinedAt, &p.LeftAt,
		&p.CurrentQuestionIdx, &p.FinishedAtSession,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("advance participant: %w", err)
	}
	return &p, nil
}

// ResetParticipant откатывает указатель студента в начало банка
// (current_question_idx = 0). Используется в race-режиме при
// неправильном ответе — Quizlet Live-style: ошибся → начинай заново.
// Не сбрасывает finished_at_session — если студент уже финишировал,
// сброс игнорируется (теоретически невозможно: после финиша он не
// отвечает).
func (r *RoomsRepo) ResetParticipant(ctx context.Context, participantID uuid.UUID) (*Participant, error) {
	const sql = `
        UPDATE participants
        SET current_question_idx = 0,
            reset_count = reset_count + 1
        WHERE id = $1 AND finished_at_session IS NULL
        RETURNING id, room_id, user_id, nickname, joined_at, left_at,
                  current_question_idx, finished_at_session, reset_count`
	var p Participant
	err := r.pool.QueryRow(ctx, sql, participantID).Scan(
		&p.ID, &p.RoomID, &p.UserID, &p.Nickname, &p.JoinedAt, &p.LeftAt,
		&p.CurrentQuestionIdx, &p.FinishedAtSession, &p.ResetCount,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("reset participant: %w", err)
	}
	return &p, nil
}

// newRoomCode возвращает строку из 6 десятичных цифр '000000'..'999999'.
// Используется crypto/rand, чтобы код было сложно угадать.
func newRoomCode() (string, error) {
	const max = 1_000_000
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}
