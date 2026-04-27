// Психометрический анализ банка вопросов: альфа Кронбаха,
// p-value (трудность пункта) и point-biserial r_pb (дискриминация).
//
// Используется в магистерской работе как «модель оценки надёжности
// усвоения знаний» — внутренняя согласованность теста и качество
// каждого пункта (раздел 2.4 «Модель надёжности усвоения знаний»).
package services

import "math"

// ItemStats — психометрика одного пункта (вопроса).
type ItemStats struct {
	// PValue — доля респондентов, ответивших правильно. ∈ [0,1].
	// Интерпретация:
	//   < 0.30 — слишком сложный, скорее всего сформулирован неясно;
	//   0.30..0.85 — рабочий диапазон;
	//   > 0.85 — слишком лёгкий, не различает студентов.
	PValue float64
	// Discrimination — точечно-бисериальная корреляция r_pb между
	// бинарным результатом по пункту и суммой по тесту.
	// Положительная — пункт «работает» (сильные студенты отвечают
	// правильнее слабых). Отрицательная или ~0 — кандидат на удаление.
	Discrimination float64
	// Variance — дисперсия бинарных оценок: p(1-p).
	Variance float64
	// AnsweredBy — сколько респондентов отвечало на этот пункт.
	AnsweredBy int
}

// BankAnalytics — сводный отчёт по банку.
type BankAnalytics struct {
	Respondents   int         // число уникальных участников
	ItemCount     int         // число пунктов в анализе
	MeanScore     float64     // среднее число правильных по тесту
	MaxPossible   int         // максимально возможный балл (= ItemCount)
	CronbachAlpha float64     // α Кронбаха (KR-20 как частный случай)
	Items         []ItemStats // в том же порядке, что входная матрица
}

// AnalyzeBank вычисляет α + статистики каждого пункта по матрице
// ответов responses[participant][item] ∈ {0,1}. Все пользователи
// должны ответить на одинаковый набор пунктов; если кто-то ответил
// не на все — caller обязан исключить его строку или подставить 0.
//
// Если participants < 2 или items < 2 — возвращает нулевой отчёт
// (формула α требует минимум 2 наблюдения по 2 пунктам).
func AnalyzeBank(responses [][]int) BankAnalytics {
	n := len(responses)
	if n < 2 {
		return BankAnalytics{Respondents: n}
	}
	k := len(responses[0])
	if k < 2 {
		return BankAnalytics{Respondents: n, ItemCount: k}
	}

	totals := make([]float64, n) // суммы по респондентам
	itemSums := make([]float64, k)
	for i := 0; i < n; i++ {
		if len(responses[i]) != k {
			// Некорректные данные — пропускаем безопасно.
			return BankAnalytics{Respondents: n, ItemCount: k}
		}
		for j := 0; j < k; j++ {
			x := float64(responses[i][j])
			totals[i] += x
			itemSums[j] += x
		}
	}

	// Среднее и дисперсия суммарного балла.
	totalMean := mean(totals)
	totalVar := variancePopulation(totals, totalMean)

	// Дисперсия каждого пункта (population variance).
	itemVars := make([]float64, k)
	for j := 0; j < k; j++ {
		p := itemSums[j] / float64(n)
		// Для бинарных оценок dispersion = p(1-p).
		itemVars[j] = p * (1 - p)
	}

	// α Кронбаха: (k / (k-1)) · (1 - Σ σ²ᵢ / σ²ₜ).
	sumItemVar := 0.0
	for _, v := range itemVars {
		sumItemVar += v
	}
	var alpha float64
	if totalVar > 0 {
		alpha = float64(k) / float64(k-1) * (1 - sumItemVar/totalVar)
	}
	if math.IsNaN(alpha) || math.IsInf(alpha, 0) {
		alpha = 0
	}

	// Per-item: p-value и r_pb.
	totalStd := math.Sqrt(totalVar)
	items := make([]ItemStats, k)
	for j := 0; j < k; j++ {
		p := itemSums[j] / float64(n)
		// Среднее totals по тем, кто ответил правильно/неправильно.
		var sumRight, sumWrong float64
		var nRight, nWrong int
		for i := 0; i < n; i++ {
			if responses[i][j] == 1 {
				sumRight += totals[i]
				nRight++
			} else {
				sumWrong += totals[i]
				nWrong++
			}
		}
		var rpb float64
		if nRight > 0 && nWrong > 0 && totalStd > 0 {
			mRight := sumRight / float64(nRight)
			mWrong := sumWrong / float64(nWrong)
			rpb = (mRight - mWrong) / totalStd * math.Sqrt(p*(1-p))
		}
		items[j] = ItemStats{
			PValue:         round3(p),
			Discrimination: round3(rpb),
			Variance:       round3(p * (1 - p)),
			AnsweredBy:     n,
		}
	}

	return BankAnalytics{
		Respondents:   n,
		ItemCount:     k,
		MeanScore:     round3(totalMean),
		MaxPossible:   k,
		CronbachAlpha: round3(alpha),
		Items:         items,
	}
}

// AlphaInterpretation — словесная интерпретация значения α по
// классификации Cohen / George & Mallery.
//
//	≥ 0.9   — отличная согласованность (но возможна избыточность);
//	0.8–0.9 — хорошая;
//	0.7–0.8 — приемлемая;
//	0.6–0.7 — сомнительная;
//	< 0.6   — низкая, тест требует доработки.
func AlphaInterpretation(alpha float64) string {
	switch {
	case alpha >= 0.9:
		return "отличная"
	case alpha >= 0.8:
		return "хорошая"
	case alpha >= 0.7:
		return "приемлемая"
	case alpha >= 0.6:
		return "сомнительная"
	default:
		return "низкая"
	}
}

// --- helpers ---

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

// variancePopulation — дисперсия с делением на N (а не на N-1).
// В формуле Кронбаха обычно используют population variance.
func variancePopulation(xs []float64, m float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range xs {
		d := x - m
		s += d * d
	}
	return s / float64(len(xs))
}

func round3(x float64) float64 {
	return math.Round(x*1000) / 1000
}
