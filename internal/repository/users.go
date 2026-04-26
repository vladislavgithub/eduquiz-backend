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
