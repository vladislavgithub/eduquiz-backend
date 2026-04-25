// Реализация алгоритма интервального повторения SuperMemo SM-2
// (Wozniak, 1990). Используется для индивидуального повторения
// студентом материала после интерактивных сессий.
package services

import "time"

// Sm2State хранит параметры повторения карточки для пары (студент, вопрос).
type Sm2State struct {
	// EF (easiness factor) — насколько легко даётся карточка.
	// Изначально 2.5; минимум 1.3.
	EF float64
	// IntervalDays — текущий интервал повторения в днях.
	IntervalDays int
	// Repetitions — число последовательных успешных повторений (q >= 3).
	Repetitions int
	// DueDate — дата следующего планового повторения.
	DueDate time.Time
	// LastReviewedAt — момент последней оценки.
	LastReviewedAt time.Time
}

// Sm2Update применяет ответ с оценкой качества q ∈ [0,5] к состоянию s.
// Возвращает обновлённое состояние; не мутирует s.
//
// Семантика q:
//
//	5 — perfect response,
//	4 — correct response after a hesitation,
//	3 — correct response with serious difficulty,
//	2 — incorrect response; correct seemed easy to recall,
//	1 — incorrect response; correct remembered after seeing,
//	0 — complete blackout.
//
// Для q < 3 повторения сбрасываются: интервал = 1 день, repetitions = 0.
// EF обновляется по формуле SM-2.
func Sm2Update(s Sm2State, q int, now time.Time) Sm2State {
	if q < 0 {
		q = 0
	}
	if q > 5 {
		q = 5
	}

	out := s

	if q < 3 {
		out.Repetitions = 0
		out.IntervalDays = 1
	} else {
		switch s.Repetitions {
		case 0:
			out.IntervalDays = 1
		case 1:
			out.IntervalDays = 6
		default:
			out.IntervalDays = ceilInt(float64(s.IntervalDays) * s.EF)
		}
		out.Repetitions = s.Repetitions + 1
	}

	delta := 0.1 - float64(5-q)*(0.08+float64(5-q)*0.02)
	out.EF = s.EF + delta
	if out.EF < 1.3 {
		out.EF = 1.3
	}

	out.LastReviewedAt = now
	out.DueDate = now.Add(time.Duration(out.IntervalDays) * 24 * time.Hour)

	return out
}

// NewSm2State возвращает начальное состояние карточки.
func NewSm2State() Sm2State {
	return Sm2State{
		EF:           2.5,
		IntervalDays: 0,
		Repetitions:  0,
	}
}

func ceilInt(x float64) int {
	n := int(x)
	if float64(n) < x {
		return n + 1
	}
	return n
}
