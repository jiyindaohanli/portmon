package win32

import "strings"

// Guard 分级：
//
//	kernel   PID 0/4，内核本身，杀 = 蓝屏
//	critical 系统关键进程（PPL / IsProcessCritical），内核拒绝结束
//	system   核心系统组件，结束会立即崩溃或注销
//	risky    可结束但有明确副作用，需要二次确认
//	wsl      WSL/Docker 端口代理，杀宿主进程解决不了问题
//	""       普通进程
const (
	GuardKernel   = "kernel"
	GuardCritical = "critical"
	GuardSystem   = "system"
	GuardRisky    = "risky"
	GuardWSL      = "wsl"
)

// blocked 一律禁止结束，只给看。
var blocked = map[string]string{
	"system":              "Windows 内核进程，结束会立即蓝屏",
	"system idle process": "Windows 内核空闲进程",
	"registry":            "内核注册表进程，结束会立即蓝屏",
	"memcompression":      "内存压缩进程，结束会导致系统不稳定",
	"smss.exe":            "会话管理器，结束会立即蓝屏",
	"csrss.exe":           "客户端/服务器运行时子系统，结束会立即蓝屏",
	"wininit.exe":         "Windows 初始化进程，结束会注销并重启",
	"winlogon.exe":        "登录管理器，结束会立即注销",
	"lsass.exe":           "本地安全认证，结束会强制重启",
	"services.exe":        "服务控制管理器，结束会强制重启",
	"lsaiso.exe":          "凭据隔离，系统关键进程",
	"fontdrvhost.exe":     "字体驱动宿主，系统关键进程",
}

// risky 允许结束，但 UI 必须二次确认并说明后果。
var risky = map[string]string{
	"svchost.exe":                 "服务宿主，可能同时结束多个系统服务",
	"dwm.exe":                     "桌面窗口管理器，结束会让界面闪烁并重建",
	"explorer.exe":                "资源管理器，结束会隐藏任务栏并在数秒后自动重启",
	"spoolsv.exe":                 "打印后台服务，结束会取消所有打印任务",
	"audiodg.exe":                 "音频设备图，结束会中断当前音频播放",
	"sihost.exe":                  "Shell 基础设施宿主",
	"runtimebroker.exe":           "运行时代理，影响 UWP 应用权限",
	"searchindexer.exe":           "Windows 搜索索引",
	"startmenuexperiencehost.exe": "开始菜单，结束会重启开始菜单",
	"shellexperiencehost.exe":     "Shell 体验宿主，影响任务栏弹出的面板",
	"systemsettings.exe":          "系统设置界面",
	"ctfmon.exe":                  "输入法框架，结束会丢失输入法状态",
	"taskhostw.exe":               "任务宿主，可能带走计划任务",
}

// wsl 代理类，杀宿主无效。
var wslProxies = map[string]string{
	"wslrelay.exe":   "WSL2 端口转发代理，请到 WSL 内部停止服务",
	"vmmemwsl":       "WSL2 虚拟机内存进程，杀它会直接关掉整个 WSL",
	"vmmem":          "Hyper-V/容器虚拟机内存进程，杀它会关掉所有虚拟机",
	"wslservice.exe": "WSL 服务宿主，杀它会停止所有 WSL 发行版",
	"vmwp.exe":       "Hyper-V 虚拟机工作进程",
}

// Guard 返回分级与给用户看的解释。port 用于 PID 4 的 http.sys 提示。
func Guard(pid uint32, name string, port int) (string, string) {
	if pid == 0 {
		return GuardKernel, "System Idle Process 是内核空闲进程，不可结束"
	}
	if pid == 4 {
		if port == 80 || port == 443 || port == 8080 || port == 8081 {
			return GuardKernel, "占用者是 System 内核进程（通常是 http.sys 的 URL 预留），不能结束。" +
				"请在管理员终端执行 netsh http show servicestate 找到真正的注册方"
		}
		return GuardKernel, "System 是 Windows 内核进程，结束会立即蓝屏"
	}

	lower := strings.ToLower(name)
	if msg, ok := blocked[lower]; ok {
		return GuardSystem, msg
	}
	if msg, ok := wslProxies[lower]; ok {
		return GuardWSL, msg
	}

	if CriticalPID(pid) {
		return GuardCritical, "这是系统关键进程，内核禁止结束（会触发系统重启）"
	}

	if msg, ok := risky[lower]; ok {
		return GuardRisky, msg
	}
	return "", ""
}

// Killable 判断该分级是否允许发起结束操作。
func Killable(guard string) bool {
	switch guard {
	case GuardKernel, GuardCritical, GuardSystem:
		return false
	}
	return true
}
