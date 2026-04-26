// Хэширование и проверка паролей через bcrypt.
// bcrypt подобран осознанно: встроенная соль, тюнинг по cost,
// устойчивость к brute-force на GPU.
package auth

import "golang.org/x/crypto/bcrypt"

// DefaultCost — компромисс между скоростью входа и стойкостью.
// Поднимется со временем по мере роста производительности железа.
const DefaultCost = 12

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
