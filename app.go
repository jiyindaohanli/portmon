package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"portmon/internal/monitor"
	"portmon/internal/notify"
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

	watchMu    sync.RWMutex
	watchPorts []int
	tracker    *notify.Tracker
	// 冷启动宽限期：WSL 桥接是异步探测的，端口表会先空后有，
	// 这段时间内只更新基线、不发通知，否则每次启动都会误报。
	notifyAfter time.Time
}

func NewApp() *App {
	return &App{
		poller:  monitor.New(),
		sampler: sysinfo.NewSampler(),
		tracker: notify.NewTracker(),
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	a.watchMu.Lock()
	a.watchPorts = store.Load().WatchPorts
	a.watchMu.Unlock()
	a.notifyAfter = time.Now().Add(3 * time.Second)

	// Windows toast 要先注册 AUMID 和激活器，否则弹不出来。
	// 失败不影响主功能，端口监控照常。
	if err := wruntime.InitializeNotifications(ctx); err != nil {
		notifyLog("InitializeNotifications: " + err.Error())
	}

	go a.poller.Loop(func(s monitor.Snapshot) {
		wruntime.EventsEmit(ctx, "ports:snapshot", s)
		a.notifyWatch(s)
	}, func(err error) {
		wruntime.EventsEmit(ctx, "ports:error", err.Error())
	})

	// 整机指标 1 秒一次就够，和端口采集频率无关
	go a.sampler.Run(time.Second, func(st sysinfo.Stats) {
		wruntime.EventsEmit(ctx, "sys:stats", st)
	})
}

func (a *App) shutdown(ctx context.Context) {}

// notifyWatch 盯守端口的占用状态翻转时弹系统通知。窗口收在托盘里也能收到。
func (a *App) notifyWatch(s monitor.Snapshot) {
	a.watchMu.RLock()
	ports := a.watchPorts
	a.watchMu.RUnlock()
	if len(ports) == 0 {
		return
	}

	owners := make(map[int]notify.Owner, len(ports))
	for _, e := range s.Entries {
		if _, ok := owners[e.Port]; ok {
			continue
		}
		owners[e.Port] = notify.Owner{Proc: e.Proc, PID: e.PID, Source: e.Source}
	}

	for _, tr := range a.tracker.Diff(ports, owners) {
		if time.Now().Before(a.notifyAfter) {
			continue // 宽限期内只更新基线，不弹通知
		}
		title, body := watchText(tr)
		err := wruntime.SendNotification(a.ctx, wruntime.NotificationOptions{
			ID:    fmt.Sprintf("portmon-%d-%d", tr.Port, time.Now().UnixMilli()),
			Title: title,
			Body:  body,
		})
		if err != nil {
			notifyLog("SendNotification: " + err.Error())
		}
	}
}

func watchText(tr notify.Transition) (string, string) {
	if !tr.InUse {
		return fmt.Sprintf("端口 %d 已释放", tr.Port), "现在没有程序在监听"
	}
	if tr.Owner.Source == "wsl" {
		return fmt.Sprintf("端口 %d 被占用", tr.Port), tr.Owner.Proc + "（WSL 内部）"
	}
	if tr.Owner.Proc == "" {
		return fmt.Sprintf("端口 %d 被占用", tr.Port), fmt.Sprintf("PID %d", tr.Owner.PID)
	}
	return fmt.Sprintf("端口 %d 被占用", tr.Port), fmt.Sprintf("%s（PID %d）", tr.Owner.Proc, tr.Owner.PID)
}

// notifyLog 通知是 Windows 上最容易静默失败的一环，出错必须留痕。
func notifyLog(msg string) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return
	}
	dir = filepath.Join(dir, "PortMon")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "notify.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), msg)
}

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
	if err := store.Save(cfg); err != nil {
		return err
	}

	a.watchMu.Lock()
	a.watchPorts = cfg.WatchPorts
	a.watchMu.Unlock()
	return nil
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
