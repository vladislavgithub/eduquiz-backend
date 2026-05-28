// Админ-эндпойнты: read+CRUD по пользователям, курсам, комнатам, ответам.
// Все маршруты под auth.RequireRole("admin"). На каждое destructive-действие
// пишем audit-лог в slog с admin_id и target.
//
// Безопасность:
// - Никаких password_hash в ответах (DTO без этого поля).
// - Админ не может удалить/демоутить сам себя (защита от self-lockout).
// - Reset password проходит через auth.ValidatePasswordStrength.
// - Все ID-параметры парсятся через uuid.Parse → невалидное → 400.
package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vladislavgithub/eduquiz-backend/internal/auth"
	"github.com/vladislavgithub/eduquiz-backend/internal/repository"
)

// AdminHandler — все админские эндпойнты в одном handler-е.
type AdminHandler struct {
	users   *repository.UsersRepo
	courses *repository.CoursesRepo
	rooms   *repository.RoomsRepo
	answers *repository.AnswersRepo
	pool    *pgxpool.Pool // для агрегатов в /stats
	log     *slog.Logger
}

func NewAdminHandler(
	users *repository.UsersRepo,
	courses *repository.CoursesRepo,
	rooms *repository.RoomsRepo,
	answers *repository.AnswersRepo,
	pool *pgxpool.Pool,
) *AdminHandler {
	return &AdminHandler{
		users:   users,
		courses: courses,
		rooms:   rooms,
		answers: answers,
		pool:    pool,
		log:     slog.Default().With("module", "admin"),
	}
}

// Routes регистрирует все админ-маршруты под /api/v1/admin/* с RequireRole("admin").
func (h *AdminHandler) Routes(api *gin.RouterGroup, issuer *auth.Issuer) {
	adm := api.Group("/admin", auth.RequireAuth(issuer), auth.RequireRole("admin"))

	adm.GET("/stats", h.Stats)

	adm.GET("/users", h.ListUsers)
	adm.POST("/users", h.CreateUser)
	adm.PATCH("/users/:uid", h.UpdateUser)
	adm.POST("/users/:uid/reset-password", h.ResetPassword)
	adm.DELETE("/users/:uid", h.DeleteUser)

	adm.GET("/courses", h.ListCourses)
	adm.PATCH("/courses/:cid", h.UpdateCourse)
	adm.DELETE("/courses/:cid", h.DeleteCourse)

	adm.GET("/rooms", h.ListRooms)
	adm.POST("/rooms/:rid/finish", h.FinishRoom)
	adm.DELETE("/rooms/:rid", h.DeleteRoom)

	adm.GET("/rooms/:rid/answers", h.RoomAnswers)
	adm.DELETE("/answers/:aid", h.DeleteAnswer)
}

// ---------- DTO ----------

