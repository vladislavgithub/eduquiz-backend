// Репозиторий курсов и привязанных к ним банков вопросов.
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Course — учебный курс, принадлежащий преподавателю.
type Course struct {
	ID          uuid.UUID
	TeacherID   uuid.UUID
	Title       string
	Description string
	IsArchived  bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// QuestionBank — именованный набор вопросов внутри курса.
type QuestionBank struct {
	ID        uuid.UUID
	CourseID  uuid.UUID
	Title     string
	Source    string // 'manual' | 'edu_gubkin' | 'moodle_xml' | 'gift'
	CreatedAt time.Time
}

// CoursesRepo — репозиторий курсов.
type CoursesRepo struct {
	pool *pgxpool.Pool
}

func NewCoursesRepo(pool *pgxpool.Pool) *CoursesRepo {
	return &CoursesRepo{pool: pool}
}

// Insert создаёт курс. teacher_id — владелец, заполняется хендлером
// из claims (а не из тела запроса), чтобы клиент не мог подделать.
func (r *CoursesRepo) Insert(ctx context.Context, c *Course) error {
	const q = `
        INSERT INTO courses (teacher_id, title, description)
        VALUES ($1, $2, $3)
        RETURNING id, is_archived, created_at, updated_at`
	return r.pool.QueryRow(ctx, q, c.TeacherID, c.Title, c.Description).
		Scan(&c.ID, &c.IsArchived, &c.CreatedAt, &c.UpdatedAt)
}

// ListByTeacher возвращает все НЕ-архивные курсы преподавателя.
func (r *CoursesRepo) ListByTeacher(ctx context.Context, teacherID uuid.UUID) ([]Course, error) {
	const q = `
        SELECT id, teacher_id, title, COALESCE(description, ''), is_archived, created_at, updated_at
        FROM courses
        WHERE teacher_id = $1 AND NOT is_archived
        ORDER BY created_at DESC`
	rows, err := r.pool.Query(ctx, q, teacherID)
	if err != nil {
		return nil, fmt.Errorf("list courses: %w", err)
	}
	defer rows.Close()

	out := make([]Course, 0, 16)
	for rows.Next() {
		var c Course
		if err := rows.Scan(&c.ID, &c.TeacherID, &c.Title, &c.Description, &c.IsArchived,
			&c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan course: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetByID возвращает курс. Доступ по teacher_id проверяется на уровне
// хендлера (не каждый владелец читает свой курс — админ может тоже).
func (r *CoursesRepo) GetByID(ctx context.Context, id uuid.UUID) (*Course, error) {
	const q = `
        SELECT id, teacher_id, title, COALESCE(description, ''), is_archived, created_at, updated_at
        FROM courses
        WHERE id = $1`
	var c Course
	err := r.pool.QueryRow(ctx, q, id).Scan(
		&c.ID, &c.TeacherID, &c.Title, &c.Description, &c.IsArchived, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get course: %w", err)
	}
	return &c, nil
}

// InsertBank создаёт банк вопросов внутри курса.
func (r *CoursesRepo) InsertBank(ctx context.Context, b *QuestionBank) error {
	if b.Source == "" {
		b.Source = "manual"
	}
	const q = `
        INSERT INTO question_banks (course_id, title, source)
        VALUES ($1, $2, $3)
        RETURNING id, created_at`
	return r.pool.QueryRow(ctx, q, b.CourseID, b.Title, b.Source).
		Scan(&b.ID, &b.CreatedAt)
}

// ListBanksByCourse возвращает все банки курса.
func (r *CoursesRepo) ListBanksByCourse(ctx context.Context, courseID uuid.UUID) ([]QuestionBank, error) {
	const q = `
        SELECT id, course_id, title, source, created_at
        FROM question_banks
        WHERE course_id = $1
        ORDER BY created_at DESC`
	rows, err := r.pool.Query(ctx, q, courseID)
	if err != nil {
		return nil, fmt.Errorf("list banks: %w", err)
	}
	defer rows.Close()

	out := make([]QuestionBank, 0, 8)
	for rows.Next() {
		var b QuestionBank
		if err := rows.Scan(&b.ID, &b.CourseID, &b.Title, &b.Source, &b.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan bank: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
