package win32

import (
	"encoding/json"
	"os"
	"strconv"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modShell32       = windows.NewLazySystemDLL("shell32.dll")
	procShellExecute = modShell32.NewProc("ShellExecuteW")
)

const swShowNormal = 1

// IsElevated 判断当前进程是否已提权。x/sys 没有封装 TokenElevation，直接查。
func IsElevated() bool {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &tok); err != nil {
		return false
	}
	defer tok.Close()

	const tokenElevation = 20
	var elevated uint32
	var retLen uint32
	err := windows.GetTokenInformation(tok, tokenElevation,
		(*byte)(unsafe.Pointer(&elevated)), uint32(unsafe.Sizeof(elevated)), &retLen)
	if err != nil {
		return false
	}
	return elevated != 0
}

// RunElevated 用 runas 动词重新拉起自己。UAC 被用户取消会返回错误。
func RunElevated(args string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	exePtr, _ := windows.UTF16PtrFromString(exe)
	argPtr, _ := windows.UTF16PtrFromString(args)
	ret, _, callErr := procShellExecute.Call(0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(exePtr)),
		uintptr(unsafe.Pointer(argPtr)),
		0, swShowNormal)
	if ret <= 32 {
		return callErr
	}
	return nil
}

// ElevatedKill 通过提权 helper 结束进程：拉起带 -kill 参数的自身，
// helper 把结果写进临时文件，这里轮询取回。无需常驻服务或 IPC。
func ElevatedKill(pid uint32, tree bool) KillResult {
	f, err := os.CreateTemp("", "portmon-kill-*.json")
	if err != nil {
		return KillResult{Code: "failed", Message: "无法创建临时文件：" + err.Error()}
	}
	path := f.Name()
	f.Close()
	os.Remove(path)
	defer os.Remove(path)

	args := "-kill -pid " + strconv.FormatUint(uint64(pid), 10) + " -result " + path
	if tree {
		args = "-kill -pid " + strconv.FormatUint(uint64(pid), 10) + " -tree -result " + path
	}
	if err := RunElevated(args); err != nil {
		return KillResult{
			Code:    "denied",
			Message: "已取消管理员授权，进程未结束",
		}
	}

	// helper 是独立进程，UAC 确认后才会真正启动，给足等待时间
	for i := 0; i < 100; i++ {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			var res KillResult
			if json.Unmarshal(b, &res) == nil {
				return res
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return KillResult{Code: "failed", Message: "提权进程没有返回结果，请手动确认端口状态"}
}

// RunElevatedHelper 在 main 最开头调用，命中 -kill 模式则执行并返回 true。
func RunElevatedHelper(args []string) bool {
	if len(args) == 0 || args[0] != "-kill" {
		return false
	}

	var pid uint32
	tree := false
	resultPath := ""
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "-tree":
			tree = true
		case "-pid":
			if i+1 < len(args) {
				if v, err := strconv.ParseUint(args[i+1], 10, 32); err == nil {
					pid = uint32(v)
				}
				i++
			}
		case "-result":
			if i+1 < len(args) {
				resultPath = args[i+1]
				i++
			}
		}
	}

	if pid == 0 || resultPath == "" {
		return true
	}

	res := Kill(pid, tree)
	if b, err := json.Marshal(res); err == nil {
		os.WriteFile(resultPath, b, 0o644)
	}
	return true
}
