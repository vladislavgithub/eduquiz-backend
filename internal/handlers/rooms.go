// HTTP-хендлеры для интерактивных комнат.
//
// Поток:
//
//	teacher: POST /rooms                         → создаёт комнату, выдаёт code
//	student: POST /rooms/join {code, nickname}   → подключается, получает room_id
//	teacher: POST /rooms/:id/start               → переход created/waiting → active
//	teacher: POST /rooms/:id/next                → следующий вопрос (или review/finished)
//	student: POST /rooms/:id/answers             → отправляет ответ
//	any:     GET  /rooms/:id/leaderboard         → сводка по XP
//
// Real-time события (room.state_changed, question.activated, ...) рассылает
// WebSocket-хаб; здесь только REST-эндпоинты, которые меняют состояние.
package handlers

import (
	"encoding/json"
	"errors"
	"math/rand"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/vladislavgithub/eduquiz-backend/internal/auth"
	"github.com/vladislavgithub/eduquiz-backend/internal/repository"
	"github.com/vladislavgithub/eduquiz-backend/internal/services"
)

// Broadcaster — интерфейс трансляции событий (реализуется WS-хабом).
// На REST-уровне нам важно только «послать событие в комнату», не зная,
// как именно оно дойдёт до клиентов (WebSocket / Redis pub-sub / no-op).
type Broadcaster interface {
	Broadcast(roomID uuid.UUID, event string, payload any)
	// SendTo — событие конкретному пользователю в комнате (per-student
	// флоу в timer/race-режимах: «твой следующий вопрос», «ты закончил»).
	SendTo(roomID uuid.UUID, userID uuid.UUID, event string, payload any)
}

// nopBroadcaster — заглушка для случаев, когда WS-хаб ещё не подключён;
// REST-эндпоинты остаются работоспособными, события просто не уйдут.
type nopBroadcaster struct{}

func (nopBroadcaster) Broadcast(uuid.UUID, string, any)              {}
func (nopBroadcaster) SendTo(uuid.UUID, uuid.UUID, string, any)      {}

// NopBroadcaster возвращает заглушку.
func NopBroadcaster() Broadcaster { return nopBroadcaster{} }

// RoomsHandler инкапсулирует зависимости room-эндпоинтов.
type RoomsHandler struct {
	rooms     *repository.RoomsRepo
	courses   *repository.CoursesRepo
	questions *repository.QuestionsRepo
	answers   *repository.AnswersRepo
	users     *repository.UsersRepo
	bcast     Broadcaster
}

func NewRoomsHandler(
	rooms *repository.RoomsRepo,
	courses *repository.CoursesRepo,
	questions *repository.QuestionsRepo,
	answers *repository.AnswersRepo,
	users *repository.UsersRepo,
	bcast Broadcaster,
) *RoomsHandler {
	if bcast == nil {
		bcast = NopBroadcaster()
	}
	return &RoomsHandler{
		rooms: rooms, courses: courses, questions: questions, answers: answers,
		users: users, bcast: bcast,
	}
}

// --- DTO ---

type createRoomReq struct {
	CourseID    string `json:"course_id" binding:"required,uuid"`
	BankID      string `json:"bank_id"   binding:"required,uuid"`
	Title       string `json:"title"     binding:"required,min=1,max=200"`
	Mode        string `json:"mode"      binding:"omitempty,oneof=classic timer race"`
	Shuffle     bool   `json:"shuffle"`      // перетасовать порядок вопросов
	AutoAdvance bool   `json:"auto_advance"` // авто-переход к следующему после review
}

type roomResp struct {
	ID                string          `json:"id"`
	Code              string          `json:"code"`
	Title             string          `json:"title"`
	Status            string          `json:"status"`
	CourseID          string          `json:"course_id"`
	BankID            string          `json:"bank_id"`
	CurrentQuestionID *string         `json:"current_question_id,omitempty"`
	QuestionOrder     []string        `json:"question_order"`
	AskedCount        int             `json:"asked_count"`
	TotalCount        int             `json:"total_count"`
	Settings          json.RawMessage `json:"settings,omitempty"`
}

