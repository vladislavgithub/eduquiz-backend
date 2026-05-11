// Репозиторий пользователей: чтение и запись в таблицу users.
// Возвращает доменные ошибки (ErrNotFound, ErrEmailTaken),
// чтобы хендлеры могли их различать без знания SQL.
package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// User — доменная модель пользователя.
type User struct {
	ID           uuid.UUID
	Email        string
	PasswordHash string
	FullName     string
	Role         string // 'teacher' | 'student' | 'admin'
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Доменные ошибки слоя репозитория.
var (
	ErrNotFound   = errors.New("user not found")
	ErrEmailTaken = errors.New("email already taken")
)

// UsersRepo — репозиторий пользователей поверх pgxpool.
type UsersRepo struct {
	pool *pgxpool.Pool
}

func NewUsersRepo(pool *pgxpool.Pool) *UsersRepo {
	return &UsersRepo{pool: pool}
}

// Insert создаёт пользователя. При нарушении уникальности email
// возвращает ErrEmailTaken (а не сырую pgError).
func (r *UsersRepo) Insert(ctx context.Context, u *User) error {
	const q = `
        INSERT INTO users (email, password_hash, full_name, role)
        VALUES ($1, $2, $3, $4)
        RETURNING id, created_at, updated_at`
	err := r.pool.QueryRow(ctx, q,
		strings.ToLower(strings.TrimSpace(u.Email)),
		u.PasswordHash,
		strings.TrimSpace(u.FullName),
		u.Role,
	).Scan(&u.ID, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrEmailTaken
		}
		return fmt.Errorf("insert user: %w", err)
	}
	return nil
}

// GetByEmail возвращает пользователя по email (case-insensitive).
// Если такого нет — ErrNotFound.
func (r *UsersRepo) GetByEmail(ctx context.Context, email string) (*User, error) {
	const q = `
        SELECT id, email, password_hash, full_name, role, created_at, updated_at
        FROM users
        WHERE email = $1`
	var u User
	err := r.pool.QueryRow(ctx, q, strings.ToLower(strings.TrimSpace(email))).
		Scan(&u.ID, &u.Email, &u.PasswordHash, &u.FullName, &u.Role, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get user by email: %w", err)
	}
	return &u, nil
}

// GetByID возвращает пользователя по uuid.
func (r *UsersRepo) GetByID(ctx context.Context, id uuid.UUID) (*User, error) {
	const q = `
        SELECT id, email, password_hash, full_name, role, created_at, updated_at
        FROM users
        WHERE id = $1`
	var u User
	err := r.pool.QueryRow(ctx, q, id).
		Scan(&u.ID, &u.Email, &u.PasswordHash, &u.FullName, &u.Role, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get user by id: %w", err)
	}
	return &u, nil
}

// ListAll возвращает пагинированный список пользователей с опциональными
// фильтрами по подстроке (email/full_name) и роли. Используется админ-панелью.
// total — общее количество подходящих под фильтр (для UI пагинации).
func (r *UsersRepo) ListAll(ctx context.Context, q string, role string, limit, offset int) ([]User, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	// Используем параметры с COALESCE, чтобы фильтры могли отсутствовать.
	const countSQL = `
        SELECT COUNT(*) FROM users
        WHERE ($1 = '' OR email ILIKE '%' || $1 || '%' OR full_name ILIKE '%' || $1 || '%')
          AND ($2 = '' OR role::text = $2)`
	const listSQL = `
        SELECT id, email, password_hash, full_name, role::text, created_at, updated_at
        FROM users
        WHERE ($1 = '' OR email ILIKE '%' || $1 || '%' OR full_name ILIKE '%' || $1 || '%')
          AND ($2 = '' OR role::text = $2)
        ORDER BY created_at DESC
        LIMIT $3 OFFSET $4`
	var total int
	if err := r.pool.QueryRow(ctx, countSQL, q, role).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count users: %w", err)
	}
	rows, err := r.pool.Query(ctx, listSQL, q, role, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	out := make([]User, 0, limit)
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.FullName, &u.Role, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan user: %w", err)
		}
		out = append(out, u)
	}
	return out, total, rows.Err()
}

// UpdateProfile обновляет full_name и/или role одной транзакцией.
// Если поле — пустая строка, оно не меняется. Возвращает ErrNotFound если юзера нет.
func (r *UsersRepo) UpdateProfile(ctx context.Context, id uuid.UUID, fullName, role string) error {
	const q = `
        UPDATE users
        SET full_name = CASE WHEN $2 = '' THEN full_name ELSE $2 END,
            role      = CASE WHEN $3 = '' THEN role ELSE $3::user_role END,
            updated_at = now()
        WHERE id = $1`
	tag, err := r.pool.Exec(ctx, q, id, strings.TrimSpace(fullName), role)
	if err != nil {
		return fmt.Errorf("update user profile: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdatePassword меняет хеш пароля пользователя. Хеш считается на уровне выше
// (handler передаёт bcrypt-hash).
func (r *UsersRepo) UpdatePassword(ctx context.Context, id uuid.UUID, hash string) error {
	const q = `UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`
	tag, err := r.pool.Exec(ctx, q, id, hash)
	if err != nil {
		return fmt.Errorf("update user password: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete удаляет пользователя. Связанные сущности (courses, rooms, answers)
// удалятся по FK ON DELETE CASCADE (см. migrations/0002_courses.up.sql).
func (r *UsersRepo) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM users WHERE id = $1`
	tag, err := r.pool.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
