package services

import "testing"

func TestXPAward_WrongAnswer(t *testing.T) {
	got := XPAward(false, 5, 1000, 30000, DefaultXPParams())
	if got != 0 {
		t.Errorf("wrong answer: got %d, want 0", got)
	}
}

func TestXPAward_FastCorrectGetsMore(t *testing.T) {
	p := DefaultXPParams()
	fast := XPAward(true, 3, 1000, 30000, p)  // 3% времени
	slow := XPAward(true, 3, 28000, 30000, p) // 93% времени
	max := XPAward(true, 3, 0, 30000, p)      // мгновенный ответ
	noLimit := XPAward(true, 3, 5000, 0, p)   // без лимита

	if fast <= slow {
		t.Errorf("fast=%d, slow=%d — fast must outscore slow", fast, slow)
	}
	if max < fast {
		t.Errorf("instant=%d, fast=%d — instant must be the max", max, fast)
	}
	if noLimit != 30 {
		// difficulty 3 * speed 1.0 * 10 = 30
		t.Errorf("no-limit: got %d, want 30", noLimit)
	}
}

func TestXPAward_ClampsDifficulty(t *testing.T) {
	p := DefaultXPParams()
	low := XPAward(true, 0, 0, 1000, p)   // clamp to 1
	high := XPAward(true, 99, 0, 1000, p) // clamp to 5
	want_low := XPAward(true, 1, 0, 1000, p)
	want_high := XPAward(true, 5, 0, 1000, p)
	if low != want_low {
		t.Errorf("clamp low: got %d, want %d", low, want_low)
	}
	if high != want_high {
		t.Errorf("clamp high: got %d, want %d", high, want_high)
	}
}

func TestLevelFromXP(t *testing.T) {
	cases := []struct {
		xp   int
		want int
	}{
		{0, 0},
		{99, 0},
		{100, 1},
		{399, 1},
		{400, 2},
		{900, 3},
		{10_000, 10},
	}
	for _, tc := range cases {
		got := LevelFromXP(tc.xp)
		if got != tc.want {
			t.Errorf("LevelFromXP(%d) = %d, want %d", tc.xp, got, tc.want)
		}
	}
}
