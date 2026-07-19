package protocol

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"
)

var rawURL = base64.RawURLEncoding

type Envelope struct {
	FormatVersion int    `json:"formatVersion"`
	KeyID         string `json:"keyId"`
	Algorithm     string `json:"algorithm"`
	Payload       string `json:"payload"`
	Signature     string `json:"signature"`
}

func KeyID(publicKey ed25519.PublicKey) string {
	sum := sha256.Sum256(publicKey)
	return "ed25519-" + hex.EncodeToString(sum[:8])
}
func VerifyEnvelope(envelope Envelope, trusted map[string]ed25519.PublicKey) ([]byte, error) {
	if envelope.FormatVersion != 1 || envelope.Algorithm != "Ed25519" {
		return nil, errors.New("unsupported envelope")
	}
	key, ok := trusted[envelope.KeyID]
	if !ok || KeyID(key) != envelope.KeyID {
		return nil, errors.New("untrusted release key")
	}
	payload, err := rawURL.DecodeString(envelope.Payload)
	if err != nil {
		return nil, errors.New("invalid payload encoding")
	}
	signature, err := rawURL.DecodeString(envelope.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return nil, errors.New("invalid signature encoding")
	}
	if !ed25519.Verify(key, payload, signature) {
		return nil, errors.New("invalid signature")
	}
	return payload, nil
}

type queryPair struct{ name, value string }

func CanonicalQuery(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	parts := strings.Split(raw, "&")
	pairs := make([]queryPair, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			return "", errors.New("empty query pair")
		}
		nameRaw, valueRaw, found := strings.Cut(part, "=")
		if !found {
			valueRaw = ""
		}
		name, err := url.PathUnescape(nameRaw)
		if err != nil {
			return "", err
		}
		value, err := url.PathUnescape(valueRaw)
		if err != nil {
			return "", err
		}
		if !validCanonicalText(name) || !validCanonicalText(value) {
			return "", errors.New("query contains invalid UTF-8 or control characters")
		}
		pairs = append(pairs, queryPair{rfc3986(name), rfc3986(value)})
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		if pairs[i].name == pairs[j].name {
			return pairs[i].value < pairs[j].value
		}
		return pairs[i].name < pairs[j].name
	})
	out := make([]string, len(pairs))
	for i, pair := range pairs {
		out[i] = pair.name + "=" + pair.value
	}
	return strings.Join(out, "&"), nil
}
func CanonicalPath(escaped string) (string, error) {
	if escaped == "" || escaped[0] != '/' || strings.Contains(escaped, "\\") || strings.Contains(escaped, "//") {
		return "", errors.New("invalid absolute path")
	}
	if escaped == "/" {
		return escaped, nil
	}
	segments := strings.Split(strings.TrimPrefix(escaped, "/"), "/")
	encoded := make([]string, len(segments))
	for i, segment := range segments {
		decoded, err := url.PathUnescape(segment)
		if err != nil {
			return "", err
		}
		if !validCanonicalText(decoded) || decoded == "" || decoded == "." || decoded == ".." || strings.ContainsAny(decoded, "/\\") {
			return "", errors.New("invalid path segment")
		}
		encoded[i] = rfc3986(decoded)
	}
	return "/" + strings.Join(encoded, "/"), nil
}
func CanonicalRequest(method, escapedPath, rawQuery string, body []byte, timestamp int64, nonce []byte) ([]byte, error) {
	path, err := CanonicalPath(escapedPath)
	if err != nil {
		return nil, err
	}
	query, err := CanonicalQuery(rawQuery)
	if err != nil {
		return nil, err
	}
	if query != "" {
		path += "?" + query
	}
	sum := sha256.Sum256(body)
	return []byte(fmt.Sprintf("LANREADY-REQUEST-V1\n%s\n%s\n%s\n%d\n%s", strings.ToUpper(method), path, hex.EncodeToString(sum[:]), timestamp, rawURL.EncodeToString(nonce))), nil
}
func validCanonicalText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func rfc3986(value string) string {
	var out strings.Builder
	for _, b := range []byte(value) {
		if b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-' || b == '.' || b == '_' || b == '~' {
			out.WriteByte(b)
		} else {
			fmt.Fprintf(&out, "%%%02X", b)
		}
	}
	return out.String()
}
