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
	importpkg "github.com/vladislavgithub/eduquiz-backend/internal/services/import"
	"github.com/vladislavgithub/eduquiz-backend/internal/repository"
	"github.com/vladislavgithub/eduquiz-backend/internal/services"
)

// CoursesHandler инкапсулирует зависимости course-эндпоинтов.
type CoursesHandler struct {
	courses   *repository.CoursesRepo
	questions *repository.QuestionsRepo
	analytics *repository.AnalyticsRepo
}

func NewCoursesHandler(
	c *repository.CoursesRepo,
	q *repository.QuestionsRepo,
	a *repository.AnalyticsRepo,
) *CoursesHandler {
	return &CoursesHandler{courses: c, questions: q, analytics: a}
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
	Kind         string          `json:"kind"           binding:"required,oneof=single_choice multi_choice open_text rating qna true_false numerical"`
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
	Correct      json.RawMessage `json:"correct,omitempty"`
	Difficulty   int             `json:"difficulty"`
	Topic        string          `json:"topic,omitempty"`
	TimeLimitSec int             `json:"time_limit_sec"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
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
	uid, _ := auth.UserIDFromContext(c)
	if !h.isQuestionOwnedBy(c, id, uid) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not your question"})
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
	uid, _ := auth.UserIDFromContext(c)
	if !h.isQuestionOwnedBy(c, id, uid) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not your question"})
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

// BankAnalytics — GET /api/v1/banks/:id/analytics.
// Возвращает психометрический отчёт: α Кронбаха, средний балл и
// для каждого вопроса — p-value (трудность) и r_pb (дискриминация).
// Доступ — teacher, владеющий курсом банка.
func (h *CoursesHandler) BankAnalytics(c *gin.Context) {
	bankID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid bank id"})
		return
	}
	// Проверяем владение курсом через первый вопрос банка
	// (банк всегда лежит в каком-то курсе teacher-а; здесь простой
	// SELECT 1 и сравнение teacher_id, без отдельного метода).
	uid, _ := auth.UserIDFromContext(c)
	q, err := h.questions.ListByBank(c.Request.Context(), bankID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list questions"})
		return
	}
	if len(q) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"respondents":          0,
			"item_count":           0,
			"cronbach_alpha":       0,
			"alpha_interpretation": "недостаточно данных",
			"mean_score":           0,
			"max_possible":         0,
			"items":                []any{},
		})
		return
	}
	// Защита: владение проверяем по одной из questions → bank → course.
	// Для простоты: тянем bank через repo (CoursesRepo), сверяем teacher_id.
	if !h.isBankOwnedBy(c, bankID, uid) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not your bank"})
		return
	}

	qIDs, _, matrix, err := h.analytics.BankResponseMatrix(c.Request.Context(), bankID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "analytics query"})
		return
	}
	report := services.AnalyzeBank(matrix)

	// Карта id вопроса → текст, чтобы фронт сразу мог отрисовать таблицу.
	textByID := make(map[uuid.UUID]string, len(q))
	for _, qq := range q {
		textByID[qq.ID] = qq.Text
	}

	type itemDTO struct {
		QuestionID     string  `json:"question_id"`
		Text           string  `json:"text"`
		PValue         float64 `json:"p_value"`
		Discrimination float64 `json:"discrimination"`
		Variance       float64 `json:"variance"`
		AnsweredBy     int     `json:"answered_by"`
	}
	items := make([]itemDTO, 0, len(report.Items))
	for i, st := range report.Items {
		var qid uuid.UUID
		if i < len(qIDs) {
			qid = qIDs[i]
		}
		items = append(items, itemDTO{
			QuestionID:     qid.String(),
			Text:           textByID[qid],
			PValue:         st.PValue,
			Discrimination: st.Discrimination,
			Variance:       st.Variance,
			AnsweredBy:     st.AnsweredBy,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"respondents":          report.Respondents,
		"item_count":           report.ItemCount,
		"cronbach_alpha":       report.CronbachAlpha,
		"alpha_interpretation": services.AlphaInterpretation(report.CronbachAlpha),
		"mean_score":           report.MeanScore,
		"max_possible":         report.MaxPossible,
		"items":                items,
	})
}

// isQuestionOwnedBy — то же самое, но для конкретного вопроса.
// Цепочка question → bank → course → teacher.
func (h *CoursesHandler) isQuestionOwnedBy(c *gin.Context, questionID, teacherID uuid.UUID) bool {
	role, _ := auth.RoleFromContext(c)
	if role == "admin" {
		return true
	}
	q, err := h.questions.GetByID(c.Request.Context(), questionID)
	if err != nil {
		return false
	}
	return h.isBankOwnedBy(c, q.BankID, teacherID)
}

// isBankOwnedBy проверяет, что банк принадлежит teacher-у через цепочку
// bank → course → teacher. Делается отдельным запросом, чтобы не тянуть
// вопросы для проверки владения.
func (h *CoursesHandler) isBankOwnedBy(c *gin.Context, bankID, teacherID uuid.UUID) bool {
	role, _ := auth.RoleFromContext(c)
	if role == "admin" {
		return true
	}
	bank, err := h.courses.GetBank(c.Request.Context(), bankID)
	if err != nil {
		return false
	}
	course, err := h.courses.GetByID(c.Request.Context(), bank.CourseID)
	if err != nil {
		return false
	}
	return course.TeacherID == teacherID
}

// CreateQuestion — POST /api/v1/banks/:id/questions.
// Доступ — teacher, владеющий курсом, к которому привязан банк.
func (h *CoursesHandler) CreateQuestion(c *gin.Context) {
	bankID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid bank id"})
		return
	}
	uid, _ := auth.UserIDFromContext(c)
	if !h.isBankOwnedBy(c, bankID, uid) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not your bank"})
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
	uid, _ := auth.UserIDFromContext(c)
	if !h.isBankOwnedBy(c, bankID, uid) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not your bank"})
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
	role, _ := auth.RoleFromContext(c)
	if role == "admin" {
		return courseID, true
	}
	uid, _ := auth.UserIDFromContext(c)
	if course.TeacherID != uid {
		c.JSON(http.StatusForbidden, gin.H{"error": "not your course"})
		return uuid.Nil, false
	}
	return courseID, true
}

// ImportQuestions — POST /api/v1/banks/:id/questions/import.
// Принимает {format: "moodle_xml"|"gift", content: "..."}, парсит и
// сохраняет вопросы в банк. Возвращает {imported, skipped, warnings}.
func (h *CoursesHandler) ImportQuestions(c *gin.Context) {
	bankID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid bank id"})
		return
	}
	uid, _ := auth.UserIDFromContext(c)
	if !h.isBankOwnedBy(c, bankID, uid) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not your bank"})
		return
	}
	var req struct {
		Format  string `json:"format"  binding:"required,oneof=moodle_xml gift"`
		Content string `json:"content" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var parsed []importpkg.ParsedQuestion
	var warnings []importpkg.ImportWarning
	switch req.Format {
	case "moodle_xml":
		parsed, warnings, err = importpkg.ParseMoodleXML([]byte(req.Content))
	case "gift":
		parsed, warnings, err = importpkg.ParseGIFT(req.Content)
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "parse error: " + err.Error()})
		return
	}

	imported := 0
	for _, pq := range parsed {
		// ParsedOption не имеет json-тегов → маршалим вручную в нужный формат.
		type optDTO struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		}
		optSlice := make([]optDTO, len(pq.Options))
		for i, o := range pq.Options {
			optSlice[i] = optDTO{ID: o.ID, Text: o.Text}
		}
		opts, _ := json.Marshal(optSlice)
		corr, _ := json.Marshal(pq.Correct)
		var meta json.RawMessage
		if len(pq.Metadata) > 0 {
			meta, _ = json.Marshal(pq.Metadata)
		}
		diff := pq.Difficulty
		if diff == 0 {
			diff = 3
		}
		tl := pq.TimeLimitSec
		if tl == 0 {
			tl = 30
		}
		q := &repository.Question{
			BankID: bankID, Kind: pq.Kind, Text: pq.Text,
			Options: opts, Correct: corr,
			Difficulty: diff, TimeLimitSec: tl, Metadata: meta,
		}
		if err := h.questions.Insert(c.Request.Context(), q); err == nil {
			imported++
		}
	}

	type warnDTO struct {
		Index   int    `json:"index"`
		Kind    string `json:"kind"`
		Message string `json:"message"`
	}
	wDTOs := make([]warnDTO, len(warnings))
	for i, w := range warnings {
		wDTOs[i] = warnDTO{Index: w.Index, Kind: w.Kind, Message: w.Message}
	}
	c.JSON(http.StatusOK, gin.H{
		"imported": imported,
		"skipped":  len(parsed) - imported + len(warnings),
		"warnings": wDTOs,
	})
}

