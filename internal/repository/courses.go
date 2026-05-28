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
	ID       uuid.UUID
	CourseID uuid.UUID
	Title    string
	Source   string // 'manual' | 'edu_gubkin' | 'moodle_xml' | 'gift'
	// OpenForStudy — преподаватель открыл банк для самостоятельного
	// изучения (режим интервального повторения SM-2). Только при true
	// студенту показывается правильный ответ в /me/sm2/due и /me/sm2/answer.
	OpenForStudy bool
	CreatedAt    time.Time
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

// GetBank возвращает банк по id или ErrNotFound.
func (r *CoursesRepo) GetBank(ctx context.Context, id uuid.UUID) (*QuestionBank, error) {
	const q = `
        SELECT id, course_id, title, source, open_for_study, created_at
        FROM question_banks
        WHERE id = $1`
	var b QuestionBank
	err := r.pool.QueryRow(ctx, q, id).
		Scan(&b.ID, &b.CourseID, &b.Title, &b.Source, &b.OpenForStudy, &b.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get bank: %w", err)
	}
	return &b, nil
}

// AdminCourseRow — курс с дополнительной информацией о владельце и счётчиками,
// которые нужны админ-панели (имя учителя, число банков и вопросов).
type AdminCourseRow struct {
	Course
	TeacherName  string
	TeacherEmail string
	BanksCount   int
}

// ListAllCourses возвращает все курсы (без owner-фильтра) для админ-панели,
// с информацией о владельце и счётчиками. Пагинация по limit/offset.
// q — опциональный фильтр по title/teacher (substring, ILIKE).
func (r *CoursesRepo) ListAllCourses(ctx context.Context, q string, limit, offset int) ([]AdminCourseRow, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	const countSQL = `
        SELECT COUNT(*) FROM courses c
        JOIN users u ON u.id = c.teacher_id
        WHERE $1 = '' OR c.title ILIKE '%' || $1 || '%'
           OR u.full_name ILIKE '%' || $1 || '%' OR u.email ILIKE '%' || $1 || '%'`
	const listSQL = `
        SELECT c.id, c.teacher_id, c.title, COALESCE(c.description, ''),
               c.is_archived, c.created_at, c.updated_at,
               u.full_name, u.email,
               COALESCE((SELECT COUNT(*) FROM question_banks b WHERE b.course_id = c.id), 0)
        FROM courses c
        JOIN users u ON u.id = c.teacher_id
        WHERE $1 = '' OR c.title ILIKE '%' || $1 || '%'
           OR u.full_name ILIKE '%' || $1 || '%' OR u.email ILIKE '%' || $1 || '%'
        ORDER BY c.created_at DESC
        LIMIT $2 OFFSET $3`
	var total int
	if err := r.pool.QueryRow(ctx, countSQL, q).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count courses: %w", err)
	}
	rows, err := r.pool.Query(ctx, listSQL, q, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list all courses: %w", err)
	}
	defer rows.Close()
	out := make([]AdminCourseRow, 0, limit)
	for rows.Next() {
		var c AdminCourseRow
		if err := rows.Scan(&c.ID, &c.TeacherID, &c.Title, &c.Description,
			&c.IsArchived, &c.CreatedAt, &c.UpdatedAt,
			&c.TeacherName, &c.TeacherEmail, &c.BanksCount); err != nil {
			return nil, 0, fmt.Errorf("scan admin course: %w", err)
		}
		out = append(out, c)
	}
	return out, total, rows.Err()
}

// UpdateCourse меняет title и/или teacher_id. Пустые значения не применяются.
func (r *CoursesRepo) UpdateCourse(ctx context.Context, id uuid.UUID, title string, newTeacherID *uuid.UUID) error {
	const q = `
        UPDATE courses
        SET title      = CASE WHEN $2 = '' THEN title ELSE $2 END,
            teacher_id = COALESCE($3, teacher_id),
            updated_at = now()
        WHERE id = $1`
	tag, err := r.pool.Exec(ctx, q, id, title, newTeacherID)
	if err != nil {
		return fmt.Errorf("update course: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteCourse удаляет курс. Банки/вопросы/комнаты/ответы каскадно удалятся
// по существующим FK ON DELETE CASCADE.
func (r *CoursesRepo) DeleteCourse(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM courses WHERE id = $1`
	tag, err := r.pool.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("delete course: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateBank меняет title банка и опционально флаг open_for_study.
// openForStudy — указатель: nil оставляет текущее значение без изменений
// (через COALESCE), не-nil перезаписывает.
func (r *CoursesRepo) UpdateBank(ctx context.Context, id uuid.UUID, title string, openForStudy *bool) error {
	const q = `
        UPDATE question_banks
        SET title          = $2,
            open_for_study = COALESCE($3, open_for_study)
        WHERE id = $1`
	tag, err := r.pool.Exec(ctx, q, id, title, openForStudy)
	if err != nil {
		return fmt.Errorf("update bank: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteBank удаляет банк. Вопросы каскадно удалятся по FK ON DELETE CASCADE.
func (r *CoursesRepo) DeleteBank(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM question_banks WHERE id = $1`
	tag, err := r.pool.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("delete bank: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListBanksByCourse возвращает все банки курса.
func (r *CoursesRepo) ListBanksByCourse(ctx context.Context, courseID uuid.UUID) ([]QuestionBank, error) {
	const q = `
        SELECT id, course_id, title, source, open_for_study, created_at
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
		if err := rows.Scan(&b.ID, &b.CourseID, &b.Title, &b.Source, &b.OpenForStudy, &b.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan bank: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
