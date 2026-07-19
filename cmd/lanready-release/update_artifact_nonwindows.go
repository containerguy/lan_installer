//go:build !windows

package main

import (
	"errors"

	"github.com/containerguy/lan_installer/internal/protocol"
)

func platformVerifyClientUpdateArtifact(string, protocol.ClientUpdateMetadata) error {
	return errors.New("sign-update muss auf Windows ausgeführt werden, damit Authenticode, Herausgeber und Dateiversion verbindlich geprüft werden")
}
