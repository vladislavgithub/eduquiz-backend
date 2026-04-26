// Выпуск и проверка JWT-токенов. Используется HS256 с симметричным
// секретом — для одного backend-процесса этого достаточно; при
// горизонтальном масштабировании секрет шарится между инстансами.
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// TokenPair — пара access + refresh, возвращаемая после login/register.
type TokenPair struct {
	Access  string
	Refresh string
}

// Claims — payload JWT. UserID — uuid пользователя в строковом
// виде; Role нужен для авторизации без обращения к БД на каждом
// запросе.
type Claims struct {
	UserID uuid.UUID `json:"sub_id"`
	Role   string    `json:"role"`
	Type   string    `json:"typ"` // "access" | "refresh"
	jwt.RegisteredClaims
}

// Issuer выпускает и валидирует токены.
type Issuer struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
}

func NewIssuer(secret string, accessTTL, refreshTTL time.Duration) *Issuer {
	return &Issuer{
		secret:     []byte(secret),
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
	}
}

// IssuePair выпускает пару access+refresh для указанного пользователя.
func (i *Issuer) IssuePair(userID uuid.UUID, role string) (TokenPair, error) {
	access, err := i.sign(userID, role, "access", i.accessTTL)
	if err != nil {
		return TokenPair{}, fmt.Errorf("issue access: %w", err)
	}
	refresh, err := i.sign(userID, role, "refresh", i.refreshTTL)
	if err != nil {
		return TokenPair{}, fmt.Errorf("issue refresh: %w", err)
	}
	return TokenPair{Access: access, Refresh: refresh}, nil
}

func (i *Issuer) sign(userID uuid.UUID, role, typ string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID: userID,
		Role:   role,
		Type:   typ,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "eduquiz-api",
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return t.SignedString(i.secret)
}

// Parse валидирует подпись и срок действия токена и возвращает claims.
// Тип токена (access/refresh) проверяется вызывающей стороной.
func (i *Issuer) Parse(token string) (*Claims, error) {
	c := &Claims{}
	_, err := jwt.ParseWithClaims(token, c, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return i.secret, nil
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, ErrTokenInvalid
	}
	return c, nil
}

// Ошибки для верификации токенов.
var (
	ErrTokenInvalid = errors.New("token invalid")
	ErrTokenExpired = errors.New("token expired")
)
