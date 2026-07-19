//go:build !windows

package deviceclient

func protectPrivateKey(value []byte) ([]byte, error)   { return append([]byte(nil), value...), nil }
func unprotectPrivateKey(value []byte) ([]byte, error) { return append([]byte(nil), value...), nil }
