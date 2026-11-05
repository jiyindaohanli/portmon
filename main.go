package main

import (
	"embed"
	"os"

	"fyne.io/systray"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"portmon/internal/win32"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed assets/icon.ico
var trayIcon []byte

func main() {
	// 提权 helper：PortMon.exe -kill -pid <n> [-tree] -result <file>
	if win32.RunElevatedHelper(os.Args[1:]) {
		return
	}

	// 开机自启时直接收进托盘，不打扰用户
	startHidden := false
	for _, a := range os.Args[1:] {
		if a == "-autostart" {
			startHidden = true
		}
	}

	app := NewApp()
	startTray(app)

	err := wails.Run(&options.App{
		Title:             "PortMon",
		Width:             1200,
		Height:            780,
		MinWidth:          940,
		MinHeight:         600,
		Frameless:         true,
		StartHidden:       startHidden,
		HideWindowOnClose: true,
		BackgroundColour:  &options.RGBA{R: 255, G: 249, B: 240, A: 255},
		Assets:            assets,
		OnStartup:         app.startup,
		OnShutdown:        app.shutdown,
		Bind:              []interface{}{app},
		Windows: &windows.Options{
			Theme:             windows.Light,
			DisableWindowIcon: false,
		},
	})
	if err != nil {
		println("PortMon 启动失败:", err.Error())
	}
}

// startTray Wails v2 没有托盘支持，用 fyne.io/systray（Windows 下纯 Go，无需 cgo）。
func startTray(app *App) {
	go systray.Run(func() {
		systray.SetIcon(trayIcon)
		systray.SetTooltip("PortMon · 端口守护")

		mShow := systray.AddMenuItem("打开 PortMon", "显示主窗口")
		mPause := systray.AddMenuItemCheckbox("暂停监听", "暂停端口采集", false)
		mAuto := systray.AddMenuItemCheckbox("开机自启", "登录后自动运行", win32.AutostartEnabled())
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("退出", "退出 PortMon")

		go func() {
			for {
				select {
				case <-mShow.ClickedCh:
					app.ResumeFromTray()
				case <-mPause.ClickedCh:
					next := !app.poller.Paused()
					app.SetPaused(next)
					if next {
						mPause.Check()
					} else {
						mPause.Uncheck()
					}
				case <-mAuto.ClickedCh:
					next := !win32.AutostartEnabled()
					if win32.SetAutostart(next) == nil {
						if next {
							mAuto.Check()
						} else {
							mAuto.Uncheck()
						}
					}
				case <-mQuit.ClickedCh:
					systray.Quit()
					if app.ctx != nil {
						wruntime.Quit(app.ctx)
					}
					os.Exit(0)
				}
			}
		}()
	}, func() {})
}
