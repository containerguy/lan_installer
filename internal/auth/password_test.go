package auth

import "testing"

func TestPasswordHash(t *testing.T) {
	hash, err := HashPassword("a sufficiently long password")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "a sufficiently long password") {
		t.Fatal("valid password rejected")
	}
	if VerifyPassword(hash, "a different long password") {
		t.Fatal("invalid password accepted")
	}
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("short password accepted")
	}
}
