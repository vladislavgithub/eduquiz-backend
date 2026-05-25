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

func (nopBroadcaster) Broadcast(uuid.UUID, string, any)         {}
func (nopBroadcaster) SendTo(uuid.UUID, uuid.UUID, string, any) {}

// NopBroadcaster возвращает заглушку.
func NopBroadcaster() Broadcaster { return nopBroadcaster{} }

// RoomsHandler инкапсулирует зависимости room-эндпоинтов.
type RoomsHandler struct {
	rooms     *repository.RoomsRepo
	courses   *repository.CoursesRepo
	questions *repository.QuestionsRepo
	answers   *repository.AnswersRepo
	users     *repository.UsersRepo
	gamif     *repository.GamificationRepo
	sm2       *repository.SM2Repo
	bcast     Broadcaster
}

func NewRoomsHandler(
	rooms *repository.RoomsRepo,
	courses *repository.CoursesRepo,
	questions *repository.QuestionsRepo,
	answers *repository.AnswersRepo,
	users *repository.UsersRepo,
	gamif *repository.GamificationRepo,
	sm2 *repository.SM2Repo,
	bcast Broadcaster,
) *RoomsHandler {
	if bcast == nil {
		bcast = NopBroadcaster()
	}
	return &RoomsHandler{
		rooms: rooms, courses: courses, questions: questions, answers: answers,
		users: users, gamif: gamif, sm2: sm2, bcast: bcast,
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
	// PreviousRoomID — id предыдущей комнаты, в которой эти же студенты
	// сидят. Если задан — на старую комнату уйдёт событие
	// room.next_session с кодом новой, чтобы старые участники
	// автоматически перешли без ручного ввода кода.
	PreviousRoomID string `json:"previous_room_id" binding:"omitempty,uuid"`
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
	// Если новая комната — продолжение сессии (relaunch), уведомляем
	// участников старой, чтобы они автоматом перешли без ручного ввода
	// кода.
	if req.PreviousRoomID != "" {
		if prevID, perr := uuid.Parse(req.PreviousRoomID); perr == nil {
			h.bcast.Broadcast(prevID, "room.next_session", gin.H{
				"room_id": room.ID.String(),
				"code":    room.Code,
				"title":   room.Title,
				"mode":    mode,
			})
		}
	}
	c.JSON(http.StatusCreated, toRoomResp(room))
}

// GetRoom — GET /api/v1/rooms/:id.
// Доступ имеют: преподаватель-владелец курса комнаты, участник комнаты,
// admin. Это закрывает horizontal-IDOR — нельзя по UUID комнаты узнать
// её settings/question_order постороннему.
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
	uid, _ := auth.UserIDFromContext(c)
	role, _ := auth.RoleFromContext(c)
	if role != "admin" {
		// Преподаватель-владелец курса?
		course, cerr := h.courses.GetByID(c.Request.Context(), room.CourseID)
		isOwner := cerr == nil && course.TeacherID == uid
		// Или участник комнаты?
		isParticipant := false
		if !isOwner {
			if _, perr := h.rooms.FindParticipant(c.Request.Context(), roomID, uid); perr == nil {
				isParticipant = true
			}
		}
		if !isOwner && !isParticipant {
			c.JSON(http.StatusForbidden, gin.H{"error": "no access to this room"})
			return
		}
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

// RestartRoom — POST /api/v1/rooms/:id/restart.
// Новый раунд внутри той же комнаты: студенты остаются на местах,
// сессия сбрасывается (answers очищаются, participant.idx=0,
// finished=NULL), опционально меняется банк/режим/перетасовка.
// Преподаватель потом дёргает /start, как обычно.
type restartRoomReq struct {
	BankID      string `json:"bank_id"      binding:"omitempty,uuid"`
	Mode        string `json:"mode"         binding:"omitempty,oneof=classic timer race"`
	Shuffle     *bool  `json:"shuffle"      binding:"omitempty"`
	AutoAdvance *bool  `json:"auto_advance" binding:"omitempty"`
}

func (h *RoomsHandler) RestartRoom(c *gin.Context) {
	roomID, ok := h.requireRoomOwnership(c)
	if !ok {
		return
	}
	var req restartRoomReq
	// Body опционален — пустой restart значит «прогнать тот же
	// банк ещё раз с прежним режимом».
	_ = c.ShouldBindJSON(&req)

	room, err := h.rooms.GetByID(c.Request.Context(), roomID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "room not found"})
		return
	}
	// Запрещаем restart только если активная сессия — пусть преподаватель
	// сначала закончит её через /finish.
	if room.Status == "active" || room.Status == "review" {
		c.JSON(http.StatusConflict, gin.H{"error": "session is active, finish it first"})
		return
	}

	// Решаем, какой банк используем (тот же или новый).
	bankID := room.BankID
	if req.BankID != "" {
		newBank, perr := uuid.Parse(req.BankID)
		if perr == nil {
			bankID = newBank
		}
	}

	// Тянем новые вопросы.
	qs, err := h.questions.ListByBank(c.Request.Context(), bankID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list questions"})
		return
	}
	if len(qs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bank is empty"})
		return
	}
	order := make([]uuid.UUID, 0, len(qs))
	for i := range qs {
		order = append(order, qs[i].ID)
	}

	// Решаем настройки.
	prev := map[string]any{}
	if len(room.Settings) > 0 {
		_ = json.Unmarshal(room.Settings, &prev)
	}
	mode := req.Mode
	if mode == "" {
		mode = readMode(room.Settings)
		if mode == "" {
			mode = "classic"
		}
	}
	shuffle := false
	if v, ok := prev["shuffle"].(bool); ok {
		shuffle = v
	}
	if req.Shuffle != nil {
		shuffle = *req.Shuffle
	}
	if shuffle {
		rand.Shuffle(len(order), func(i, j int) {
			order[i], order[j] = order[j], order[i]
		})
	}
	autoAdvance := false
	if v, ok := prev["auto_advance"].(bool); ok {
		autoAdvance = v
	}
	if req.AutoAdvance != nil {
		autoAdvance = *req.AutoAdvance
	}
	settingsJSON, _ := json.Marshal(map[string]any{
		"mode":         mode,
		"shuffle":      shuffle,
		"auto_advance": autoAdvance,
	})

	if err := h.rooms.RestartRoom(c.Request.Context(), roomID, bankID, order, settingsJSON); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "restart"})
		return
	}

	// Уведомляем участников. Они в _onEvent перезагрузят room+state.
	h.bcast.Broadcast(roomID, "room.restarted", gin.H{
		"bank_id":     bankID.String(),
		"mode":        mode,
		"total_count": len(order),
	})
	// И room.state_changed на всякий случай — у preподавателя обновится UI.
	h.bcast.Broadcast(roomID, "room.state_changed", gin.H{"status": "waiting"})

	// Возвращаем обновлённую комнату.
	fresh, _ := h.rooms.GetByID(c.Request.Context(), roomID)
	if fresh != nil {
		c.JSON(http.StatusOK, toRoomResp(fresh))
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "waiting"})
}