type adminUserResp struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	FullName  string `json:"full_name"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
}

func toAdminUserResp(u repository.User) adminUserResp {
	return adminUserResp{
		ID:        u.ID.String(),
		Email:     u.Email,
		FullName:  u.FullName,
		Role:      u.Role,
		CreatedAt: u.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

type adminCourseResp struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	TeacherID    string `json:"teacher_id"`
	TeacherName  string `json:"teacher_name"`
	TeacherEmail string `json:"teacher_email"`
	BanksCount   int    `json:"banks_count"`
	IsArchived   bool   `json:"is_archived"`
	CreatedAt    string `json:"created_at"`
}

type adminRoomResp struct {
	ID                string `json:"id"`
	Code              string `json:"code"`
	Title             string `json:"title"`
	Status            string `json:"status"`
	CourseID          string `json:"course_id"`
	CourseTitle       string `json:"course_title"`
	BankID            string `json:"bank_id"`
	TeacherID         string `json:"teacher_id"`
	TeacherName       string `json:"teacher_name"`
	ParticipantsCount int    `json:"participants_count"`
	CreatedAt         string `json:"created_at"`
}

type adminAnswerResp struct {
	ID            string          `json:"id"`
	QuestionID    string          `json:"question_id"`
	QuestionText  string          `json:"question_text"`
	ParticipantID string          `json:"participant_id"`
	Nickname      string          `json:"nickname"`
	Value         json.RawMessage `json:"value"`
	IsCorrect     *bool           `json:"is_correct"`
	AwardedXP     int             `json:"awarded_xp"`
	ElapsedMs     int             `json:"elapsed_ms"`
	CreatedAt     string          `json:"created_at"`
}

// ---------- helpers ----------

// adminID возвращает id текущего админа (для audit) или Nil.
func adminID(c *gin.Context) uuid.UUID {
	uid, _ := auth.UserIDFromContext(c)
	return uid
}

func parseLimitOffset(c *gin.Context) (int, int) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	return limit, offset
}

// ---------- /admin/stats ----------

func (h *AdminHandler) Stats(c *gin.Context) {
	const sql = `
        SELECT
          (SELECT COUNT(*) FROM users)                                        AS users_total,
          (SELECT COUNT(*) FROM users WHERE role='teacher')                   AS teachers,
          (SELECT COUNT(*) FROM users WHERE role='student')                   AS students,
          (SELECT COUNT(*) FROM users WHERE role='admin')                     AS admins,
          (SELECT COUNT(*) FROM courses)                                      AS courses_total,
          (SELECT COUNT(*) FROM question_banks)                               AS banks_total,
          (SELECT COUNT(*) FROM questions)                                    AS questions_total,
          (SELECT COUNT(*) FROM rooms)                                        AS rooms_total,
          (SELECT COUNT(*) FROM rooms WHERE status='active'::room_status)     AS rooms_active,
          (SELECT COUNT(*) FROM rooms WHERE status='finished'::room_status)   AS rooms_finished,
          (SELECT COUNT(*) FROM answers)                                      AS answers_total`
	var s struct {
		UsersTotal, Teachers, Students, Admins   int
		CoursesTotal, BanksTotal, QuestionsTotal int
		RoomsTotal, RoomsActive, RoomsFinished   int
		AnswersTotal                             int
	}
	if err := h.pool.QueryRow(c.Request.Context(), sql).Scan(
		&s.UsersTotal, &s.Teachers, &s.Students, &s.Admins,
		&s.CoursesTotal, &s.BanksTotal, &s.QuestionsTotal,
		&s.RoomsTotal, &s.RoomsActive, &s.RoomsFinished, &s.AnswersTotal,
	); err != nil {
		h.log.Error("admin stats", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "stats failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"users_total":     s.UsersTotal,
		"teachers":        s.Teachers,
		"students":        s.Students,
		"admins":          s.Admins,
		"courses_total":   s.CoursesTotal,
		"banks_total":     s.BanksTotal,
		"questions_total": s.QuestionsTotal,
		"rooms_total":     s.RoomsTotal,
		"rooms_active":    s.RoomsActive,
		"rooms_finished":  s.RoomsFinished,
		"answers_total":   s.AnswersTotal,
	})
}

// ---------- /admin/users ----------

func (h *AdminHandler) ListUsers(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	role := strings.TrimSpace(c.Query("role"))
	limit, offset := parseLimitOffset(c)
	users, total, err := h.users.ListAll(c.Request.Context(), q, role, limit, offset)
	if err != nil {
		h.log.Error("admin list users", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list users failed"})
		return
	}
	items := make([]adminUserResp, 0, len(users))
	for _, u := range users {
		items = append(items, toAdminUserResp(u))
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "limit": limit, "offset": offset})
}

type adminCreateUserReq struct {
	Email    string `json:"email"     binding:"required,email"`
	FullName string `json:"full_name" binding:"required,min=2,max=200"`
	Password string `json:"password"  binding:"required,min=8,max=72"`
	Role     string `json:"role"      binding:"required,oneof=teacher student admin"`
}

// CreateUser обрабатывает POST /api/v1/admin/users.
// Админ заводит учётку (teacher/student/admin) с заданным паролем.
// В отличие от публичной регистрации: роль берётся как есть, проверка на
// одноразовые домены не применяется, токены НЕ выпускаются.
func (h *AdminHandler) CreateUser(c *gin.Context) {
	var req adminCreateUserReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Та же проверка силы пароля, что и в публичной регистрации.
	if err := auth.ValidatePasswordStrength(req.Password); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "hash password"})
		return
	}
	u := &repository.User{
		Email:        strings.ToLower(strings.TrimSpace(req.Email)),
		PasswordHash: hash,
		FullName:     strings.TrimSpace(req.FullName),
		Role:         req.Role,
	}
	if err := h.users.Insert(c.Request.Context(), u); err != nil {
		if errors.Is(err, repository.ErrEmailTaken) {
			c.JSON(http.StatusConflict, gin.H{"error": "email already registered"})
			return
		}
		h.log.Error("admin create user", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create user"})
		return
	}
	h.log.Info("admin_op", "admin_id", adminID(c), "op", "user.create", "target", u.ID, "role", u.Role)
	c.JSON(http.StatusCreated, gin.H{"user": userResp{
		ID: u.ID.String(), Email: u.Email, FullName: u.FullName, Role: u.Role,
	}})
}

type adminUpdateUserReq struct {
	FullName *string `json:"full_name" binding:"omitempty,min=2,max=200"`
	Role     *string `json:"role"      binding:"omitempty,oneof=teacher student admin"`
}

func (h *AdminHandler) UpdateUser(c *gin.Context) {
	uid, err := uuid.Parse(c.Param("uid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user id"})
		return
	}
	var req adminUpdateUserReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Защита от self-demote: админ не может понизить себе роль.
	if uid == adminID(c) && req.Role != nil && *req.Role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "нельзя сменить свою admin-роль"})
		return
	}
	fullName := ""
	if req.FullName != nil {
		fullName = *req.FullName
	}
	role := ""
	if req.Role != nil {
		role = *req.Role
	}
	if err := h.users.UpdateProfile(c.Request.Context(), uid, fullName, role); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
			return
		}
		h.log.Error("admin update user", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
		return
	}
	h.log.Info("admin_op", "admin_id", adminID(c), "op", "user.update", "target", uid, "full_name?", req.FullName != nil, "role?", req.Role != nil)
	c.Status(http.StatusNoContent)
}

type adminResetPwdReq struct {
	NewPassword string `json:"new_password" binding:"required,min=8,max=72"`
}

func (h *AdminHandler) ResetPassword(c *gin.Context) {
	uid, err := uuid.Parse(c.Param("uid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user id"})
		return
	}
	var req adminResetPwdReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := auth.ValidatePasswordStrength(req.NewPassword); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "hash failed"})
		return
	}
	if err := h.users.UpdatePassword(c.Request.Context(), uid, hash); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
			return
		}
		h.log.Error("admin reset pwd", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "reset failed"})
		return
	}
	h.log.Info("admin_op", "admin_id", adminID(c), "op", "user.reset_password", "target", uid)
	c.Status(http.StatusNoContent)
}

func (h *AdminHandler) DeleteUser(c *gin.Context) {
	uid, err := uuid.Parse(c.Param("uid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user id"})
		return
	}
	// Self-protect: админ не может удалить сам себя.
	if uid == adminID(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "нельзя удалить свою учётку"})
		return
	}
	if err := h.users.Delete(c.Request.Context(), uid); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
			return
		}
		h.log.Error("admin delete user", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete failed"})
		return
	}
	h.log.Info("admin_op", "admin_id", adminID(c), "op", "user.delete", "target", uid)
	c.Status(http.StatusNoContent)
}

// ---------- /admin/courses ----------

func (h *AdminHandler) ListCourses(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	limit, offset := parseLimitOffset(c)
	list, total, err := h.courses.ListAllCourses(c.Request.Context(), q, limit, offset)
	if err != nil {
		h.log.Error("admin list courses", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list courses failed"})
		return
	}
	items := make([]adminCourseResp, 0, len(list))
	for _, x := range list {
		items = append(items, adminCourseResp{
			ID:           x.ID.String(),
			Title:        x.Title,
			Description:  x.Description,
			TeacherID:    x.TeacherID.String(),
			TeacherName:  x.TeacherName,
			TeacherEmail: x.TeacherEmail,
			BanksCount:   x.BanksCount,
			IsArchived:   x.IsArchived,
			CreatedAt:    x.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "limit": limit, "offset": offset})
}

type adminUpdateCourseReq struct {
	Title     *string `json:"title"      binding:"omitempty,min=1,max=200"`
	TeacherID *string `json:"teacher_id" binding:"omitempty,uuid"`
}

func (h *AdminHandler) UpdateCourse(c *gin.Context) {
	cid, err := uuid.Parse(c.Param("cid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid course id"})
		return
	}
	var req adminUpdateCourseReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	title := ""
	if req.Title != nil {
		title = *req.Title
	}
	var newTeacher *uuid.UUID
	if req.TeacherID != nil {
		t, parseErr := uuid.Parse(*req.TeacherID)
		if parseErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid teacher id"})
			return
		}
		// Проверим что цель — реальный teacher/admin.
		target, getErr := h.users.GetByID(c.Request.Context(), t)
		if getErr != nil || (target.Role != "teacher" && target.Role != "admin") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "target user must be teacher or admin"})
			return
		}
		newTeacher = &t
	}
	if err := h.courses.UpdateCourse(c.Request.Context(), cid, title, newTeacher); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "course not found"})
			return
		}
		h.log.Error("admin update course", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
		return
	}
	h.log.Info("admin_op", "admin_id", adminID(c), "op", "course.update", "target", cid)
	c.Status(http.StatusNoContent)
}

func (h *AdminHandler) DeleteCourse(c *gin.Context) {
	cid, err := uuid.Parse(c.Param("cid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid course id"})
		return
	}
	if err := h.courses.DeleteCourse(c.Request.Context(), cid); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "course not found"})
			return
		}
		h.log.Error("admin delete course", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete failed"})
		return
	}
	h.log.Info("admin_op", "admin_id", adminID(c), "op", "course.delete", "target", cid)
	c.Status(http.StatusNoContent)
}

// ---------- /admin/rooms ----------

func (h *AdminHandler) ListRooms(c *gin.Context) {
	status := strings.TrimSpace(c.Query("status"))
	limit, offset := parseLimitOffset(c)
	list, total, err := h.rooms.ListAllRooms(c.Request.Context(), status, limit, offset)
	if err != nil {
		h.log.Error("admin list rooms", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list rooms failed"})
		return
	}
	items := make([]adminRoomResp, 0, len(list))
	for _, x := range list {
		items = append(items, adminRoomResp{
			ID:                x.ID.String(),
			Code:              x.Code,
			Title:             x.Title,
			Status:            x.Status,
			CourseID:          x.CourseID.String(),
			CourseTitle:       x.CourseTitle,
			BankID:            x.BankID.String(),
			TeacherID:         x.TeacherID.String(),
			TeacherName:       x.TeacherName,
			ParticipantsCount: x.ParticipantsCount,
			CreatedAt:         x.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "limit": limit, "offset": offset})
}

func (h *AdminHandler) FinishRoom(c *gin.Context) {
	rid, err := uuid.Parse(c.Param("rid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid room id"})
		return
	}
	if err := h.rooms.ForceFinish(c.Request.Context(), rid); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "room not found"})
			return
		}
		h.log.Error("admin finish room", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "finish failed"})
		return
	}
	h.log.Info("admin_op", "admin_id", adminID(c), "op", "room.force_finish", "target", rid)
	c.Status(http.StatusNoContent)
}

func (h *AdminHandler) DeleteRoom(c *gin.Context) {
	rid, err := uuid.Parse(c.Param("rid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid room id"})
		return
	}
	if err := h.rooms.DeleteRoom(c.Request.Context(), rid); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "room not found"})
			return
		}
		h.log.Error("admin delete room", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete failed"})
		return
	}
	h.log.Info("admin_op", "admin_id", adminID(c), "op", "room.delete", "target", rid)
	c.Status(http.StatusNoContent)
}

func (h *AdminHandler) RoomAnswers(c *gin.Context) {
	rid, err := uuid.Parse(c.Param("rid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid room id"})
		return
	}
	list, err := h.answers.ListByRoomAdmin(c.Request.Context(), rid)
	if err != nil {
		h.log.Error("admin room answers", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list answers failed"})
		return
	}
	items := make([]adminAnswerResp, 0, len(list))
	for _, x := range list {
		items = append(items, adminAnswerResp{
			ID:            x.ID.String(),
			QuestionID:    x.QuestionID.String(),
			QuestionText:  x.QuestionText,
			ParticipantID: x.ParticipantID.String(),
			Nickname:      x.Nickname,
			Value:         x.Value,
			IsCorrect:     x.IsCorrect,
			AwardedXP:     x.AwardedXP,
			ElapsedMs:     x.ElapsedMs,
			CreatedAt:     x.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *AdminHandler) DeleteAnswer(c *gin.Context) {
	aid, err := uuid.Parse(c.Param("aid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid answer id"})
		return
	}
	if err := h.answers.DeleteAnswer(c.Request.Context(), aid); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "answer not found"})
			return
		}
		h.log.Error("admin delete answer", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete failed"})
		return
	}
	h.log.Info("admin_op", "admin_id", adminID(c), "op", "answer.delete", "target", aid)
	c.Status(http.StatusNoContent)
}
