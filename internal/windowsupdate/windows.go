//go:build windows

package windowsupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/containerguy/lan_installer/internal/model"
)

func OSVersion(ctx context.Context) string {
	output, err := exec.CommandContext(ctx, "cmd.exe", "/c", "ver").CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func Run(ctx context.Context, mode string) model.WinReport {
	result := model.WinReport{Mode: mode}
	if mode != "check" && mode != "install" {
		result.ErrorText = fmt.Sprintf("unknown Windows Update mode %q", mode)
		return result
	}

	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$session = New-Object -ComObject Microsoft.Update.Session
$searcher = $session.CreateUpdateSearcher()
$search = $searcher.Search("IsInstalled=0 and IsHidden=0")
$missing = $search.Updates.Count
$installed = 0
$rebootRequired = $false
if ('%s' -eq 'install' -and $missing -gt 0) {
    $updates = New-Object -ComObject Microsoft.Update.UpdateColl
    foreach ($update in $search.Updates) {
        if (-not $update.EulaAccepted) { $update.AcceptEula() }
        [void]$updates.Add($update)
    }
    $downloader = $session.CreateUpdateDownloader()
    $downloader.Updates = $updates
    [void]$downloader.Download()
    $installer = $session.CreateUpdateInstaller()
    $installer.Updates = $updates
    $installResult = $installer.Install()
    $installed = $updates.Count
    $rebootRequired = $installResult.RebootRequired
}
@{ missingUpdates = $missing; installed = $installed; rebootRequired = $rebootRequired } | ConvertTo-Json -Compress
`, mode)
	output, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		result.ErrorText = fmt.Sprintf("Windows Update failed: %v: %s", err, strings.TrimSpace(string(output)))
		return result
	}
	var values struct {
		MissingUpdates int  `json:"missingUpdates"`
		Installed      int  `json:"installed"`
		RebootRequired bool `json:"rebootRequired"`
	}
	if err := json.Unmarshal(output, &values); err != nil {
		result.ErrorText = fmt.Sprintf("decode Windows Update result: %v", err)
		return result
	}
	result.MissingUpdates = values.MissingUpdates
	result.Installed = values.Installed
	result.RebootRequired = values.RebootRequired
	return result
}