type joinRoomReq struct {
	Code     string `json:"code"     binding:"required,len=6,numeric"`
	Nickname string `json:"nickname" binding:"required,min=1,max=64"`
}

type joinRoomResp struct {
	RoomID        string `json:"room_id"`
	ParticipantID string `json:"participant_id"`
	Nickname      string `json:"nickname"`
}

type submitAnswerReq struct {
	QuestionID string          `json:"question_id" binding:"required,uuid"`
	Value      json.RawMessage `json:"value"       binding:"required"`
	ElapsedMs  int             `json:"elapsed_ms"  binding:"omitempty,min=0"`
}

type submitAnswerResp struct {
	Correct   *bool `json:"correct,omitempty"`
	AwardedXP int   `json:"awarded_xp"`
}

// --- Endpoints ---

// CreateRoom — POST /api/v1/rooms.
// Сразу заполняет question_order перечислением всех вопросов банка
// в порядке создания (его потом можно переупорядочивать отдельной ручкой).
func (h *RoomsHandler) CreateRoom(c *gin.Context) {
	uid, _ := auth.UserIDFromContext(c)

	var req createRoomReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	courseID, _ := uuid.Parse(req.CourseID)
	bankID, _ := uuid.Parse(req.BankID)

	// Курс должен принадлежать teacher'у.
	course, err := h.courses.GetByID(c.Request.Context(), courseID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "course not found"})
		return
	}
	if course.TeacherID != uid {
		c.JSON(http.StatusForbidden, gin.H{"error": "not your course"})
		return
	}

	// Все вопросы банка — в очередь.
	qs, err := h.questions.ListByBank(c.Request.Context(), bankID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list questions"})
		return
	}
	order := make([]uuid.UUID, 0, len(qs))
	for i := range qs {
		order = append(order, qs[i].ID)
	}
	if req.Shuffle {
		rand.Shuffle(len(order), func(i, j int) {
			order[i], order[j] = order[j], order[i]
		})
	}

	// Сохраняем настройки в room.settings (JSONB), чтобы клиент
	// мог их прочитать через GetRoom и применить (mode, shuffle,
	// auto_advance и любые будущие).
	mode := req.Mode
	if mode == "" {
		mode = "classic"
	}
	settingsJSON, _ := json.Marshal(map[string]any{
		"mode":         mode,
		"shuffle":      req.Shuffle,
		"auto_advance": req.AutoAdvance,
	})

	room := &repository.Room{
		CourseID:      courseID,
		BankID:        bankID,
		Title:         req.Title,
		QuestionOrder: order,
		Settings:      json.RawMessage(settingsJSON),
	}
	if err := h.rooms.Create(c.Request.Context(), room); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create room"})
		return
	}
	c.JSON(http.StatusCreated, toRoomResp(room))
}

// GetRoom — GET /api/v1/rooms/:id.
func (h *RoomsHandler) GetRoom(c *gin.Context) {
	roomID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid room id"})
		return
	}
	room, err := h.rooms.GetByID(c.Request.Context(), roomID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "room not found"})
		return
	}
	c.JSON(http.StatusOK, toRoomResp(room))
}

