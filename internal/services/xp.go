// Расчёт очков опыта (XP) за ответ на вопрос интерактивной сессии.
// Соответствует формуле, описанной в магистерской работе (раздел 2.4):
//
//	ΔXP = w_c · d · (1 - α · t/T)
//
// где w_c — множитель корректности, d — сложность вопроса, t — время
// ответа, T — лимит, α — коэффициент штрафа за медлительность.
package services

// XPParams — параметры формулы. Defaults подобраны под обычные
// сценарии: α=0.5, MinFactor=0.0 (медленный правильный ответ всё ещё
// что-то даёт, кроме совсем граничного случая t≈T).
type XPParams struct {
	Alpha     float64 // штраф за медлительность; 0.5 по умолчанию
	MinFactor float64 // минимум скоростного множителя; 0.0 по умолчанию
}

// DefaultXPParams возвращает параметры формулы по умолчанию.
func DefaultXPParams() XPParams {
	return XPParams{Alpha: 0.5, MinFactor: 0.0}
}

// XPAward вычисляет XP за один ответ.
//
//	correct      — правильный ли ответ;
//	difficulty   — сложность вопроса 1..5 (вне диапазона клампится);
//	elapsedMs    — время ответа в миллисекундах;
//	timeLimitMs  — лимит времени на вопрос в миллисекундах (0 = без лимита).
//
// За неправильный ответ возвращает 0. За правильный без лимита — d (без штрафа).
func XPAward(correct bool, difficulty int, elapsedMs, timeLimitMs int, p XPParams) int {
	if !correct {
		return 0
	}
	if difficulty < 1 {
		difficulty = 1
	}
	if difficulty > 5 {
		difficulty = 5
	}

	speed := 1.0
	if timeLimitMs > 0 {
		ratio := float64(elapsedMs) / float64(timeLimitMs)
		if ratio > 1 {
			ratio = 1
		}
		speed = 1.0 - p.Alpha*ratio
		if speed < p.MinFactor {
			speed = p.MinFactor
		}
	}

	xp := float64(difficulty) * speed * 10.0 // базовый множитель *10 — даёт читаемые цифры
	return int(xp + 0.5)                     // округление к ближайшему целому
}

// LevelFromXP возвращает уровень по накопленным XP по правилу
// L = floor(sqrt(XP / k)). При k=100 это даёт первый уровень
// уже через 100 XP, а удвоение уровня требует ×4 опыта.
func LevelFromXP(xp int) int {
	const k = 100
	if xp <= 0 {
		return 0
	}
	// Линейный поиск достаточно быстр — при XP=10^9 это ~3000 итераций.
	// Для типичных значений (XP < 10^5) — десятки итераций.
	l := 0
	for {
		next := (l + 1) * (l + 1) * k
		if next > xp {
			return l
		}
		l++
	}
}
