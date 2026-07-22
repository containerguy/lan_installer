package secretbox

import (
	"bytes"
	"testing"
)

func TestSealOpenAndTamper(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	box, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	nonce, cipher, err := box.Seal([]byte("app-password"), WebDAVAAD(42))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := box.Open(nonce, cipher, WebDAVAAD(42))
	if err != nil || string(plain) != "app-password" {
		t.Fatalf("open: %q %v", plain, err)
	}
	cipher[0] ^= 1
	if _, err = box.Open(nonce, cipher, WebDAVAAD(42)); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}
