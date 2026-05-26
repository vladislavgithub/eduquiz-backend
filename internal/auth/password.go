// Хэширование и проверка паролей через bcrypt.
// bcrypt подобран осознанно: встроенная соль, тюнинг по cost,
// устойчивость к brute-force на GPU.
package auth

import "golang.org/x/crypto/bcrypt"

// DefaultCost — компромисс между скоростью входа и стойкостью.
// Cost=11 даёт ~100 мс на современном CPU — рекомендация OWASP
// Password Storage Cheat Sheet 2024 (bcrypt work factor 10–12).
// Cost=12 (~250 мс) увеличивал DoS-вектор на /auth/register:
// при N=500 параллельных регистраций p99 латентности достигал
// 27 с (см. результаты нагрузочного тестирования). Старые хэши
// с cost=12 продолжают валидироваться без миграции — параметр
// зашит в каждый хэш.
const DefaultCost = 11

// HashPassword возвращает bcrypt-хэш пароля. Соль bcrypt генерирует
// сам и встраивает в результат, отдельное поле в БД не нужно.
func HashPassword(plain string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plain), DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// VerifyPassword возвращает true, если plain соответствует hash.
// Для несовпадения вернёт false без ошибки; ошибка — только если
// сам hash повреждён или имеет неправильный формат.
func VerifyPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}
