package main

import (
	"context"
	"os"
	"os/exec"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"portmon/internal/monitor"
	"portmon/internal/store"
	"portmon/internal/sysinfo"
	"portmon/internal/win32"
)

// Settings 前端启动时要的一次性配置。
type Settings struct {
	Elevated  bool   `json:"elevated"`
	Autostart bool   `json:"autostart"`
	ExePath   string `json:"exePath"`
	Interval  int64  `json:"interval"`
}

type App struct {
	ctx     context.Context
	poller  *monitor.Poller
	sampler *sysinfo.Sampler
}

func NewApp() *App {
	return &App{poller: monitor.New(), sampler: sysinfo.NewSampler()}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	go a.poller.Loop(func(s monitor.Snapshot) {
		wruntime.EventsEmit(ctx, "ports:snapshot", s)
	}, func(err error) {
		wruntime.EventsEmit(ctx, "ports:error", err.Error())
	})

	// 整机指标 1 秒一次就够，和端口采集频率无关
	go a.sampler.Run(time.Second, func(st sysinfo.Stats) {
		wruntime.EventsEmit(ctx, "sys:stats", st)
	})
}

func (a *App) shutdown(ctx context.Context) {}

// GetSnapshot 首次加载用，之后靠 ports:snapshot 事件推送。
func (a *App) GetSnapshot() monitor.Snapshot {
	return a.poller.Current()
}

// GetSysStats 首屏用的整机指标，之后靠 sys:stats 事件推送。
func (a *App) GetSysStats() sysinfo.Stats {
	return a.sampler.Current()
}

func (a *App) GetSettings() Settings {
	exe, _ := os.Executable()
	return Settings{
		Elevated:  win32.IsElevated(),
		Autostart: win32.AutostartEnabled(),
		ExePath:   exe,
		Interval:  a.poller.Interval().Milliseconds(),
	}
}

// GetDetail 打开详情抽屉时才调用，避免高频采样时反复开进程句柄。
func (a *App) GetDetail(pid uint32) win32.ProcDetail {
	return win32.Detailed(pid, win32.ProcTable())
}

// Kill 普通权限结束进程。权限不足时返回 NeedAdmin，前端引导提权重试。
func (a *App) Kill(pid uint32, tree bool) win32.KillResult {
	return win32.Kill(pid, tree)
}

// KillElevated 拉起带 -kill 参数的管理员实例完成结束，结果经临时文件回传。
func (a *App) KillElevated(pid uint32, tree bool) win32.KillResult {
	return win32.ElevatedKill(pid, tree)
}

func (a *App) SetPaused(paused bool) monitor.Snapshot {
	a.poller.SetPaused(paused)
	snap := a.poller.Current()
	snap.Paused = paused
	return snap
}

// SetFocus 窗口聚焦时加快采样，失焦后降频省 CPU。
func (a *App) SetFocus(focused bool) {
	a.poller.SetFast(focused)
}

func (a *App) SetAutostart(enable bool) error {
	return win32.SetAutostart(enable)
}

// GetWatchPorts 返回盯守端口列表，持久化在 %APPDATA%\PortMon\config.json。
func (a *App) GetWatchPorts() []int {
	return store.Load().WatchPorts
}

// SetWatchPorts 保存盯守端口。越界和重复在这里就丢掉，前端不用重复校验。
func (a *App) SetWatchPorts(ports []int) error {
	cfg := store.Load()
	cfg.WatchPorts = store.Normalize(ports)
	return store.Save(cfg)
}

// RevealExe 在资源管理器里定位该进程的可执行文件。
func (a *App) RevealExe(pid uint32) string {
	d := win32.Detailed(pid, win32.ProcTable())
	if d.ExePath == "" {
		return ""
	}
	_ = exec.Command("explorer.exe", "/select,"+d.ExePath).Start()
	return d.ExePath
}

func (a *App) CopyText(text string) {
	if a.ctx != nil {
		wruntime.ClipboardSetText(a.ctx, text)
	}
}

func (a *App) WindowMin()  { wruntime.WindowMinimise(a.ctx) }
func (a *App) WindowMax()  { wruntime.WindowToggleMaximise(a.ctx) }
func (a *App) WindowHide() { wruntime.WindowHide(a.ctx) }
func (a *App) WindowShow() { wruntime.WindowShow(a.ctx) }
func (a *App) Quit()       { wruntime.Quit(a.ctx) }

// ResumeFromTray 托盘点击"打开"时用，保证窗口真的到前台。
func (a *App) ResumeFromTray() {
	if a.ctx == nil {
		return
	}
	wruntime.WindowShow(a.ctx)
	time.Sleep(20 * time.Millisecond)
	wruntime.WindowSetTitle(a.ctx, "PortMon")
}
