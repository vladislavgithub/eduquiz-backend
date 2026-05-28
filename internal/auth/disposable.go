// Блоклист одноразовых (disposable / throwaway) почтовых доменов.
// Список встраивается в бинарь через go:embed, чтобы не зависеть от
// файловой системы в рантайме. Используется на публичной регистрации
// как анти-бот мера: регистрация с таких доменов запрещена.
package auth

import (
	_ "embed"
	"strings"
)

//go:embed disposable_domains.txt
var disposableDomainsRaw string

// disposableDomains — множество запрещённых доменов (нижний регистр).
var disposableDomains = buildDisposableSet(disposableDomainsRaw)

// buildDisposableSet парсит сырой список: по одному домену на строку,
// в нижний регистр, без пустых строк и комментариев (# или //).
func buildDisposableSet(raw string) map[string]struct{} {
	set := make(map[string]struct{}, 8192)
	for _, line := range strings.Split(raw, "\n") {
		d := strings.ToLower(strings.TrimSpace(line))
		if d == "" || strings.HasPrefix(d, "#") || strings.HasPrefix(d, "//") {
			continue
		}
		set[d] = struct{}{}
	}
	return set
}

// IsDisposableEmailDomain сообщает, относится ли домен email к одноразовым.
// Email приводится к нижнему регистру; берётся часть после последнего '@'.
// Если '@' нет — возвращает false (валидацию формата делает binding-тег).
func IsDisposableEmailDomain(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return false
	}
	domain := email[at+1:]
	_, ok := disposableDomains[domain]
	return ok
}
