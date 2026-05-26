// Простой in-memory rate limiter с алгоритмом «token bucket» по IP.
//
// Используется на /auth/login и /auth/register, чтобы:
//   - не дать перебирать пароли (online brute-force);
//   - не позволить заDDoS-ить bcrypt одной горутиной (cost 11 ≈ 100 мс CPU).
//
// Без внешних зависимостей: для production-нагрузки этого достаточно
// в рамках одного инстанса. При горизонтальном масштабировании нужен
// общий бакет в Redis (см. backlog).
package server

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type bucket struct {
	tokens   float64
	last     time.Time
	maxBurst float64
	refill   float64 // токенов в секунду
}

type ipRateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	maxBurst int
	perMin   int
	// Время жизни записи без активности — ради чистки памяти.
	idleTTL time.Duration
}

func newIPRateLimiter(perMin, maxBurst int) *ipRateLimiter {
	rl := &ipRateLimiter{
		buckets:  make(map[string]*bucket),
		maxBurst: maxBurst,
		perMin:   perMin,
		idleTTL:  10 * time.Minute,
	}
	go rl.gcLoop()
	return rl
}

// allow решает, пропустить ли запрос с этого IP. Атомарно списывает
// токен, если он есть.
func (rl *ipRateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	b, ok := rl.buckets[ip]
	now := time.Now()
	if !ok {
		b = &bucket{
			tokens:   float64(rl.maxBurst),
			last:     now,
			maxBurst: float64(rl.maxBurst),
			refill:   float64(rl.perMin) / 60.0,
		}
		rl.buckets[ip] = b
	}
	// Доливаем токены за прошедшее время.
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * b.refill
	if b.tokens > b.maxBurst {
		b.tokens = b.maxBurst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// gcLoop периодически чистит молчаливые IP, чтобы карта не росла.
func (rl *ipRateLimiter) gcLoop() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for range t.C {
		rl.mu.Lock()
		now := time.Now()
		for ip, b := range rl.buckets {
			if now.Sub(b.last) > rl.idleTTL {
				delete(rl.buckets, ip)
			}
		}
		rl.mu.Unlock()
	}
}

// rateLimitMiddleware возвращает Gin middleware, который пропускает
// запрос только если у IP остались токены. На отказ — 429 с Retry-After.
func rateLimitMiddleware(perMin, maxBurst int) gin.HandlerFunc {
	rl := newIPRateLimiter(perMin, maxBurst)
	return func(c *gin.Context) {
		ip := clientIP(c)
		if !rl.allow(ip) {
			c.Header("Retry-After", "60")
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error": "слишком много попыток, попробуйте через минуту",
			})
			c.Abort()
			return
		}
		c.Next()
	}
}

// clientIP — извлекает IP клиента, учитывая X-Forwarded-For (Caddy)
// и X-Real-IP. В проде Caddy ставит правильный заголовок; в dev —
// идём по RemoteAddr.
func clientIP(c *gin.Context) string {
	if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
		// первый IP в списке — реальный клиент
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if rip := c.GetHeader("X-Real-IP"); rip != "" {
		return rip
	}
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		return c.Request.RemoteAddr
	}
	return host
}
