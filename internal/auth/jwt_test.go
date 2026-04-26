package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func newTestIssuer(t *testing.T) *Issuer {
	t.Helper()
	return NewIssuer("test-secret-32-bytes-long-padding!!", 15*time.Minute, 7*24*time.Hour)
}

func TestIssueAndParse_RoundTrip(t *testing.T) {
	iss := newTestIssuer(t)
	uid := uuid.New()

	pair, err := iss.IssuePair(uid, "teacher")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if pair.Access == "" || pair.Refresh == "" {
		t.Fatal("empty access/refresh")
	}
	if pair.Access == pair.Refresh {
		t.Fatal("access and refresh are equal — different ttl/typ should differ")
	}

	c, err := iss.Parse(pair.Access)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.UserID != uid {
		t.Errorf("UserID = %v, want %v", c.UserID, uid)
	}
	if c.Role != "teacher" {
		t.Errorf("Role = %q, want teacher", c.Role)
	}
	if c.Type != "access" {
		t.Errorf("Type = %q, want access", c.Type)
	}
}

func TestParse_RejectsTamperedSignature(t *testing.T) {
	iss := newTestIssuer(t)
	pair, _ := iss.IssuePair(uuid.New(), "student")

	// Меняем последний символ — подпись становится невалидной.
	tampered := pair.Access[:len(pair.Access)-1] + "X"
	if _, err := iss.Parse(tampered); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expected ErrTokenInvalid, got %v", err)
	}
}

func TestParse_RejectsForeignSecret(t *testing.T) {
	a := newTestIssuer(t)
	b := NewIssuer("other-secret-totally-different-key!!", time.Minute, time.Hour)

	pair, _ := a.IssuePair(uuid.New(), "student")
	if _, err := b.Parse(pair.Access); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("issuer with different secret should reject token, got %v", err)
	}
}

func TestParse_DetectsExpiry(t *testing.T) {
	iss := NewIssuer("s", -time.Second, time.Hour) // access уже протух
	pair, _ := iss.IssuePair(uuid.New(), "student")

	if _, err := iss.Parse(pair.Access); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expected ErrTokenExpired, got %v", err)
	}
}
