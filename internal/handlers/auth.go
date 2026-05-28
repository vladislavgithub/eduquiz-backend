// HTTP-хендлеры аутентификации: регистрация, логин, /me.
// Все ответы — JSON; ошибки приходят в формате {"error": "..."}.
package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/vladislavgithub/eduquiz-backend/internal/auth"
	"github.com/vladislavgithub/eduquiz-backend/internal/repository"
)

// AuthHandler инкапсулирует зависимости auth-эндпоинтов.
type AuthHandler struct {
	users  *repository.UsersRepo
	issuer *auth.Issuer
}

func NewAuthHandler(users *repository.UsersRepo, issuer *auth.Issuer) *AuthHandler {
	return &AuthHandler{users: users, issuer: issuer}
}

// Register обрабатывает POST /api/v1/auth/register.
// Создаёт пользователя и сразу выпускает пару токенов.
type registerReq struct {
	Email    string `json:"email"    binding:"required,email"`
	Password string `json:"password" binding:"required,min=8,max=72"`
	FullName string `json:"full_name" binding:"required,min=2,max=200"`
	// Публичная регистрация всегда создаёт student — преподаватели заводятся
	// через админ-панель. Поле role принимается для совместимости со старыми
	// клиентами, но игнорируется. omitempty — чтобы клиенты без role не падали.
	Role string `json:"role" binding:"omitempty,oneof=teacher student"`
}

type tokenResp struct {
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	User         userResp `json:"user"`
}

type userResp struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	FullName string `json:"full_name"`
	Role     string `json:"role"`
}

func (h *AuthHandler) Register(c *gin.Context) {
	var req registerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Дополнительная проверка силы пароля поверх min/max из binding-тегов:
	// требует букву + цифру, отсеивает блэклист (qwerty/password/...).
	if err := auth.ValidatePasswordStrength(req.Password); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Анти-бот: запрещаем регистрацию с одноразовых почтовых доменов.
	// Проверяем ту же нормализованную строку, что и сохраняем в БД.
	normalizedEmail := strings.ToLower(strings.TrimSpace(req.Email))
	if auth.IsDisposableEmailDomain(normalizedEmail) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "регистрация с одноразовых почтовых адресов запрещена"})
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "hash password"})
		return
	}

	u := &repository.User{
		Email:        normalizedEmail,
		PasswordHash: hash,
		FullName:     strings.TrimSpace(req.FullName),
		// Игнорируем любую переданную клиентом роль: публичная регистрация
		// всегда создаёт student (защита от privilege escalation).
		Role: "student",
	}
	if err := h.users.Insert(c.Request.Context(), u); err != nil {
		if errors.Is(err, repository.ErrEmailTaken) {
			c.JSON(http.StatusConflict, gin.H{"error": "email already registered"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create user"})
		return
	}

	pair, err := h.issuer.IssuePair(u.ID, u.Role)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "issue tokens"})
		return
	}

	c.JSON(http.StatusCreated, tokenResp{
		AccessToken:  pair.Access,
		RefreshToken: pair.Refresh,
		User: userResp{
			ID: u.ID.String(), Email: u.Email, FullName: u.FullName, Role: u.Role,
		},
	})
}

// Login обрабатывает POST /api/v1/auth/login.
type loginReq struct {
	Email    string `json:"email"    binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

func (h *AuthHandler) Login(c *gin.Context) {
	var req loginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	u, err := h.users.GetByEmail(c.Request.Context(), req.Email)
	if err != nil {
		// Унифицированный 401: не различаем «нет пользователя»
		// и «неверный пароль» — иначе утечка информации о существовании email.
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}
	if !auth.VerifyPassword(u.PasswordHash, req.Password) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	pair, err := h.issuer.IssuePair(u.ID, u.Role)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "issue tokens"})
		return
	}
	c.JSON(http.StatusOK, tokenResp{
		AccessToken:  pair.Access,
		RefreshToken: pair.Refresh,
		User: userResp{
			ID: u.ID.String(), Email: u.Email, FullName: u.FullName, Role: u.Role,
		},
	})
}

// Refresh обрабатывает POST /api/v1/auth/refresh.
// Принимает refresh-токен, возвращает новую пару access+refresh.
// Это «rotating refresh» — старый refresh не отзывается специально,
// но клиент ОБЯЗАН перезаписать на новый: при истечении первого
// остаётся только последний.
type refreshReq struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

func (h *AuthHandler) Refresh(c *gin.Context) {
	var req refreshReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	claims, err := h.issuer.Parse(req.RefreshToken)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid refresh token"})
		return
	}
	if claims.Type != "refresh" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "wrong token type"})
		return
	}
	// Подгружаем профиль — на случай если роль/имя изменились с момента
	// выдачи токена (теоретически админ мог их править).
	u, err := h.users.GetByID(c.Request.Context(), claims.UserID)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not found"})
		return
	}
	pair, err := h.issuer.IssuePair(u.ID, u.Role)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "issue tokens"})
		return
	}
	c.JSON(http.StatusOK, tokenResp{
		AccessToken:  pair.Access,
		RefreshToken: pair.Refresh,
		User: userResp{
			ID: u.ID.String(), Email: u.Email, FullName: u.FullName, Role: u.Role,
		},
	})
}

// Me обрабатывает GET /api/v1/auth/me — возвращает профиль владельца токена.
func (h *AuthHandler) Me(c *gin.Context) {
	uid, ok := auth.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "no auth"})
		return
	}
	u, err := h.users.GetByID(c.Request.Context(), uid)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	c.JSON(http.StatusOK, userResp{
		ID: u.ID.String(), Email: u.Email, FullName: u.FullName, Role: u.Role,
	})
}

// Routes регистрирует auth-эндпоинты под уже существующей группой /api/v1.
// registerLimiter ставится на /register и /refresh (обычный per-IP лимит).
// loginLimiter ставится на /login — он штрафует только за НЕУДАЧНЫЙ вход,
// чтобы класс с одного IP с верными паролями не упирался в 429.
// Любой из лимитеров может быть nil (dev/test) — тогда не применяется.
func (h *AuthHandler) Routes(api *gin.RouterGroup, issuer *auth.Issuer, registerLimiter, loginLimiter gin.HandlerFunc) {
	a := api.Group("/auth")

	reg := a.Group("")
	if registerLimiter != nil {
		reg.Use(registerLimiter)
	}
	reg.POST("/register", h.Register)
	reg.POST("/refresh", h.Refresh)

	lg := a.Group("")
	if loginLimiter != nil {
		lg.Use(loginLimiter)
	}
	lg.POST("/login", h.Login)

	a.GET("/me", auth.RequireAuth(issuer), h.Me)
}
