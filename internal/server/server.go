// HTTP-сервер EduQuiz API на базе Gin.
package server

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/vladislavgithub/eduquiz-backend/internal/config"
)

func New(cfg *config.Config) *http.Server {
	if cfg.Env == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	r.GET("/readyz", func(c *gin.Context) {
		// TODO: проверять postgres и redis подключения
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})

	api := r.Group("/api/v1")
	{
		api.GET("/", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{
				"name":    "EduQuiz API",
				"version": "0.1.0",
			})
		})
		// TODO: подключить роуты auth, courses, rooms, gamification
	}

	return &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}
