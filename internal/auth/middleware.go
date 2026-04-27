// Gin-middleware, проверяющий JWT-токен в заголовке Authorization
// и кладущий claims в контекст для последующих хендлеров.
package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Ключ под которым в gin.Context кладутся распарсенные claims.
const ctxClaimsKey = "auth_claims"

// RequireAuth — middleware: 401, если нет валидного access-токена.
// При успехе claims доступны через FromContext(c).
func RequireAuth(issuer *Issuer) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := extractBearer(c.GetHeader("Authorization"))
		if raw == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
			return
		}
		claims, err := issuer.Parse(raw)
		if err != nil {
			status := http.StatusUnauthorized
			c.AbortWithStatusJSON(status, gin.H{"error": err.Error()})
			return
		}
		if claims.Type != "access" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "wrong token type"})
			return
		}
		c.Set(ctxClaimsKey, claims)
		c.Next()
	}
}

// RequireRole — middleware: 403, если роль пользователя не в whitelist.
// Должен идти ПОСЛЕ RequireAuth.
func RequireRole(roles ...string) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(roles))
	for _, r := range roles {
		allowed[r] = struct{}{}
	}
	return func(c *gin.Context) {
		claims, ok := FromContext(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "no claims"})
			return
		}
		if _, ok := allowed[claims.Role]; !ok {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		c.Next()
	}
}

// FromContext возвращает claims, ранее уложенные RequireAuth.
// Второй результат — false, если middleware не отрабатывал.
func FromContext(c *gin.Context) (*Claims, bool) {
	v, ok := c.Get(ctxClaimsKey)
	if !ok {
		return nil, false
	}
	claims, ok := v.(*Claims)
	return claims, ok
}

// UserIDFromContext возвращает uuid авторизованного пользователя.
func UserIDFromContext(c *gin.Context) (uuid.UUID, bool) {
	claims, ok := FromContext(c)
	if !ok {
		return uuid.Nil, false
	}
	return claims.UserID, true
}

// RoleFromContext возвращает роль ('teacher'/'student'/'admin')
// авторизованного пользователя.
func RoleFromContext(c *gin.Context) (string, bool) {
	claims, ok := FromContext(c)
	if !ok {
		return "", false
	}
	return claims.Role, true
}

func extractBearer(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}
