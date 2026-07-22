package store

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"
)

func TestEnrollmentRateLimitIsPersistent(t *testing.T) {
	path := t.TempDir() + "/rate.db"
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i := 0; i < 8; i++ {
		allowed, allowErr := st.AllowEnrollmentAttempt(context.Background(), "203.0.113.10", "INVALID-CODE", now)
		if allowErr != nil || !allowed {
			t.Fatalf("attempt %d: allowed=%v err=%v", i+1, allowed, allowErr)
		}
	}
	allowed, err := st.AllowEnrollmentAttempt(context.Background(), "203.0.113.10", "INVALID-CODE", now)
	if err != nil || allowed {
		t.Fatalf("ninth attempt allowed=%v err=%v", allowed, err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	allowed, err = st.AllowEnrollmentAttempt(context.Background(), "203.0.113.10", "INVALID-CODE", now)
	if err != nil || allowed {
		t.Fatalf("limit did not survive reopen: allowed=%v err=%v", allowed, err)
	}
}

func TestEnrollmentCodeConcurrentUseSucceedsOnce(t *testing.T) {
	st, err := Open(t.TempDir() + "/concurrent-enroll.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err = st.db.Exec(`INSERT INTO users(id,username,password_hash,active) VALUES(1,'admin','unused',1)`); err != nil {
		t.Fatal(err)
	}
	code, err := st.CreateEnrollmentCode(context.Background(), 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		key := make([]byte, 32)
		_, _ = rand.Read(key)
		go func(publicKey []byte) {
			_, enrollErr := st.EnrollDevice(context.Background(), code.Code, publicKey, "PC", "11", "1.0.0")
			results <- enrollErr
		}(key)
	}
	success, rejected := 0, 0
	for i := 0; i < 2; i++ {
		err = <-results
		if err == nil {
			success++
		} else if errors.Is(err, ErrEnrollmentCodeInvalid) {
			rejected++
		} else {
			t.Fatalf("unexpected enroll error: %v", err)
		}
	}
	if success != 1 || rejected != 1 {
		t.Fatalf("success=%d rejected=%d", success, rejected)
	}
}

func TestEnrollmentCodeCanBeRevokedBeforeUse(t *testing.T) {
	st, err := Open(t.TempDir() + "/revoke.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err = st.db.Exec(`INSERT INTO users(id,username,password_hash,active) VALUES(1,'admin','unused',1)`); err != nil {
		t.Fatal(err)
	}
	code, err := st.CreateEnrollmentCode(context.Background(), 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := st.RevokeEnrollmentCode(context.Background(), code.ID)
	if err != nil || !revoked {
		t.Fatalf("revoke: %v %v", revoked, err)
	}
	if _, err = st.EnrollDevice(context.Background(), code.Code, make([]byte, 32), "PC", "11", "1.0.0"); err != ErrEnrollmentCodeInvalid {
		t.Fatalf("revoked code accepted: %v", err)
	}
}
