// Чёрный список JWT-токенов по jti. Используется для logout / refresh-rotation:
// когда юзер обновляет токен, старый jti кидаем сюда — даже если злоумышленник
// его украл и попробует использовать, middleware отвергнет.
//
// Реализация — in-memory map. Подходит для одного инстанса бэка (наш кейс).
// Для горизонтального масштабирования заменить на Redis.
package auth

import (
	"sync"
	"time"
)

// Revocation — потокобезопасный TTL-blacklist по jti.
type Revocation struct {
	mu      sync.RWMutex
	entries map[string]time.Time // jti → expires_at
	// Чистка раз в minute, удаляет уже истёкшие записи (после exp токена
	// они и так не пройдут sig-check, нет смысла держать память).
	cleanupTicker *time.Ticker
	stop          chan struct{}
}

// NewRevocation создаёт revocation-store и запускает фоновую очистку.
func NewRevocation() *Revocation {
	r := &Revocation{
		entries:       make(map[string]time.Time),
		cleanupTicker: time.NewTicker(time.Minute),
		stop:          make(chan struct{}),
	}
	go r.cleanupLoop()
	return r
}

// Close останавливает фоновую очистку. Вызывать при shutdown.
func (r *Revocation) Close() {
	close(r.stop)
	r.cleanupTicker.Stop()
}

// Revoke помечает jti отозванным до момента exp.
func (r *Revocation) Revoke(jti string, exp time.Time) {
	if jti == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[jti] = exp
}

// IsRevoked возвращает true если jti в blacklist'е и ещё не истёк.
func (r *Revocation) IsRevoked(jti string) bool {
	if jti == "" {
		return false
	}
	r.mu.RLock()
	exp, ok := r.entries[jti]
	r.mu.RUnlock()
	if !ok {
		return false
	}
	return time.Now().Before(exp)
}

func (r *Revocation) cleanupLoop() {
	for {
		select {
		case <-r.stop:
			return
		case <-r.cleanupTicker.C:
			now := time.Now()
			r.mu.Lock()
			for jti, exp := range r.entries {
				if now.After(exp) {
					delete(r.entries, jti)
				}
			}
			r.mu.Unlock()
		}
	}
}