// FinishRoom — POST /api/v1/rooms/:id/finish.
// Принудительно переводит комнату в finished. Идемпотентен — повторный
// вызов на уже finished-комнате вернёт 200. Нужен преподавателю для
// досрочного завершения (например, в solo-режимах если кто-то завис).
func (h *RoomsHandler) FinishRoom(c *gin.Context) {
	roomID, ok := h.requireRoomOwnership(c)
	if !ok {
		return
	}
	room, err := h.rooms.GetByID(c.Request.Context(), roomID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "room not found"})
		return
	}
	if room.Status == "finished" {
		c.JSON(http.StatusOK, gin.H{"status": "finished"})
		return
	}
	if err := h.rooms.SetStatus(c.Request.Context(), room.ID, room.Status, "finished"); err != nil &&
		!errors.Is(err, repository.ErrInvalidStatus) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "set finished"})
		return
	}
	h.bcast.Broadcast(room.ID, "room.state_changed", gin.H{"status": "finished"})
	c.JSON(http.StatusOK, gin.H{"status": "finished"})
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
	// nickname+value нужны host-у, чтобы показать "кто как ответил" в реальном времени.
	// Студенты получают этот event тоже, но в их UI value/nickname игнорируются до review.
	var rawValue any
	_ = json.Unmarshal(req.Value, &rawValue)
	h.bcast.Broadcast(roomID, "question.answered", gin.H{
		"question_id":      qID.String(),
		"participant_id":   participant.ID.String(),
		"answers_received": count,
		"nickname":         participant.Nickname,
		"value":            rawValue,
	})

	// XP в журнал, прогресс, бейджи, SM-2 — побочные эффекты.
	newBadges, _, _, _ := h.awardForAnswer(c, awardCtx{
		UserID: uid, CourseID: room.CourseID, RoomID: roomID, QuestionID: qID,
		XP: xp, Correct: correct, HasCorrect: ok,
		ElapsedMs: elapsed, TimeLimitMs: q.TimeLimitSec * 1000,
		Difficulty: q.Difficulty,
		Finished:   false, // в classic нет понятия «закончил»
	})
	resp := gin.H{"correct": corrPtr, "awarded_xp": xp}
	if len(newBadges) > 0 {
		resp["new_badges"] = newBadges
	}
	c.JSON(http.StatusOK, resp)
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

	// В race-режиме неправильный ответ откатывает студента в начало
	// банка (Quizlet Live: ошибся — начинай заново). В timer-режиме
	// ответ всегда двигает вперёд.
	wrongInRace := mode == "race" && ok && !correct

	var advanced *repository.Participant
	if wrongInRace {
		// Чистим прошлые ответы этого студента в комнате — иначе при
		// повторном проходе вылетит UNIQUE-constraint. История попытки
		// теряется, но XP-журнал и user_progress сохраняются.
		_ = h.answers.DeleteByParticipant(c.Request.Context(), roomID, participant.ID)
		advanced, err = h.rooms.ResetParticipant(c.Request.Context(), participant.ID)
	} else {
		// Сохраняем ответ. Дубликат — игнорируем (FSM защита).
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
			if !errors.Is(err, repository.ErrDuplicateAnswer) {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "save answer"})
				return
			}
		}
		advanced, err = h.rooms.AdvanceParticipant(c.Request.Context(), participant.ID, total)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "advance"})
		return
	}

	// Broadcast progress всем (для race-bar в реальном времени).
	h.bcast.Broadcast(roomID, "participant.progress", gin.H{
		"participant_id":       advanced.ID.String(),
		"current_question_idx": advanced.CurrentQuestionIdx,
		"finished":             advanced.FinishedAtSession != nil,
		"reset":                wrongInRace,
		"reset_count":          advanced.ResetCount,
	})

	// Геймификация (XP-журнал, прогресс, бейджи, SM-2).
	finished := advanced.FinishedAtSession != nil
	newBadges, _, _, _ := h.awardForAnswer(c, awardCtx{
		UserID: uid, CourseID: room.CourseID, RoomID: roomID, QuestionID: currentQID,
		XP: xp, Correct: correct, HasCorrect: ok,
		ElapsedMs: req.ElapsedMs, TimeLimitMs: q.TimeLimitSec * 1000,
		Difficulty: q.Difficulty, Finished: finished,
	})

	resp := gin.H{
		"correct":              corrPtr,
		"awarded_xp":           xp,
		"current_question_idx": advanced.CurrentQuestionIdx,
		"total":                total,
		"finished":             advanced.FinishedAtSession != nil,
		"reset":                wrongInRace,
	}
	if len(newBadges) > 0 {
		resp["new_badges"] = newBadges
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
		ResetCount         int    `json:"reset_count"`
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
			ResetCount:         e.ResetCount,
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
	teacher.POST("/:id/finish", h.FinishRoom)
	teacher.POST("/:id/restart", h.RestartRoom)
	teacher.GET("/:id/participants/:pid/answers", h.ParticipantAnswers)
}

