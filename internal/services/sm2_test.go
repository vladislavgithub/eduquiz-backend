package services

import (
	"testing"
	"time"
)

func TestSm2Update_FirstSuccess(t *testing.T) {
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	s := NewSm2State()

	got := Sm2Update(s, 5, now)

	if got.Repetitions != 1 {
		t.Errorf("repetitions = %d, want 1", got.Repetitions)
	}
	if got.IntervalDays != 1 {
		t.Errorf("interval = %d, want 1", got.IntervalDays)
	}
	if got.EF <= 2.5 {
		t.Errorf("EF = %.4f, want > 2.5 after q=5", got.EF)
	}
	wantDue := now.AddDate(0, 0, 1)
	if !got.DueDate.Equal(wantDue) {
		t.Errorf("DueDate = %v, want %v", got.DueDate, wantDue)
	}
}

func TestSm2Update_SecondSuccessJumpsToSix(t *testing.T) {
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	s := NewSm2State()

	s = Sm2Update(s, 5, now)                  // rep=1, interval=1
	s = Sm2Update(s, 4, now.AddDate(0, 0, 1)) // rep=2, interval=6

	if s.IntervalDays != 6 {
		t.Errorf("interval after 2nd success = %d, want 6", s.IntervalDays)
	}
	if s.Repetitions != 2 {
		t.Errorf("repetitions = %d, want 2", s.Repetitions)
	}
}

func TestSm2Update_FailureResets(t *testing.T) {
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	s := NewSm2State()
	s = Sm2Update(s, 5, now)
	s = Sm2Update(s, 5, now.AddDate(0, 0, 1))
	s = Sm2Update(s, 5, now.AddDate(0, 0, 7))
	if s.Repetitions != 3 {
		t.Fatalf("setup: repetitions = %d, want 3", s.Repetitions)
	}

	got := Sm2Update(s, 1, now.AddDate(0, 0, 14))

	if got.Repetitions != 0 {
		t.Errorf("after q=1: repetitions = %d, want 0", got.Repetitions)
	}
	if got.IntervalDays != 1 {
		t.Errorf("after q=1: interval = %d, want 1", got.IntervalDays)
	}
}

func TestSm2Update_EFFloorAt1_3(t *testing.T) {
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	s := NewSm2State()

	for i := 0; i < 30; i++ {
		s = Sm2Update(s, 0, now.AddDate(0, 0, i))
	}

	if s.EF < 1.3 {
		t.Errorf("EF dropped below floor: %.4f", s.EF)
	}
	if s.EF != 1.3 {
		t.Errorf("EF should clamp to 1.3 after many failures, got %.4f", s.EF)
	}
}

func TestSm2Update_QClamping(t *testing.T) {
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	s := NewSm2State()

	got := Sm2Update(s, 100, now)
	want := Sm2Update(s, 5, now)

	if got != want {
		t.Errorf("q=100 should clamp to q=5; got %+v, want %+v", got, want)
	}
}