// JoinRoom — POST /api/v1/rooms/join. Студент подключается по коду.
// Учительский join тоже допустим (например, чтобы видеть аудиторию).
func (h *RoomsHandler) JoinRoom(c *gin.Context) {
	uid, _ := auth.UserIDFromContext(c)
	var req joinRoomReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	room, err := h.rooms.GetByCode(c.Request.Context(), req.Code)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "room not found"})
		return
	}
	if room.Status == "finished" {
		c.JSON(http.StatusGone, gin.H{"error": "room finished"})
		return
	}
	p := &repository.Participant{RoomID: room.ID, UserID: uid, Nickname: req.Nickname}
	if err := h.rooms.AddParticipant(c.Request.Context(), p); err != nil {
		if errors.Is(err, repository.ErrAlreadyJoined) {
			// Идемпотентность: повторный join возвращает существующего участника.
			existing, ferr := h.rooms.FindParticipant(c.Request.Context(), room.ID, uid)
			if ferr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "find existing participant"})
				return
			}
			c.JSON(http.StatusOK, joinRoomResp{
				RoomID: room.ID.String(), ParticipantID: existing.ID.String(),
				Nickname: existing.Nickname,
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "join room"})
		return
	}

	// Перевод комнаты в waiting при первом подключении.
	if room.Status == "created" {
		_ = h.rooms.SetStatus(c.Request.Context(), room.ID, "created", "waiting")
	}

	h.bcast.Broadcast(room.ID, "participant.joined", gin.H{
		"participant_id": p.ID.String(),
		"nickname":       p.Nickname,
	})
	c.JSON(http.StatusOK, joinRoomResp{
		RoomID: room.ID.String(), ParticipantID: p.ID.String(), Nickname: p.Nickname,
	})
}

// StartRoom — POST /api/v1/rooms/:id/start. Активирует первый вопрос.
func (h *RoomsHandler) StartRoom(c *gin.Context) {
	roomID, ok := h.requireRoomOwnership(c)
	if !ok {
		return
	}
	room, _ := h.rooms.GetByID(c.Request.Context(), roomID)
	if len(room.QuestionOrder) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty question_order"})
		return
	}
	mode := readMode(room.Settings)

	if mode == "timer" || mode == "race" {
		// Solo-режимы: server не активирует общий вопрос для room.
		// Каждый студент будет тянуть свой текущий через GET /my/state
		// и двигать через POST /my/answer. Достаточно установить статус.
		if err := h.rooms.SetStatus(c.Request.Context(), room.ID, room.Status, "active"); err != nil &&
			!errors.Is(err, repository.ErrInvalidStatus) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "set active"})
			return
		}
		h.bcast.Broadcast(room.ID, "room.state_changed", gin.H{
			"status": "active",
			"mode":   mode,
		})
		// Каждому участнику персонально — «твой первый вопрос».
		// Отдельным запросом фронт сам возьмёт через MyState; событие
		// просто будит клиент.
		participants, _ := h.rooms.ListParticipants(c.Request.Context(), room.ID)
		for _, p := range participants {
			h.bcast.SendTo(room.ID, p.UserID, "my.session.started", gin.H{
				"total": len(room.QuestionOrder),
			})
		}
		c.JSON(http.StatusOK, gin.H{"status": "active", "mode": mode})
		return
	}

	// Classic-режим: групповая активация первого вопроса.
	first := room.QuestionOrder[0]
	asked := []uuid.UUID{first}
	if err := h.rooms.SetCurrentQuestion(c.Request.Context(), room.ID, first, asked); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "activate question"})
		return
	}
	q, err := h.questions.GetByID(c.Request.Context(), first)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load question"})
		return
	}
	h.bcast.Broadcast(room.ID, "question.activated", toQuestionResp(q))
	h.bcast.Broadcast(room.ID, "room.state_changed", gin.H{"status": "active"})
	c.JSON(http.StatusOK, gin.H{"current_question": toQuestionResp(q)})
}

// readMode извлекает поле mode из room.settings JSONB, fallback classic.
func readMode(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "classic"
	}
	var s struct {
		Mode string `json:"mode"`
	}
	_ = json.Unmarshal(raw, &s)
	if s.Mode == "" {
		return "classic"
	}
	return s.Mode
}

