package auth

import "testing"

func TestHashAndVerifyPassword_RoundTrip(t *testing.T) {
	const plain = "correct horse battery staple"
	h, err := HashPassword(plain)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if h == plain {
		t.Fatal("hash equals plain — bcrypt is broken")
	}
	if !VerifyPassword(h, plain) {
		t.Fatal("VerifyPassword returned false for correct password")
	}
}

func TestVerifyPassword_RejectsWrong(t *testing.T) {
	h, _ := HashPassword("right-password")
	if VerifyPassword(h, "wrong-password") {
		t.Fatal("VerifyPassword accepted wrong password")
	}
}

func TestHashPassword_DifferentSaltsForSamePlain(t *testing.T) {
	a, _ := HashPassword("same")
	b, _ := HashPassword("same")
	if a == b {
		t.Fatal("two hashes of same password are identical — bcrypt isn't salting")
	}
}
