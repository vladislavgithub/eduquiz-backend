// HTTP-сервер EduQuiz API на базе Gin.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vladislavgithub/eduquiz-backend/internal/auth"
	"github.com/vladislavgithub/eduquiz-backend/internal/config"
	"github.com/vladislavgithub/eduquiz-backend/internal/handlers"
	"github.com/vladislavgithub/eduquiz-backend/internal/repository"
	"github.com/vladislavgithub/eduquiz-backend/internal/ws"
)

// Deps — внешние зависимости сервера. Создаются в main и
// прокидываются сюда: так server остаётся чистой проводкой
// без знания о том, как именно открывается соединение.
type Deps struct {
	DB *pgxpool.Pool
}

// New собирает Gin-роутер, регистрирует все ручки и возвращает
// настроенный *http.Server.
func New(cfg *config.Config, deps Deps) *http.Server {
	if cfg.Env == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(corsMiddleware())

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	r.GET("/readyz", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := deps.DB.Ping(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "not ready", "db": err.Error(),
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})

	// Сборка зависимостей. Issuer и репозитории живут вместе с сервером
	// (они stateless / держат указатели на pool).
	usersRepo := repository.NewUsersRepo(deps.DB)
	coursesRepo := repository.NewCoursesRepo(deps.DB)
	questionsRepo := repository.NewQuestionsRepo(deps.DB)
	roomsRepo := repository.NewRoomsRepo(deps.DB)
	answersRepo := repository.NewAnswersRepo(deps.DB)

	issuer := auth.NewIssuer(cfg.JWTSecret, cfg.JWTAccessTTL, cfg.JWTRefreshTTL)

	// WebSocket-хаб запускается фоновой горутиной; реализует Broadcaster.
	hub := ws.NewHub(slog.Default())
	go hub.Run()
	wsHandler := ws.NewHandler(hub, issuer, roomsRepo, slog.Default())

	authHandler := handlers.NewAuthHandler(usersRepo, issuer)
	coursesHandler := handlers.NewCoursesHandler(coursesRepo, questionsRepo)
	roomsHandler := handlers.NewRoomsHandler(
		roomsRepo, coursesRepo, questionsRepo, answersRepo, usersRepo,
		hub,
	)

	api := r.Group("/api/v1")
	api.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"name":    "EduQuiz API",
			"version": "0.1.0",
		})
	})
	authHandler.Routes(api, issuer)
	coursesHandler.Routes(api, issuer)
	roomsHandler.Routes(api, issuer)

	// WebSocket вне /api/v1 — общепринятая практика для real-time.
	r.GET("/ws/rooms/:id", wsHandler.ServeWS)

	return &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// corsMiddleware — разрешает запросы с любого origin'а. Для MVP
// (Flutter Web на localhost:8089 → API на localhost:8088, плюс будущий
// прод-домен) удобно «*»; в production режиме можно сузить через
// ALLOWED_ORIGIN env. Без явных CORS-заголовков браузер режет
// preflight OPTIONS и любой запрос с Authorization-хедером.
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "" {
			origin = "*"
		}
		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Vary", "Origin")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
		c.Header("Access-Control-Max-Age", "600")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