// ParticipantAnswers — GET /api/v1/rooms/:id/participants/:pid/answers.
// Возвращает историю ответов участника с подсветкой correct/wrong.
// Доступен только владельцу комнаты (teacher).
func (h *RoomsHandler) ParticipantAnswers(c *gin.Context) {
	roomID, ok := h.requireRoomOwnership(c)
	if !ok {
		return
	}
	pid, err := uuid.Parse(c.Param("pid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid participant id"})
		return
	}
	list, err := h.answers.ListByParticipant(c.Request.Context(), roomID, pid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list participant answers"})
		return
	}
	type item struct {
		QuestionID   string          `json:"question_id"`
		QuestionText string          `json:"question_text"`
		QuestionKind string          `json:"question_kind"`
		Options      json.RawMessage `json:"options,omitempty"`
		Correct      json.RawMessage `json:"correct,omitempty"`
		Value        json.RawMessage `json:"value"`
		IsCorrect    *bool           `json:"is_correct"`
		AwardedXP    int             `json:"awarded_xp"`
		ElapsedMs    int             `json:"elapsed_ms"`
	}
	out := make([]item, 0, len(list))
	for _, pa := range list {
		out = append(out, item{
			QuestionID:   pa.QuestionID.String(),
			QuestionText: pa.QuestionText,
			QuestionKind: pa.QuestionKind,
			Options:      pa.Options,
			Correct:      pa.Correct,
			Value:        pa.Value,
			IsCorrect:    pa.IsCorrect,
			AwardedXP:    pa.AwardedXP,
			ElapsedMs:    pa.ElapsedMs,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
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

// awardCtx — параметры начисления XP/бейджей/SM-2 за один ответ.
type awardCtx struct {
	UserID      uuid.UUID
	CourseID    uuid.UUID
	RoomID      uuid.UUID
	QuestionID  uuid.UUID
	XP          int
	Correct     bool
	HasCorrect  bool // true, если автопроверка возможна
	ElapsedMs   int
	TimeLimitMs int
	Difficulty  int
	// Finished=true когда это последний ответ участника в solo-режиме —
	// триггерит бейдж «идеальный раунд» при correct == total.
	Finished bool
}

// awardForAnswer — побочные эффекты ответа: XP в журнал, прогресс,
// бейджи, SM-2 state. Возвращает список *новых* кодов бейджей и
// сводку прогресса. Любые ошибки логируются, но не прерывают ответ —
// это «non-critical path».
func (h *RoomsHandler) awardForAnswer(c *gin.Context, a awardCtx) (newBadges []string, totalXP, level, streak int) {
	if h.gamif == nil {
		return nil, 0, 0, 0
	}
	ctx := c.Request.Context()

	// 1. XP в журнал + прогресс (atomic upsert + xp_log insert).
	// Считаем уровень в Go (LevelFromXP), передаём в SQL — там нет sqrt.
	prev, _ := h.gamif.ListProgress(ctx, a.UserID)
	prevTotal := 0
	for _, p := range prev {
		if p.CourseID == a.CourseID {
			prevTotal = p.TotalXP
			break
		}
	}
	newTotal := prevTotal + a.XP
	newLevel := services.LevelFromXP(newTotal)
	progress, err := h.gamif.AddXP(ctx, a.UserID, a.CourseID, a.XP, newLevel, nil, "answer")
	if err == nil {
		totalXP = progress.TotalXP
		level = progress.Level
		streak = progress.StreakDays
	}

	// 2. Бейджи. Все ошибки игнорируем — побочный эффект.
	tryAward := func(code string) {
		ok, _ := h.gamif.AwardBadge(ctx, a.UserID, code, a.CourseID)
		if ok {
			newBadges = append(newBadges, code)
		}
	}
	if a.HasCorrect && a.Correct {
		// «Первый правильный»: до этого ответа — 0 правильных в курсе.
		corrBefore, _ := h.gamif.CountCorrectInCourse(ctx, a.UserID, a.CourseID)
		// CountCorrectInCourse уже учёл текущий ответ (он в БД), поэтому 1.
		if corrBefore == 1 {
			tryAward("first_correct")
		}
		// «Молниеносный»: правильно и < 1/3 от лимита.
		if a.TimeLimitMs > 0 && a.ElapsedMs > 0 && a.ElapsedMs*3 < a.TimeLimitMs {
			tryAward("quick_thinker")
		}
	}
	if streak >= 3 {
		tryAward("streak_3")
	}
	if streak >= 7 {
		tryAward("streak_7")
	}
	if level >= 5 {
		tryAward("level_5")
	}
	if level >= 10 {
		tryAward("level_10")
	}
	if totalXP >= 1000 {
		tryAward("xp_1000")
	}
	// «Идеальный раунд»: дочитал до конца (Finished=true) и в комнате
	// все ответы правильные.
	if a.Finished {
		correct, total, _ := h.gamif.CountCorrectInRoom(ctx, a.UserID, a.RoomID)
		if total > 0 && correct == total {
			tryAward("perfect_round")
		}
	}

	// 3. SM-2: q-score выводим из (correct, скорость).
	if h.sm2 != nil && a.HasCorrect {
		q := sm2QScore(a.Correct, a.ElapsedMs, a.TimeLimitMs)
		st, err := h.sm2.Get(ctx, a.UserID, a.QuestionID)
		var src services.Sm2State
		if errors.Is(err, repository.ErrNotFound) {
			src = services.NewSm2State()
		} else if err == nil {
			src = services.Sm2State{
				EF: st.EF, IntervalDays: st.IntervalDays,
				Repetitions: st.Repetitions, DueDate: st.DueDate,
			}
			if st.LastReviewedAt != nil {
				src.LastReviewedAt = *st.LastReviewedAt
			}
		} else {
			return
		}
		updated := services.Sm2Update(src, q, time.Now())
		now := updated.LastReviewedAt
		_ = h.sm2.Upsert(ctx, repository.SM2State{
			UserID: a.UserID, QuestionID: a.QuestionID,
			EF: updated.EF, IntervalDays: updated.IntervalDays,
			Repetitions: updated.Repetitions, DueDate: updated.DueDate,
			LastReviewedAt: &now,
		})
	}
	return
}

// sm2QScore выводит q ∈ [0,5] из факта правильности и скорости ответа.
//
//	correct + ≤½T → 5; correct + ≤T → 4; correct + >T → 3;
//	incorrect → 2 (если был известен правильный); таймаут → 1.
func sm2QScore(correct bool, elapsedMs, timeLimitMs int) int {
	if !correct {
		if timeLimitMs > 0 && elapsedMs >= timeLimitMs {
			return 1
		}
		return 2
	}
	if timeLimitMs <= 0 {
		return 4
	}
	if elapsedMs*2 <= timeLimitMs {
		return 5
	}
	if elapsedMs <= timeLimitMs {
		return 4
	}
	return 3
}
