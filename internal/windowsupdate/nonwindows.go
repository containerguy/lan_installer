//go:build !windows

package windowsupdate

import (
	"context"
	"runtime"

	"github.com/containerguy/lan_installer/internal/model"
)

func OSVersion(context.Context) string {
	return runtime.GOOS + "/" + runtime.GOARCH
}

func Run(_ context.Context, mode string) model.WinReport {
	return model.WinReport{Mode: mode, ErrorText: "Windows Update is only available on Windows"}
}
