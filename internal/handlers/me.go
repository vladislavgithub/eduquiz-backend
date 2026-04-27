// Личные эндпоинты студента: прогресс, бейджи, повторение (SM-2).
//
//	GET  /api/v1/me/progress         — список прогресса по курсам;
//	GET  /api/v1/me/progress/:cid    — детально по курсу + xp по дням;
//	GET  /api/v1/me/badges           — все бейджи (заработанные + остальные);
//	GET  /api/v1/me/sm2/due          — карточки на сегодняшнее повторение;
//	POST /api/v1/me/sm2/answer       — ответил на карточку, обновить SM-2.
package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/vladislavgithub/eduquiz-backend/internal/auth"
	"github.com/vladislavgithub/eduquiz-backend/internal/repository"
	"github.com/vladislavgithub/eduquiz-backend/internal/services"
)

type MeHandler struct {
	gamif     *repository.GamificationRepo
	sm2       *repository.SM2Repo
	courses   *repository.CoursesRepo
	questions *repository.QuestionsRepo
}

func NewMeHandler(
	gamif *repository.GamificationRepo,
	sm2 *repository.SM2Repo,
	courses *repository.CoursesRepo,
	questions *repository.QuestionsRepo,
) *MeHandler {
	return &MeHandler{gamif: gamif, sm2: sm2, courses: courses, questions: questions}
}

func (h *MeHandler) Routes(api *gin.RouterGroup, issuer *auth.Issuer) {
	authed := api.Group("/me", auth.RequireAuth(issuer))
	authed.GET("/progress", h.ListProgress)
	authed.GET("/progress/:cid", h.CourseProgress)
	authed.GET("/badges", h.ListBadges)
	authed.GET("/sm2/due", h.ListDue)
	authed.POST("/sm2/answer", h.AnswerSM2)
}

// --- DTO ---

type progressItem struct {
	CourseID    string `json:"course_id"`
	CourseTitle string `json:"course_title"`
	TotalXP     int    `json:"total_xp"`
	Level       int    `json:"level"`
	StreakDays  int    `json:"streak_days"`
	LastActive  string `json:"last_active,omitempty"`
}