// ReviewQuestion — POST /api/v1/rooms/:id/review.
// Переводит комнату из active в review: рассылает событие
// question.reviewed с правильным ответом всем участникам, чтобы UI
// мог подсветить верный вариант и заблокировать ответ.
//
// Идемпотентен: если уже review/finished — отвечает 200 без изменений.
// Это нужно потому, что вызов триггерит клиент по истечению таймера,
// и одновременно может прийти от нескольких клиентов; мы не хотим 409.
func (h *RoomsHandler) ReviewQuestion(c *gin.Context) {
	roomID, ok := h.requireRoomOwnership(c)
	if !ok {
		return
	}
	room, err := h.rooms.GetByID(c.Request.Context(), roomID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "room not found"})
		return
	}
	if room.Status != "active" {
		// Уже review/finished — ничего не делаем, отвечаем 200.
		c.JSON(http.StatusOK, gin.H{"status": room.Status})
		return
	}
	if room.CurrentQuestionID == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "no active question"})
		return
	}
	q, err := h.questions.GetByID(c.Request.Context(), *room.CurrentQuestionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load question"})
		return
	}
	// Переводим в review через FSM-guard.
	if err := h.rooms.SetStatus(c.Request.Context(), room.ID, "active", "review"); err != nil &&
		!errors.Is(err, repository.ErrInvalidStatus) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "set review"})
		return
	}
	count, _ := h.answers.CountForQuestion(c.Request.Context(), room.ID, q.ID)
	h.bcast.Broadcast(room.ID, "question.reviewed", gin.H{
		"question_id":      q.ID.String(),
		"correct":          q.Correct, // raw json.RawMessage уйдёт как есть
		"answers_received": count,
	})
	h.bcast.Broadcast(room.ID, "room.state_changed", gin.H{"status": "review"})
	c.JSON(http.StatusOK, gin.H{"status": "review"})
}

// NextQuestion — POST /api/v1/rooms/:id/next.
// Если ещё есть вопросы — активирует следующий; иначе — finished.
func (h *RoomsHandler) NextQuestion(c *gin.Context) {
	roomID, ok := h.requireRoomOwnership(c)
	if !ok {
		return
	}
	room, _ := h.rooms.GetByID(c.Request.Context(), roomID)

	asked := room.AskedQuestionIDs
	next, found := pickNext(room.QuestionOrder, asked)
	if !found {
		// SetStatus может вернуть ErrInvalidStatus, если кто-то уже
		// перевёл комнату в finished параллельно. Это не ошибка для
		// клиента — статус всё равно «finished». Реальные SQL-ошибки
		// (потеря коннекта и т.п.) нужно отличать и возвращать 500.
		if err := h.rooms.SetStatus(c.Request.Context(), room.ID, room.Status, "finished"); err != nil &&
			!errors.Is(err, repository.ErrInvalidStatus) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "set status finished"})
			return
		}
		h.bcast.Broadcast(room.ID, "room.state_changed", gin.H{"status": "finished"})
		c.JSON(http.StatusOK, gin.H{"status": "finished"})
		return
	}
	asked = append(asked, next)
	if err := h.rooms.SetCurrentQuestion(c.Request.Context(), room.ID, next, asked); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "activate next"})
		return
	}
	q, err := h.questions.GetByID(c.Request.Context(), next)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load question"})
		return
	}
	h.bcast.Broadcast(room.ID, "question.activated", toQuestionResp(q))
	c.JSON(http.StatusOK, gin.H{"current_question": toQuestionResp(q)})
}