// UpdateCourse — PATCH /api/v1/courses/:id.
func (h *CoursesHandler) UpdateCourse(c *gin.Context) {
	courseID, ok := h.requireOwnedCourse(c)
	if !ok {
		return
	}
	var req courseReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.courses.UpdateCourse(c.Request.Context(), courseID, req.Title, nil); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "course not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update course"})
		return
	}
	course, _ := h.courses.GetByID(c.Request.Context(), courseID)
	c.JSON(http.StatusOK, toCourseResp(course))
}

// DeleteCourse — DELETE /api/v1/courses/:id.
func (h *CoursesHandler) DeleteCourse(c *gin.Context) {
	courseID, ok := h.requireOwnedCourse(c)
	if !ok {
		return
	}
	if err := h.courses.DeleteCourse(c.Request.Context(), courseID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "course not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete course"})
		return
	}
	c.Status(http.StatusNoContent)
}

// UpdateBank — PATCH /api/v1/banks/:id.
func (h *CoursesHandler) UpdateBank(c *gin.Context) {
	bankID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid bank id"})
		return
	}
	uid, _ := auth.UserIDFromContext(c)
	if !h.isBankOwnedBy(c, bankID, uid) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not your bank"})
		return
	}
	var req bankReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.courses.UpdateBank(c.Request.Context(), bankID, req.Title); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "bank not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update bank"})
		return
	}
	bank, _ := h.courses.GetBank(c.Request.Context(), bankID)
	c.JSON(http.StatusOK, toBankResp(bank))
}

