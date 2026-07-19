//go:build windows

package windowsapp

import (
	"context"
	"fmt"
	"os"
	"time"

	toast "git.sr.ht/~jackmordaunt/go-toast/v2"
)

func (a *App) startAgent(ctx context.Context) {
	executable, _ := os.Executable()
	_ = toast.SetAppData(toast.AppData{
		AppID:         "LANReady",
		GUID:          "3f2b47c6-0a34-4aa4-9a71-7d45d3f63f2e",
		ActivationExe: executable,
	})
	toast.SetActivationCallback(func(string, []toast.UserData) { a.ShowWindow() })
	go func() {
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		lastNotice := ""
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				state, err := a.State()
				if err == nil && state.Connected && state.ServerReachable && (state.UpdateRequired || state.UpdateAvailable) {
					key := fmt.Sprintf("%t:%s", state.UpdateRequired, state.UpdateVersion)
					if key != lastNotice {
						title := "LANReady-Update verfügbar"
						body := "Ein signiertes Stable-Update ist verfügbar. Klicke hier, um LANReady zu öffnen."
						if state.UpdateRequired {
							title = "LANReady-Pflichtupdate"
							body = "Vor weiteren LAN-Aktionen muss ein signiertes Clientupdate installiert werden."
						}
						_ = (&toast.Notification{AppID: "LANReady", Title: title, Body: body, ActivationArguments: "show"}).Push()
						lastNotice = key
					}
				}
				timer.Reset(30 * time.Minute)
			}
		}
	}()
}
