// Gin-хендлер для апгрейда HTTP→WebSocket.
// JWT-токен передаётся через query (?token=...), потому что браузерный
// WebSocket-API не позволяет добавлять кастомные заголовки.
package ws

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/vladislavgithub/eduquiz-backend/internal/auth"
	"github.com/vladislavgithub/eduquiz-backend/internal/repository"
)

// Handler — фабрика gin-хендлера WS.
type Handler struct {
	hub     *Hub
	issuer  *auth.Issuer
	rooms   *repository.RoomsRepo
	courses *repository.CoursesRepo
	logger  *slog.Logger
}

func NewHandler(hub *Hub, issuer *auth.Issuer, rooms *repository.RoomsRepo, courses *repository.CoursesRepo, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{hub: hub, issuer: issuer, rooms: rooms, courses: courses, logger: logger}
}

// ServeWS — GET /ws/rooms/:id?token=<access-jwt>.
// Проверяет JWT, что комната существует и пользователь — её участник
// (либо teacher этой комнаты), затем апгрейдит соединение.
func (h *Handler) ServeWS(c *gin.Context) {
	roomID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid room id"})
		return
	}
	tokenStr := c.Query("token")
	if tokenStr == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing token"})
		return
	}
	claims, err := h.issuer.Parse(tokenStr)
	if err != nil || claims.Type != "access" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
		return
	}

	if !h.canJoin(c.Request.Context(), roomID, claims) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not allowed"})
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		// Upgrade сам пишет ответ при ошибке.
		return
	}
	client := &Client{
		hub:    h.hub,
		conn:   conn,
		send:   make(chan []byte, 32),
		roomID: roomID,
		userID: claims.UserID,
		logger: h.logger,
	}
	h.hub.register <- client
	go client.serve()
}

// canJoin: teacher допускается ТОЛЬКО если он владелец курса
// комнаты; любой другой должен быть зарегистрированным participant'ом.
// Это закрывает утечку «question.reviewed» с правильными ответами
// чужому teacher'у через UUID комнаты.
func (h *Handler) canJoin(ctx context.Context, roomID uuid.UUID, claims *auth.Claims) bool {
	if claims.Role == "admin" {
		return true
	}
	if claims.Role == "teacher" {
		room, err := h.rooms.GetByID(ctx, roomID)
		if err != nil {
			return false
		}
		course, err := h.courses.GetByID(ctx, room.CourseID)
		if err != nil {
			return false
		}
		return course.TeacherID == claims.UserID
	}
	_, err := h.rooms.FindParticipant(ctx, roomID, claims.UserID)
	return err == nil
}
