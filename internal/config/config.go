// Конфигурация EduQuiz API. Источник — переменные окружения.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr      string
	DatabaseURL   string
	RedisURL      string
	JWTSecret     string
	JWTAccessTTL  time.Duration
	JWTRefreshTTL time.Duration
	Env           string // "development" / "production"

	// SMTP-настройки для отправки писем (восстановление пароля).
	// Пустой SMTPHost/SMTPUsername => отправка превращается в no-op
	// (см. mailer.SendPasswordReset) — это позволяет безопасно
	// деплоить код ДО того, как на сервере прописаны креды Yandex.
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string
	// AppBaseURL — базовый URL фронтенда; из него строится ссылка сброса.
	AppBaseURL string
}

func Load() (*Config, error) {
	cfg := &Config{
		HTTPAddr:      env("HTTP_ADDR", ":8080"),
		DatabaseURL:   env("DATABASE_URL", "postgres://eduquiz:eduquiz@localhost:5432/eduquiz?sslmode=disable"),
		RedisURL:      env("REDIS_URL", "redis://localhost:6379/0"),
		JWTSecret:     env("JWT_SECRET", ""),
		JWTAccessTTL:  envDuration("JWT_ACCESS_TTL", 15*time.Minute),
		JWTRefreshTTL: envDuration("JWT_REFRESH_TTL", 7*24*time.Hour),
		Env:           env("ENV", "development"),

		SMTPHost:     env("SMTP_HOST", ""),
		SMTPPort:     envInt("SMTP_PORT", 465),
		SMTPUsername: env("SMTP_USERNAME", ""),
		SMTPPassword: env("SMTP_PASSWORD", ""),
		SMTPFrom:     env("SMTP_FROM", ""),
		AppBaseURL:   env("APP_BASE_URL", "https://eduquizz.ru"),
	}

	if cfg.Env == "production" && cfg.JWTSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET must be set in production")
	}
	if cfg.JWTSecret == "" {
		cfg.JWTSecret = "dev-secret-change-me"
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
