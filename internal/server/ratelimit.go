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
	"bytes"
	"encoding/json"
	"io"
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

// refillLocked возвращает (создавая при необходимости) бакет ключа и
// доливает токены за прошедшее время. Вызывать под удержанным rl.mu.
func (rl *ipRateLimiter) refillLocked(key string) *bucket {
	b, ok := rl.buckets[key]
	now := time.Now()
	if !ok {
		b = &bucket{
			tokens:   float64(rl.maxBurst),
			last:     now,
			maxBurst: float64(rl.maxBurst),
			refill:   float64(rl.perMin) / 60.0,
		}
		rl.buckets[key] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * b.refill
	if b.tokens > b.maxBurst {
		b.tokens = b.maxBurst
	}
	b.last = now
	return b
}

// allow решает, пропустить ли запрос с этого ключа. Атомарно списывает
// токен, если он есть.
func (rl *ipRateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	b := rl.refillLocked(key)
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// available сообщает, есть ли у ключа хотя бы один токен, НЕ списывая его.
func (rl *ipRateLimiter) available(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.refillLocked(key).tokens >= 1
}

// penalize списывает один токен с ключа (штраф за неудачную попытку).
func (rl *ipRateLimiter) penalize(key string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	b := rl.refillLocked(key)
	if b.tokens >= 1 {
		b.tokens--
	} else {
		b.tokens = 0
	}
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

// loginRateLimitMiddleware — лимитер именно для /auth/login.
// Ключевое отличие от обычного per-IP лимита: токен СПИСЫВАЕТСЯ ТОЛЬКО при
// неудачном входе (ответ 401). Успешный вход не тратит ничего — поэтому
// целый класс с одного IP, у которого верные пароли, логинится свободно,
// сколько бы студентов ни было. Брут одного аккаунта (поток 401) режется
// по паре (IP+email); агрегатный поток неудач с одного IP ловит мягкий
// потолок по IP (на случай перебора по множеству email с одного адреса).
func loginRateLimitMiddleware() gin.HandlerFunc {
	perAcct := newIPRateLimiter(10, 5) // на (IP|email): 5 burst, ~10/мин
	perIP := newIPRateLimiter(100, 50) // потолок по IP: 50 burst, ~100/мин
	return func(c *gin.Context) {
		ip := clientIP(c)
		acctKey := ip + "|" + peekLoginEmail(c)
		// Проверяем ДО bcrypt — чтобы заблокированный брут не жёг CPU.
		if !perAcct.available(acctKey) || !perIP.available(ip) {
			c.Header("Retry-After", "60")
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error": "слишком много неудачных попыток входа, попробуйте через минуту",
			})
			c.Abort()
			return
		}
		c.Next()
		// Штрафуем только за неудачный вход (неверные учётные данные).
		if c.Writer.Status() == http.StatusUnauthorized {
			perAcct.penalize(acctKey)
			perIP.penalize(ip)
		}
	}
}

// peekLoginEmail читает email из JSON-тела запроса, не «съедая» его:
// тело восстанавливается для последующего хендлера. При ошибке — "".
func peekLoginEmail(c *gin.Context) string {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return ""
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body)) // вернуть тело хендлеру
	var p struct {
		Email string `json:"email"`
	}
	_ = json.Unmarshal(body, &p)
	return strings.ToLower(strings.TrimSpace(p.Email))
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
