// Проверка силы пароля. Применяется при регистрации и смене пароля.
//
// Правила (мягкие, чтобы не отпугнуть студентов):
//   - длина не меньше 8 символов;
//   - есть хотя бы одна буква;
//   - есть хотя бы одна цифра;
//   - не входит в небольшой блэклист самых частых ("123456", "password" и т.п.).
//
// bcrypt cost 12 (см. password.go) добавляет уровня вычислительной защиты;
// этих правил достаточно, чтобы отсеять очевидно слабые пароли.
package auth

import (
	"fmt"
	"strings"
	"unicode"
)

// commonWeakPasswords — короткий блэклист, чтобы пользователь не оказался
// в любой утечке rockyou-style. Сравнение case-insensitive.
var commonWeakPasswords = map[string]struct{}{
	"password": {}, "qwerty": {}, "qwerty123": {}, "12345678": {},
	"123456789": {}, "1234567890": {}, "111111111": {}, "abcdefg": {},
	"abc12345": {}, "letmein": {}, "iloveyou": {}, "admin": {},
	"welcome": {}, "monkey": {}, "dragon": {}, "master": {},
	"sunshine": {}, "ashley": {}, "bailey": {}, "passw0rd": {},
}

// ValidatePasswordStrength возвращает nil, если пароль удовлетворяет
// правилам, иначе — описательную ошибку для клиента.
func ValidatePasswordStrength(p string) error {
	if len(p) < 8 {
		return fmt.Errorf("пароль должен быть не короче 8 символов")
	}
	if len(p) > 72 {
		// bcrypt не различает символы после 72 — отрезаем явно.
		return fmt.Errorf("пароль слишком длинный (макс 72 символа)")
	}
	var hasLetter, hasDigit bool
	for _, r := range p {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
		if hasLetter && hasDigit {
			break
		}
	}
	if !hasLetter {
		return fmt.Errorf("пароль должен содержать хотя бы одну букву")
	}
	if !hasDigit {
		return fmt.Errorf("пароль должен содержать хотя бы одну цифру")
	}
	if _, weak := commonWeakPasswords[strings.ToLower(p)]; weak {
		return fmt.Errorf("пароль слишком распространённый, выбери другой")
	}
	return nil
}
