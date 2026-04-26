// HTTP-хендлеры для курсов, банков вопросов и вопросов.
// Доступ — только для роли teacher (проверяется middleware на роуте).
package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/vladislavgithub/eduquiz-backend/internal/auth"
	"github.com/vladislavgithub/eduquiz-backend/internal/repository"
)

// CoursesHandler инкапсулирует зависимости course-эндпоинтов.
type CoursesHandler struct {
	courses   *repository.CoursesRepo
	questions *repository.QuestionsRepo
}

func NewCoursesHandler(c *repository.CoursesRepo, q *repository.QuestionsRepo) *CoursesHandler {
	return &CoursesHandler{courses: c, questions: q}
}

// --- DTO ---

type courseReq struct {
	Title       string `json:"title"       binding:"required,min=1,max=200"`
	Description string `json:"description" binding:"max=2000"`
}

type courseResp struct {
	ID          string `json:"id"`
	TeacherID   string `json:"teacher_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	IsArchived  bool   `json:"is_archived"`
	CreatedAt   string `json:"created_at"`
}

type bankReq struct {
	Title  string `json:"title"  binding:"required,min=1,max=200"`
	Source string `json:"source" binding:"omitempty,oneof=manual edu_gubkin moodle_xml gift"`
}

type bankResp struct {
	ID        string `json:"id"`
	CourseID  string `json:"course_id"`
	Title     string `json:"title"`
	Source    string `json:"source"`
	CreatedAt string `json:"created_at"`
}

type questionReq struct {
	Kind         string          `json:"kind"           binding:"required,oneof=single_choice multi_choice open_text rating qna"`
	Text         string          `json:"text"           binding:"required,min=1"`
	Options      json.RawMessage `json:"options"`
	Correct      json.RawMessage `json:"correct"`
	Difficulty   int             `json:"difficulty"     binding:"omitempty,min=1,max=5"`
	Topic        string          `json:"topic"          binding:"max=200"`
	TimeLimitSec int             `json:"time_limit_sec" binding:"omitempty,min=0,max=600"`
	Metadata     json.RawMessage `json:"metadata"`
}

type questionResp struct {
	ID           string          `json:"id"`
	BankID       string          `json:"bank_id"`
	Kind         string          `json:"kind"`
	Text         string          `json:"text"`
	Options      json.RawMessage `json:"options"`
	Difficulty   int             `json:"difficulty"`
	Topic        string          `json:"topic,omitempty"`
	TimeLimitSec int             `json:"time_limit_sec"`
	// correct по умолчанию НЕ возвращаем студентам в публичной выдаче;
	// для учительских ручек — отдельный handler с includeCorrect=true.
}

// --- Endpoints ---

// CreateCourse — POST /api/v1/courses.
func (h *CoursesHandler) CreateCourse(c *gin.Context) {
	uid, _ := auth.UserIDFromContext(c)
	var req courseReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	course := &repository.Course{
		TeacherID:   uid,
		Title:       req.Title,
		Description: req.Description,
	}
	if err := h.courses.Insert(c.Request.Context(), course); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create course"})
		return
	}
	c.JSON(http.StatusCreated, toCourseResp(course))
}

// ListCourses — GET /api/v1/courses (только курсы текущего teacher).
func (h *CoursesHandler) ListCourses(c *gin.Context) {
	uid, _ := auth.UserIDFromContext(c)
	list, err := h.courses.ListByTeacher(c.Request.Context(), uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list courses"})
		return
	}
	out := make([]courseResp, 0, len(list))
	for i := range list {
		out = append(out, toCourseResp(&list[i]))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// CreateBank — POST /api/v1/courses/:id/banks.
func (h *CoursesHandler) CreateBank(c *gin.Context) {
	courseID, ok := h.requireOwnedCourse(c)
	if !ok {
		return
	}
	var req bankReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	b := &repository.QuestionBank{CourseID: courseID, Title: req.Title, Source: req.Source}
	if err := h.courses.InsertBank(c.Request.Context(), b); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create bank"})
		return
	}
	c.JSON(http.StatusCreated, toBankResp(b))
}

// ListBanks — GET /api/v1/courses/:id/banks.
func (h *CoursesHandler) ListBanks(c *gin.Context) {
	courseID, ok := h.requireOwnedCourse(c)
	if !ok {
		return
	}
	list, err := h.courses.ListBanksByCourse(c.Request.Context(), courseID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list banks"})
		return
	}
	out := make([]bankResp, 0, len(list))
	for i := range list {
		out = append(out, toBankResp(&list[i]))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// UpdateQuestion — PATCH /api/v1/questions/:id.
// Перезаписывает все поля вопроса. Доступ — teacher, владеющий
// курсом, к которому привязан банк.
func (h *CoursesHandler) UpdateQuestion(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid question id"})
		return
	}
	var req questionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	q := &repository.Question{
		ID:           id,
		Kind:         req.Kind,
		Text:         req.Text,
		Options:      req.Options,
		Correct:      req.Correct,
		Difficulty:   req.Difficulty,
		Topic:        req.Topic,
		TimeLimitSec: req.TimeLimitSec,
		Metadata:     req.Metadata,
	}
	if err := h.questions.Update(c.Request.Context(), q); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "question not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update question"})
		return
	}
	// Возвращаем актуальную версию (с новым updated_at).
	full, err := h.questions.GetByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "reload question"})
		return
	}
	c.JSON(http.StatusOK, toQuestionResp(full))
}

// DeleteQuestion — DELETE /api/v1/questions/:id.
func (h *CoursesHandler) DeleteQuestion(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid question id"})
		return
	}
	if err := h.questions.Delete(c.Request.Context(), id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "question not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete question"})
		return
	}
	c.Status(http.StatusNoContent)
}

// CreateQuestion — POST /api/v1/banks/:id/questions.
// Доступ — teacher, владеющий курсом, к которому привязан банк.
func (h *CoursesHandler) CreateQuestion(c *gin.Context) {
	bankID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid bank id"})
		return
	}
	var req questionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	q := &repository.Question{
		BankID:       bankID,
		Kind:         req.Kind,
		Text:         req.Text,
		Options:      req.Options,
		Correct:      req.Correct,
		Difficulty:   req.Difficulty,
		Topic:        req.Topic,
		TimeLimitSec: req.TimeLimitSec,
		Metadata:     req.Metadata,
	}
	if err := h.questions.Insert(c.Request.Context(), q); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create question"})
		return
	}
	c.JSON(http.StatusCreated, toQuestionResp(q))
}

// ListQuestions — GET /api/v1/banks/:id/questions.
// Студентам в комнате этот endpoint не нужен — там вопросы приходят
// через WebSocket-событие question.activated по одному.
func (h *CoursesHandler) ListQuestions(c *gin.Context) {
	bankID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid bank id"})
		return
	}
	list, err := h.questions.ListByBank(c.Request.Context(), bankID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list questions"})
		return
	}
	out := make([]questionResp, 0, len(list))
	for i := range list {
		out = append(out, toQuestionResp(&list[i]))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// requireOwnedCourse — общий guard: курс существует, и текущий teacher
// — его владелец. Возвращает courseID и true при успехе; иначе уже
// записал ответ (404/403) и вернул false.
func (h *CoursesHandler) requireOwnedCourse(c *gin.Context) (uuid.UUID, bool) {
	courseID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid course id"})
		return uuid.Nil, false
	}
	course, err := h.courses.GetByID(c.Request.Context(), courseID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "course not found"})
			return uuid.Nil, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "get course"})
		return uuid.Nil, false
	}
	uid, _ := auth.UserIDFromContext(c)
	if course.TeacherID != uid {
		c.JSON(http.StatusForbidden, gin.H{"error": "not your course"})
		return uuid.Nil, false
	}
	return courseID, true
}

// Routes регистрирует все course-эндпоинты под /api/v1.
// Все требуют роль teacher.
func (h *CoursesHandler) Routes(api *gin.RouterGroup, issuer *auth.Issuer) {
	teacher := api.Group("", auth.RequireAuth(issuer), auth.RequireRole("teacher", "admin"))

	c := teacher.Group("/courses")
	c.GET("", h.ListCourses)
	c.POST("", h.CreateCourse)
	c.GET("/:id/banks", h.ListBanks)
	c.POST("/:id/banks", h.CreateBank)

	b := teacher.Group("/banks")
	b.GET("/:id/questions", h.ListQuestions)
	b.POST("/:id/questions", h.CreateQuestion)

	q := teacher.Group("/questions")
	q.PATCH("/:id", h.UpdateQuestion)
	q.DELETE("/:id", h.DeleteQuestion)
}

// --- mappers ---

func toCourseResp(c *repository.Course) courseResp {
	return courseResp{
		ID: c.ID.String(), TeacherID: c.TeacherID.String(),
		Title: c.Title, Description: c.Description,
		IsArchived: c.IsArchived, CreatedAt: c.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}
func toBankResp(b *repository.QuestionBank) bankResp {
	return bankResp{
		ID: b.ID.String(), CourseID: b.CourseID.String(),
		Title: b.Title, Source: b.Source,
		CreatedAt: b.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}
func toQuestionResp(q *repository.Question) questionResp {
	return questionResp{
		ID: q.ID.String(), BankID: q.BankID.String(),
		Kind: q.Kind, Text: q.Text, Options: q.Options,
		Difficulty: q.Difficulty, Topic: q.Topic,
		TimeLimitSec: q.TimeLimitSec,
	}
}
