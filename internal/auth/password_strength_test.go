package auth

import (
	"strings"
	"testing"
)

func TestValidatePasswordStrength(t *testing.T) {
	cases := []struct {
		name    string
		pwd     string
		wantErr bool
	}{
		{"короткий", "qwer123", true},
		{"только буквы", "password123abc", false},
		{"только цифры", "12345678", true},
		{"только буквы без цифр", "abcdefghij", true},
		{"норм 8 символов", "abcd1234", false},
		{"норм с пробелами", "my pass 99", false},
		{"русский", "пароль42", false},
		{"в блэклисте", "qwerty123", true},
		{"в блэклисте регистр", "PASSWORD", true},
		{"длинный — > 72", "ab1" + strings.Repeat("x", 70), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidatePasswordStrength(c.pwd)
			if (err != nil) != c.wantErr {
				t.Errorf("got err=%v, wantErr=%v (pwd=%q)", err, c.wantErr, c.pwd)
			}
		})
	}
}