// DeleteBank — DELETE /api/v1/banks/:id.
func (h *CoursesHandler) DeleteBank(c *gin.Context) {
	bankID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid bank id"})
		return
	}
	uid, _ := auth.UserIDFromContext(c)
	if !h.isBankOwnedBy(c, bankID, uid) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not your bank"})
		return
	}
	if err := h.courses.DeleteBank(c.Request.Context(), bankID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "bank not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete bank"})
		return
	}
	c.Status(http.StatusNoContent)
}

// Routes регистрирует все course-эндпоинты под /api/v1.
// Все требуют роль teacher.
func (h *CoursesHandler) Routes(api *gin.RouterGroup, issuer *auth.Issuer) {
	teacher := api.Group("", auth.RequireAuth(issuer), auth.RequireRole("teacher", "admin"))

	c := teacher.Group("/courses")
	c.GET("", h.ListCourses)
	c.POST("", h.CreateCourse)
	c.PATCH("/:id", h.UpdateCourse)
	c.DELETE("/:id", h.DeleteCourse)
	c.GET("/:id/banks", h.ListBanks)
	c.POST("/:id/banks", h.CreateBank)

	b := teacher.Group("/banks")
	b.PATCH("/:id", h.UpdateBank)
	b.DELETE("/:id", h.DeleteBank)
	b.GET("/:id/questions", h.ListQuestions)
	b.POST("/:id/questions", h.CreateQuestion)
	b.POST("/:id/questions/import", h.ImportQuestions)
	b.GET("/:id/analytics", h.BankAnalytics)

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
		Kind: q.Kind, Text: q.Text, Options: q.Options, Correct: q.Correct,
		Difficulty: q.Difficulty, Topic: q.Topic,
		TimeLimitSec: q.TimeLimitSec, Metadata: q.Metadata,
	}
}
