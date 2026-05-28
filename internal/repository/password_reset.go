// Репозиторий токенов сброса пароля. Хранит только sha256-хеш токена,
// сам токен никогда не попадает в БД. ErrNotFound (общий для пакета,
// см. users.go) означает «токена нет / истёк / уже использован».
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

// PasswordResetRepo — репозиторий одноразовых токенов сброса пароля.
type PasswordResetRepo struct {
	pool *pgxpool.Pool
}

func NewPasswordResetRepo(pool *pgxpool.Pool) *PasswordResetRepo {
	return &PasswordResetRepo{pool: pool}
}

// Create сохраняет хеш токена сброса для пользователя с заданным сроком жизни.
func (r *PasswordResetRepo) Create(ctx context.Context, userID uuid.UUID, tokenHash string, expiresAt time.Time) error {
	const q = `
        INSERT INTO password_reset_tokens (user_id, token_hash, expires_at)
        VALUES ($1, $2, $3)`
	if _, err := r.pool.Exec(ctx, q, userID, tokenHash, expiresAt); err != nil {
		return fmt.Errorf("create password reset token: %w", err)
	}
	return nil
}

// ConsumeValid атомарно находит живой неиспользованный токен по хешу,
// помечает его used_at=now() и возвращает user_id. Если подходящей
// строки нет (нет токена / истёк / уже использован) — ErrNotFound.
// UPDATE ... RETURNING делает выборку и пометку одной операцией,
// исключая гонку повторного использования одного токена.
func (r *PasswordResetRepo) ConsumeValid(ctx context.Context, tokenHash string) (uuid.UUID, error) {
	const q = `
        UPDATE password_reset_tokens
        SET used_at = now()
        WHERE token_hash = $1
          AND used_at IS NULL
          AND expires_at > now()
        RETURNING user_id`
	var userID uuid.UUID
	err := r.pool.QueryRow(ctx, q, tokenHash).Scan(&userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrNotFound
		}
		return uuid.Nil, fmt.Errorf("consume password reset token: %w", err)
	}
	return userID, nil
}
