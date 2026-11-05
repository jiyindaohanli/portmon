package win32

import (
	"fmt"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// KillResult 杀进程的统一结果，前端按 code 决定怎么提示。
// code 取值：ok / blocked / denied / gone / failed / still_alive
type KillResult struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code"`
	Message string `json:"message"`
	// NeedAdmin 表示换管理员权限重试有可能成功
	NeedAdmin bool `json:"needAdmin"`
	// Freed 表示结束后该进程的端口确实释放了
	Freed bool `json:"freed"`
	// Killed 实际被结束的 PID
	Killed []uint32 `json:"killed"`
}

// Kill 结束单个进程或整棵进程树。guard 仍会再校验一次，即使是从提权 helper 进来。
func Kill(pid uint32, tree bool) KillResult {
	procs := ProcTable()
	name := procs[pid].Name
	guard, msg := Guard(pid, name, 0)
	if !Killable(guard) {
		return KillResult{Code: "blocked", Message: msg}
	}

	targets := []uint32{pid}
	if tree {
		targets = append(targets, descendants(procs, pid)...)
	}

	// 结束前先记下这些进程正在监听的端口。进程一死它的行就没了，
	// 只查 PID 等于没查；必须查这些端口是不是真的空出来了。
	watch := listeningPorts(targets)

	var killed []uint32
	var firstErr error
	var denied, gone bool

	// 子进程先死，避免父进程重启它们
	for i := len(targets) - 1; i >= 0; i-- {
		err := terminate(targets[i])
		switch {
		case err == nil:
			killed = append(killed, targets[i])
		case err == windows.ERROR_ACCESS_DENIED:
			denied = true
			if firstErr == nil {
				firstErr = err
			}
		case err == windows.ERROR_INVALID_PARAMETER:
			gone = true
		default:
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	switch {
	case len(killed) == 0 && denied:
		return KillResult{
			Code: "denied", NeedAdmin: true, Killed: killed,
			Message: "权限不足，该进程属于其他用户或受系统保护。可以尝试以管理员身份重试",
		}
	case len(killed) == 0 && gone:
		return KillResult{Code: "gone", Message: "进程已经不存在了，可能是刚刚自己退出的"}
	case len(killed) == 0:
		return KillResult{Code: "failed", Message: errorText(firstErr), Killed: killed}
	}

	// 内核清理套接字有几毫秒到几百毫秒的延迟，必须回查，不能乐观返回
	freed, left := waitPortsReleased(watch, 2500*time.Millisecond)
	res := KillResult{Code: "ok", OK: true, Freed: freed, Killed: killed}
	res.Message = fmt.Sprintf("已结束 %d 个进程", len(killed))
	if !freed {
		res.Message += "，但端口仍被占用：" + strings.Join(left, ", ")
	}
	return res
}

func terminate(pid uint32) error {
	h, err := windows.OpenProcess(processTerminate|processQueryLimitedInformation, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}

// descendants 广度优先收集所有子孙 PID。
func descendants(procs map[uint32]ProcBasic, pid uint32) []uint32 {
	children := make(map[uint32][]uint32, len(procs))
	for _, p := range procs {
		children[p.PPID] = append(children[p.PPID], p.PID)
	}
	var out []uint32
	queue := []uint32{pid}
	seen := map[uint32]bool{pid: true}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, c := range children[cur] {
			if seen[c] {
				continue
			}
			seen[c] = true
			out = append(out, c)
			queue = append(queue, c)
		}
	}
	return out
}

// listeningPorts 列出这些 PID 当前正在监听的地址:端口。
func listeningPorts(pids []uint32) []string {
	if len(pids) == 0 {
		return nil
	}
	watch := make(map[uint32]bool, len(pids))
	for _, p := range pids {
		watch[p] = true
	}
	rows, err := TCPTable()
	if err != nil {
		return nil
	}
	var out []string
	for _, r := range rows {
		if watch[r.PID] && r.State == "LISTEN" {
			out = append(out, fmt.Sprintf("%s:%d", r.Addr, r.Port))
		}
	}
	return dedupe(out)
}

// waitPortsReleased 轮询端口表，直到这些地址:端口都不再有任何进程监听。
// 只看“端口有没有被释放”，不管是哪个进程接手的。
func waitPortsReleased(ports []string, timeout time.Duration) (bool, []string) {
	if len(ports) == 0 {
		return true, nil
	}
	deadline := time.Now().Add(timeout)

	for {
		rows, err := TCPTable()
		if err == nil {
			live := make(map[string]bool, len(ports))
			for _, r := range rows {
				if r.State == "LISTEN" {
					live[fmt.Sprintf("%s:%d", r.Addr, r.Port)] = true
				}
			}
			var left []string
			for _, p := range ports {
				if live[p] {
					left = append(left, p)
				}
			}
			if len(left) == 0 {
				return true, nil
			}
			if time.Now().After(deadline) {
				return false, left
			}
		} else if time.Now().After(deadline) {
			return false, ports
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func errorText(err error) string {
	if err == nil {
		return "结束失败，原因未知"
	}
	if errno, ok := err.(windows.Errno); ok {
		switch errno {
		case windows.ERROR_ACCESS_DENIED:
			return "权限不足"
		case windows.ERROR_INVALID_PARAMETER:
			return "进程已退出"
		}
		return fmt.Sprintf("结束失败：%v", errno)
	}
	return fmt.Sprintf("结束失败：%v", err)
}
