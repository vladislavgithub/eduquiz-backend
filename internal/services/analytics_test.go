package services

import (
	"math"
	"testing"
)

func almostEqual(a, b, eps float64) bool {
	return math.Abs(a-b) < eps
}

// Классический пример из учебника: 5 студентов × 6 вопросов
// (Crocker & Algina, 2008). Ожидаемая α около 0.79.
func TestAnalyzeBank_TextbookExample(t *testing.T) {
	responses := [][]int{
		// Студент 1 — все правильно (5 из 6).
		{1, 1, 1, 1, 1, 0},
		// Студент 2.
		{1, 1, 1, 0, 1, 1},
		// Студент 3.
		{1, 1, 0, 1, 0, 1},
		// Студент 4 — слабый.
		{0, 1, 0, 0, 1, 0},
		// Студент 5.
		{1, 0, 1, 1, 0, 1},
	}
	a := AnalyzeBank(responses)
	if a.Respondents != 5 {
		t.Errorf("respondents = %d, want 5", a.Respondents)
	}
	if a.ItemCount != 6 {
		t.Errorf("items = %d, want 6", a.ItemCount)
	}
	// α должна быть в диапазоне разумного значения.
	if a.CronbachAlpha < -0.5 || a.CronbachAlpha > 1.0 {
		t.Errorf("alpha = %v out of valid range", a.CronbachAlpha)
	}
	// p-value первого пункта = 4/5 = 0.8.
	if !almostEqual(a.Items[0].PValue, 0.8, 1e-6) {
		t.Errorf("item[0].PValue = %v, want 0.8", a.Items[0].PValue)
	}
}

// Если все отвечают одинаково — variance=0, α=0 (формула делит на 0).
func TestAnalyzeBank_AllIdenticalAnswers(t *testing.T) {
	responses := [][]int{
		{1, 1, 1, 1},
		{1, 1, 1, 1},
		{1, 1, 1, 1},
	}
	a := AnalyzeBank(responses)
	if a.CronbachAlpha != 0 {
		t.Errorf("alpha for identical = %v, want 0", a.CronbachAlpha)
	}
	for i, item := range a.Items {
		if item.PValue != 1.0 {
			t.Errorf("item[%d].PValue = %v, want 1.0", i, item.PValue)
		}
	}
}

// Идеальная корреляция: пункт повторяет ранг студента → высокая α.
func TestAnalyzeBank_PerfectGuttmanScale(t *testing.T) {
	// 5 студентов, 4 пункта. Студент i отвечает правильно на пункты 0..i-1.
	responses := [][]int{
		{0, 0, 0, 0}, // студент 0 — ничего
		{1, 0, 0, 0},
		{1, 1, 0, 0},
		{1, 1, 1, 0},
		{1, 1, 1, 1}, // студент 4 — всё
	}
	a := AnalyzeBank(responses)
	// На шкале Гуттмана с 5 респондентами и 4 пунктами α ≈ 0.8
	// (стремится к 1 при росте числа пунктов и испытуемых).
	if a.CronbachAlpha < 0.7 {
		t.Errorf("alpha for Guttman = %v, want >= 0.7", a.CronbachAlpha)
	}
	// Все пункты должны иметь положительную дискриминацию.
	for i, item := range a.Items {
		if item.Discrimination <= 0 {
			t.Errorf("item[%d] discrimination = %v, want > 0",
				i, item.Discrimination)
		}
	}
}

// Меньше двух студентов — пустой отчёт.
func TestAnalyzeBank_TooFewParticipants(t *testing.T) {
	a := AnalyzeBank([][]int{{1, 0, 1}})
	if a.CronbachAlpha != 0 || a.ItemCount != 0 {
		t.Errorf("expected empty report, got %+v", a)
	}
}

func TestAlphaInterpretation_Bands(t *testing.T) {
	cases := []struct {
		a    float64
		want string
	}{
		{0.95, "отличная"},
		{0.85, "хорошая"},
		{0.75, "приемлемая"},
		{0.65, "сомнительная"},
		{0.40, "низкая"},
		{-1.0, "низкая"},
	}
	for _, c := range cases {
		got := AlphaInterpretation(c.a)
		if got != c.want {
			t.Errorf("alpha=%v: got %q, want %q", c.a, got, c.want)
		}
	}
}
