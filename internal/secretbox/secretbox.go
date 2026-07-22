package secretbox

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
)

type Box struct {
	key [chacha20poly1305.KeySize]byte
}

func FromFile(path string) (*Box, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("WebDAV-Masterschlüssel ist nicht gültig base64: %w", err)
	}
	if len(decoded) != chacha20poly1305.KeySize {
		return nil, fmt.Errorf("WebDAV-Masterschlüssel muss %d Byte enthalten", chacha20poly1305.KeySize)
	}
	b := &Box{}
	copy(b.key[:], decoded)
	return b, nil
}
func New(key []byte) (*Box, error) {
	if len(key) != chacha20poly1305.KeySize {
		return nil, errors.New("invalid key length")
	}
	b := &Box{}
	copy(b.key[:], key)
	return b, nil
}
func (b *Box) Seal(plaintext []byte, aad string) (nonce, ciphertext []byte, err error) {
	aead, err := chacha20poly1305.NewX(b.key[:])
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	ciphertext = aead.Seal(nil, nonce, plaintext, []byte(aad))
	return nonce, ciphertext, nil
}
func (b *Box) Open(nonce, ciphertext []byte, aad string) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(b.key[:])
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, nonce, ciphertext, []byte(aad))
}
func WebDAVAAD(sourceID int64) string { return fmt.Sprintf("lanready:webdav:v1:%d", sourceID) }