type badgeItem struct {
	Code        string `json:"code"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
	Earned      bool   `json:"earned"`
	AwardedAt   string `json:"awarded_at,omitempty"`
	CourseID    string `json:"course_id,omitempty"`
}

type dueCardResp struct {
	QuestionID   string          `json:"question_id"`
	BankID       string          `json:"bank_id"`
	BankTitle    string          `json:"bank_title"`
	CourseID     string          `json:"course_id"`
	Kind         string          `json:"kind"`
	Text         string          `json:"text"`
	Options      json.RawMessage `json:"options"`
	TimeLimitSec int             `json:"time_limit_sec"`
	Difficulty   int             `json:"difficulty"`
	DueDate      string          `json:"due_date"`
}

type sm2AnswerReq struct {
	QuestionID string          `json:"question_id" binding:"required,uuid"`
	Value      json.RawMessage `json:"value"       binding:"required"`
	ElapsedMs  int             `json:"elapsed_ms"  binding:"omitempty,min=0"`
}

type sm2AnswerResp struct {
	Correct      *bool  `json:"correct,omitempty"`
	Q            int    `json:"q"`
	IntervalDays int    `json:"interval_days"`
	NextDue      string `json:"next_due"`
}

// --- Endpoints ---

func (h *MeHandler) ListProgress(c *gin.Context) {
	uid, _ := auth.UserIDFromContext(c)
	list, err := h.gamif.ListProgress(c.Request.Context(), uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list progress"})
		return
	}
	out := make([]progressItem, 0, len(list))
	for _, p := range list {
		title := ""
		if cr, err := h.courses.GetByID(c.Request.Context(), p.CourseID); err == nil {
			title = cr.Title
		}
		item := progressItem{
			CourseID: p.CourseID.String(), CourseTitle: title,
			TotalXP: p.TotalXP, Level: p.Level, StreakDays: p.StreakDays,
		}
		if p.LastActive != nil {
			item.LastActive = p.LastActive.Format("2006-01-02")
		}
		out = append(out, item)
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func (h *MeHandler) CourseProgress(c *gin.Context) {
	uid, _ := auth.UserIDFromContext(c)
	cid, err := uuid.Parse(c.Param("cid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid course id"})
		return
	}
	list, _ := h.gamif.ListProgress(c.Request.Context(), uid)
	var found *repository.UserProgress
	for i := range list {
		if list[i].CourseID == cid {
			found = &list[i]
			break
		}
	}
	if found == nil {
		c.JSON(http.StatusOK, gin.H{
			"course_id":   cid.String(),
			"total_xp":    0,
			"level":       0,
			"streak_days": 0,
			"xp_by_day":   map[string]int{},
			"xp_to_next":  100, // следующий уровень требует ≥100 XP
		})
		return
	}
	hist, _ := h.gamif.XPHistogramByDay(c.Request.Context(), uid, cid, 14)

	// XP до следующего уровня: (lvl+1)^2 * 100 - total_xp
	nextLevelXP := (found.Level + 1) * (found.Level + 1) * 100
	xpToNext := nextLevelXP - found.TotalXP
	if xpToNext < 0 {
		xpToNext = 0
	}

	c.JSON(http.StatusOK, gin.H{
		"course_id":     cid.String(),
		"total_xp":      found.TotalXP,
		"level":         found.Level,
		"streak_days":   found.StreakDays,
		"xp_by_day":     hist,
		"xp_to_next":    xpToNext,
		"next_level_xp": nextLevelXP,
	})
}

func (h *MeHandler) ListBadges(c *gin.Context) {
	uid, _ := auth.UserIDFromContext(c)
	all, err := h.gamif.ListAllBadges(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list badges"})
		return
	}
	earned, _ := h.gamif.ListUserBadges(c.Request.Context(), uid)
	earnedByCode := map[string]repository.UserBadge{}
	for _, e := range earned {
		// Берём самый ранний из дублей по разным курсам.
		if _, ok := earnedByCode[e.Code]; !ok {
			earnedByCode[e.Code] = e
		}
	}
	out := make([]badgeItem, 0, len(all))
	for _, b := range all {
		item := badgeItem{
			Code: b.Code, Title: b.Title, Description: b.Description,
			Icon: b.Icon, Earned: false,
		}
		if e, ok := earnedByCode[b.Code]; ok {
			item.Earned = true
			item.AwardedAt = e.AwardedAt.Format(time.RFC3339)
			item.CourseID = e.CourseID.String()
		}
		out = append(out, item)
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func (h *MeHandler) ListDue(c *gin.Context) {
	uid, _ := auth.UserIDFromContext(c)
	cards, err := h.sm2.ListDue(c.Request.Context(), uid, 30)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list due"})
		return
	}
	out := make([]dueCardResp, 0, len(cards))
	for _, q := range cards {
		out = append(out, dueCardResp{
			QuestionID:   q.QuestionID.String(),
			BankID:       q.BankID.String(),
			BankTitle:    q.BankTitle,
			CourseID:     q.CourseID.String(),
			Kind:         q.Kind,
			Text:         q.Text,
			Options:      q.Options,
			TimeLimitSec: q.TimeLimitSec,
			Difficulty:   q.Difficulty,
			DueDate:      q.DueDate.Format("2006-01-02"),
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "count": len(out)})
}

func (h *MeHandler) AnswerSM2(c *gin.Context) {
	uid, _ := auth.UserIDFromContext(c)
	var req sm2AnswerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	qID, _ := uuid.Parse(req.QuestionID)
	q, err := h.questions.GetByID(c.Request.Context(), qID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "question not found"})
		return
	}
	// Доступ к SM-2 только для тех вопросов, к которым студент уже
	// прикасался (есть запись в sm2_states от прошлой сессии).
	// Защищает от того, что любой залогиненный пользователь по UUID
	// чужого вопроса заведёт себе sm2_state и таскает его в /me/sm2/due.
	if _, sErr := h.sm2.Get(c.Request.Context(), uid, qID); sErr != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "no access to this question"})
		return
	}
	correct, ok := services.CheckAnswer(q.Kind, q.Correct, req.Value)
	var corrPtr *bool
	if ok {
		c2 := correct
		corrPtr = &c2
	}

	// q-score из факта правильности и скорости.
	qScore := sm2QScore(correct, req.ElapsedMs, q.TimeLimitSec*1000)

	st, err := h.sm2.Get(c.Request.Context(), uid, qID)
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load sm2"})
		return
	}
	upd := services.Sm2Update(src, qScore, time.Now())
	now := upd.LastReviewedAt
	if err := h.sm2.Upsert(c.Request.Context(), repository.SM2State{
		UserID: uid, QuestionID: qID,
		EF: upd.EF, IntervalDays: upd.IntervalDays,
		Repetitions: upd.Repetitions, DueDate: upd.DueDate,
		LastReviewedAt: &now,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save sm2"})
		return
	}

	c.JSON(http.StatusOK, sm2AnswerResp{
		Correct:      corrPtr,
		Q:            qScore,
		IntervalDays: upd.IntervalDays,
		NextDue:      upd.DueDate.Format("2006-01-02"),
	})
}