// SubmitAnswer — POST /api/v1/rooms/:id/answers. Доступен любому
// аутентифицированному участнику комнаты.
func (h *RoomsHandler) SubmitAnswer(c *gin.Context) {
	uid, _ := auth.UserIDFromContext(c)
	roomID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid room id"})
		return
	}
	var req submitAnswerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	qID, _ := uuid.Parse(req.QuestionID)

	room, err := h.rooms.GetByID(c.Request.Context(), roomID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "room not found"})
		return
	}
	if room.Status != "active" {
		c.JSON(http.StatusConflict, gin.H{"error": "room is not active"})
		return
	}
	if room.CurrentQuestionID == nil || *room.CurrentQuestionID != qID {
		c.JSON(http.StatusConflict, gin.H{"error": "question not active"})
		return
	}

	participant, err := h.rooms.FindParticipant(c.Request.Context(), roomID, uid)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "not a participant"})
		return
	}
	q, err := h.questions.GetByID(c.Request.Context(), qID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load question"})
		return
	}

	// Если elapsed_ms не передали — считаем сами от current_started_at.
	elapsed := req.ElapsedMs
	if elapsed == 0 && room.CurrentStartedAt != nil {
		elapsed = int(time.Since(*room.CurrentStartedAt) / time.Millisecond)
	}

	correct, ok := services.CheckAnswer(q.Kind, q.Correct, req.Value)
	var corrPtr *bool
	if ok {
		c2 := correct
		corrPtr = &c2
	}
	xp := services.XPAward(correct, q.Difficulty, elapsed, q.TimeLimitSec*1000, services.DefaultXPParams())

	a := &repository.Answer{
		RoomID:        roomID,
		QuestionID:    qID,
		ParticipantID: participant.ID,
		Value:         req.Value,
		IsCorrect:     corrPtr,
		ElapsedMs:     elapsed,
		AwardedXP:     xp,
	}
	if err := h.answers.Insert(c.Request.Context(), a); err != nil {
		if errors.Is(err, repository.ErrDuplicateAnswer) {
			c.JSON(http.StatusConflict, gin.H{"error": "answer already submitted"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save answer"})
		return
	}

	count, _ := h.answers.CountForQuestion(c.Request.Context(), roomID, qID)
	h.bcast.Broadcast(roomID, "question.answered", gin.H{
		"question_id":      qID.String(),
		"participant_id":   participant.ID.String(),
		"answers_received": count,
	})
	c.JSON(http.StatusOK, submitAnswerResp{Correct: corrPtr, AwardedXP: xp})
}

// Leaderboard — GET /api/v1/rooms/:id/leaderboard.
// MyState — GET /api/v1/rooms/:id/my/state.
// Возвращает текущий вопрос и прогресс конкретного студента в
// timer/race-режимах. В classic-режиме отдаёт ту же информацию
// что и общий current_question.
func (h *RoomsHandler) MyState(c *gin.Context) {
	uid, _ := auth.UserIDFromContext(c)
	roomID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid room id"})
		return
	}
	room, err := h.rooms.GetByID(c.Request.Context(), roomID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "room not found"})
		return
	}
	participant, err := h.rooms.FindParticipant(c.Request.Context(), roomID, uid)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "not a participant"})
		return
	}

	total := len(room.QuestionOrder)
	idx := participant.CurrentQuestionIdx
	finished := participant.FinishedAtSession != nil || idx >= total

	resp := gin.H{
		"current_question_idx": idx,
		"total":                total,
		"finished":             finished,
		"mode":                 readMode(room.Settings),
	}
	if !finished && idx < total {
		q, err := h.questions.GetByID(c.Request.Context(), room.QuestionOrder[idx])
		if err == nil {
			resp["current_question"] = toQuestionResp(q)
		}
	}
	c.JSON(http.StatusOK, resp)
}

// SubmitMyAnswer — POST /api/v1/rooms/:id/my/answer.
// Solo-режимы: студент отправляет ответ на свой текущий вопрос,
// сервер двигает его на следующий, отдаёт следующий вопрос (или
// «ты закончил»).
type myAnswerReq struct {
	Value     json.RawMessage `json:"value"      binding:"required"`
	ElapsedMs int             `json:"elapsed_ms" binding:"omitempty,min=0"`
}

