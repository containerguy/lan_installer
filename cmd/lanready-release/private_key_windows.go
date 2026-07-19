//go:build windows

package main

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type aclHeader struct {
	Revision  byte
	Reserved  byte
	Size      uint16
	ACECount  uint16
	Reserved2 uint16
}

func securePrivateKeyFile(path string, info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return errors.New("Private-Key-Datei muss regulär sein")
	}
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("Private-Key-DACL muss vor Vererbung geschützt sein")
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return errors.New("Private-Key-Datei besitzt keinen prüfbaren Besitzer")
	}
	current, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || !owner.Equals(current.User.Sid) {
		return errors.New("Private-Key-Datei muss dem aktuellen Windows-Benutzer gehören")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return errors.New("Private-Key-Datei besitzt keine restriktive DACL")
	}
	system, _ := windows.StringToSid("S-1-5-18")
	administrators, _ := windows.StringToSid("S-1-5-32-544")
	header := (*aclHeader)(unsafe.Pointer(dacl))
	for index := uint32(0); index < uint32(header.ACECount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err = windows.GetAce(dacl, index, &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.Equals(current.User.Sid) && !sid.Equals(system) && !sid.Equals(administrators) {
			return errors.New("Private-Key-DACL gewährt einem nicht erlaubten Principal Zugriff")
		}
	}
	return nil
}

func hardenPrivateKeyFile(path string) error {
	current, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sid := current.User.Sid.String()
	descriptor, err := windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;;FA;;;" + sid + ")(A;;FR;;;SY)(A;;FR;;;BA)")
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, owner, nil, dacl, nil)
}
