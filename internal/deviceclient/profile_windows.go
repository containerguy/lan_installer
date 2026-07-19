//go:build windows

package deviceclient

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

func dataBlob(value []byte) windows.DataBlob {
	if len(value) == 0 {
		return windows.DataBlob{}
	}
	return windows.DataBlob{Size: uint32(len(value)), Data: &value[0]}
}

func copyBlob(blob windows.DataBlob) []byte {
	if blob.Size == 0 || blob.Data == nil {
		return nil
	}
	return append([]byte(nil), unsafe.Slice(blob.Data, int(blob.Size))...)
}

func protectPrivateKey(value []byte) ([]byte, error) {
	in, out := dataBlob(value), windows.DataBlob{}
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return copyBlob(out), nil
}

func unprotectPrivateKey(value []byte) ([]byte, error) {
	in, out := dataBlob(value), windows.DataBlob{}
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return copyBlob(out), nil
}