func (h *RoomsHandler) SubmitMyAnswer(c *gin.Context) {
	uid, _ := auth.UserIDFromContext(c)
	roomID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid room id"})
		return
	}
	var req myAnswerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	room, err := h.rooms.GetByID(c.Request.Context(), roomID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "room not found"})
		return
	}
	if room.Status != "active" {
		c.JSON(http.StatusConflict, gin.H{"error": "room is not active"})
		return
	}
	mode := readMode(room.Settings)
	if mode != "timer" && mode != "race" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "this endpoint is for timer/race modes only"})
		return
	}

	participant, err := h.rooms.FindParticipant(c.Request.Context(), roomID, uid)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "not a participant"})
		return
	}
	total := len(room.QuestionOrder)
	if participant.CurrentQuestionIdx >= total ||
		participant.FinishedAtSession != nil {
		c.JSON(http.StatusGone, gin.H{"error": "you already finished"})
		return
	}

	currentQID := room.QuestionOrder[participant.CurrentQuestionIdx]
	q, err := h.questions.GetByID(c.Request.Context(), currentQID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load question"})
		return
	}

	correct, ok := services.CheckAnswer(q.Kind, q.Correct, req.Value)
	var corrPtr *bool
	if ok {
		c2 := correct
		corrPtr = &c2
	}
	xp := services.XPAward(correct, q.Difficulty, req.ElapsedMs, q.TimeLimitSec*1000, services.DefaultXPParams())

	a := &repository.Answer{
		RoomID:        roomID,
		QuestionID:    currentQID,
		ParticipantID: participant.ID,
		Value:         req.Value,
		IsCorrect:     corrPtr,
		ElapsedMs:     req.ElapsedMs,
		AwardedXP:     xp,
	}
	if err := h.answers.Insert(c.Request.Context(), a); err != nil {
		// Дубликат на этот же вопрос для участника — продолжаем
		// двигать вперёд.
		if !errors.Is(err, repository.ErrDuplicateAnswer) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "save answer"})
			return
		}
	}

	advanced, err := h.rooms.AdvanceParticipant(c.Request.Context(), participant.ID, total)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "advance"})
		return
	}

	// Broadcast progress всем (для race-bar в реальном времени).
	h.bcast.Broadcast(roomID, "participant.progress", gin.H{
		"participant_id":       advanced.ID.String(),
		"current_question_idx": advanced.CurrentQuestionIdx,
		"finished":             advanced.FinishedAtSession != nil,
	})

	resp := gin.H{
		"correct":              corrPtr,
		"awarded_xp":           xp,
		"current_question_idx": advanced.CurrentQuestionIdx,
		"total":                total,
		"finished":             advanced.FinishedAtSession != nil,
	}
	if advanced.FinishedAtSession == nil && advanced.CurrentQuestionIdx < total {
		// Следующий вопрос — этому студенту.
		nextQID := room.QuestionOrder[advanced.CurrentQuestionIdx]
		nextQ, err := h.questions.GetByID(c.Request.Context(), nextQID)
		if err == nil {
			resp["next_question"] = toQuestionResp(nextQ)
			h.bcast.SendTo(roomID, uid, "my.question.activated",
				toQuestionResp(nextQ))
		}
	} else {
		// Студент закончил — личное событие, и проверка «все ли закончили».
		h.bcast.SendTo(roomID, uid, "my.finished", gin.H{
			"correct": corrPtr, "awarded_xp": xp,
		})
		// Если все участники finished — переводим комнату в finished.
		all, _ := h.rooms.ListParticipants(c.Request.Context(), roomID)
		allDone := true
		for _, p := range all {
			if p.FinishedAtSession == nil {
				allDone = false
				break
			}
		}
		if allDone && len(all) > 0 {
			_ = h.rooms.SetStatus(c.Request.Context(), roomID, "active", "finished")
			h.bcast.Broadcast(roomID, "room.state_changed", gin.H{"status": "finished"})
		}
	}
	c.JSON(http.StatusOK, resp)
}

func (h *RoomsHandler) Leaderboard(c *gin.Context) {
	roomID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid room id"})
		return
	}
	board, err := h.answers.Leaderboard(c.Request.Context(), roomID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "leaderboard"})
		return
	}
	type row struct {
		ParticipantID      string `json:"participant_id"`
		Nickname           string `json:"nickname"`
		TotalXP            int    `json:"total_xp"`
		Correct            int    `json:"correct"`
		Total              int    `json:"total"`
		CurrentQuestionIdx int    `json:"current_question_idx"`
		IsFinished         bool   `json:"is_finished"`
	}
	out := make([]row, 0, len(board))
	for _, e := range board {
		out = append(out, row{
			ParticipantID:      e.ParticipantID.String(),
			Nickname:           e.Nickname,
			TotalXP:            e.TotalXP,
			Correct:            e.Correct,
			Total:              e.Total,
			CurrentQuestionIdx: e.CurrentQuestionIdx,
			IsFinished:         e.IsFinished,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// requireRoomOwnership возвращает roomID и true, если текущий пользователь
// — teacher курса, к которому привязана комната.
func (h *RoomsHandler) requireRoomOwnership(c *gin.Context) (uuid.UUID, bool) {
	roomID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid room id"})
		return uuid.Nil, false
	}
	room, err := h.rooms.GetByID(c.Request.Context(), roomID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "room not found"})
		return uuid.Nil, false
	}
	course, err := h.courses.GetByID(c.Request.Context(), room.CourseID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load course"})
		return uuid.Nil, false
	}
	uid, _ := auth.UserIDFromContext(c)
	if course.TeacherID != uid {
		c.JSON(http.StatusForbidden, gin.H{"error": "not your room"})
		return uuid.Nil, false
	}
	return roomID, true
}

// Routes регистрирует room-эндпоинты под /api/v1.
// /rooms/join и /rooms/:id/answers требуют только аутентификации (любая роль).
// Создание/управление — только teacher.
func (h *RoomsHandler) Routes(api *gin.RouterGroup, issuer *auth.Issuer) {
	authed := api.Group("", auth.RequireAuth(issuer))

	r := authed.Group("/rooms")
	r.GET("/:id", h.GetRoom)
	r.GET("/:id/leaderboard", h.Leaderboard)
	r.POST("/join", h.JoinRoom)
	r.POST("/:id/answers", h.SubmitAnswer)
	// Solo-режимы (timer/race) — per-student endpoints.
	r.GET("/:id/my/state", h.MyState)
	r.POST("/:id/my/answer", h.SubmitMyAnswer)

	teacher := authed.Group("/rooms", auth.RequireRole("teacher", "admin"))
	teacher.POST("", h.CreateRoom)
	teacher.POST("/:id/start", h.StartRoom)
	teacher.POST("/:id/review", h.ReviewQuestion)
	teacher.POST("/:id/next", h.NextQuestion)
}

// --- helpers ---

func toRoomResp(r *repository.Room) roomResp {
	order := make([]string, 0, len(r.QuestionOrder))
	for _, id := range r.QuestionOrder {
		order = append(order, id.String())
	}
	resp := roomResp{
		ID: r.ID.String(), Code: r.Code, Title: r.Title, Status: r.Status,
		CourseID: r.CourseID.String(), BankID: r.BankID.String(),
		QuestionOrder: order,
		AskedCount:    len(r.AskedQuestionIDs),
		TotalCount:    len(r.QuestionOrder),
		Settings:      r.Settings,
	}
	if r.CurrentQuestionID != nil {
		s := r.CurrentQuestionID.String()
		resp.CurrentQuestionID = &s
	}
	return resp
}

// pickNext возвращает первый id из order, ещё не присутствующий в asked.
func pickNext(order, asked []uuid.UUID) (uuid.UUID, bool) {
	seen := make(map[uuid.UUID]struct{}, len(asked))
	for _, a := range asked {
		seen[a] = struct{}{}
	}
	for _, id := range order {
		if _, ok := seen[id]; !ok {
			return id, true
		}
	}
	return uuid.Nil, false
}
