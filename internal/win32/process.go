package win32

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")
	modNtdll    = windows.NewLazySystemDLL("ntdll.dll")

	procIsProcessCritical         = modKernel32.NewProc("IsProcessCritical")
	procK32GetProcessMemoryInfo   = modKernel32.NewProc("K32GetProcessMemoryInfo")
	procNtQueryInformationProcess = modNtdll.NewProc("NtQueryInformationProcess")
)

const (
	processTerminate               = 0x0001
	processQueryLimitedInformation = 0x1000

	processCommandLineInformation = 60
)

// ProcBasic 来自 Toolhelp 快照，不需要进程句柄，任何权限都能读到。
type ProcBasic struct {
	PID  uint32
	PPID uint32
	Name string
}

// ProcTable 一次性抓取全部进程的 PID/父 PID/映像名。
func ProcTable() map[uint32]ProcBasic {
	out := make(map[uint32]ProcBasic, 320)
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return out
	}
	defer windows.CloseHandle(snap)

	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	if err := windows.Process32First(snap, &e); err != nil {
		return out
	}
	for {
		out[e.ProcessID] = ProcBasic{
			PID:  e.ProcessID,
			PPID: e.ParentProcessID,
			Name: windows.UTF16ToString(e.ExeFile[:]),
		}
		if err := windows.Process32Next(snap, &e); err != nil {
			break
		}
	}
	return out
}

// ProcDetail 进程详情。非管理员打开其他用户的进程会失败，此时 AccessDenied=true，
// 只回填 Toolhelp 能拿到的字段。
type ProcDetail struct {
	PID          uint32 `json:"pid"`
	Name         string `json:"name"`
	ExePath      string `json:"exePath"`
	CmdLine      string `json:"cmdLine"`
	ParentPID    uint32 `json:"parentPid"`
	ParentName   string `json:"parentName"`
	Owner        string `json:"owner"`
	SessionID    uint32 `json:"sessionId"`
	StartedAt    int64  `json:"startedAt"`
	WorkingSet   uint64 `json:"workingSet"`
	PrivateBytes uint64 `json:"privateBytes"`
	Critical     bool   `json:"critical"`
	AccessDenied bool   `json:"accessDenied"`
}

type unicodeString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        *uint16
}

type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

// Detailed 拉取指定 PID 的完整信息。
func Detailed(pid uint32, procs map[uint32]ProcBasic) ProcDetail {
	d := ProcDetail{PID: pid}
	if b, ok := procs[pid]; ok {
		d.Name = b.Name
		d.ParentPID = b.PPID
		d.ParentName = procs[b.PPID].Name
	}
	if pid == 0 || pid == 4 {
		d.Owner = "NT AUTHORITY\\SYSTEM"
		d.Critical = true
		return d
	}

	var sid uint32
	if err := windows.ProcessIdToSessionId(pid, &sid); err == nil {
		d.SessionID = sid
	}

	h, err := windows.OpenProcess(processQueryLimitedInformation, false, pid)
	if err != nil {
		d.AccessDenied = true
		return d
	}
	defer windows.CloseHandle(h)

	d.ExePath = imagePath(h)
	d.CmdLine = commandLine(h)
	d.Owner = tokenUser(h)
	d.Critical = IsCritical(h)

	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err == nil {
		d.StartedAt = created.Nanoseconds() / 1e6
	}

	var mc processMemoryCounters
	mc.CB = uint32(unsafe.Sizeof(mc))
	r, _, _ := procK32GetProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&mc)), uintptr(mc.CB))
	if r != 0 {
		d.WorkingSet = uint64(mc.WorkingSetSize)
		d.PrivateBytes = uint64(mc.PagefileUsage)
	}
	return d
}

// IsCritical 包装 IsProcessCritical，需要 PROCESS_QUERY_LIMITED_INFORMATION 句柄。
func IsCritical(h windows.Handle) bool {
	if procIsProcessCritical.Find() != nil {
		return false
	}
	var critical int32
	r, _, _ := procIsProcessCritical.Call(uintptr(h), uintptr(unsafe.Pointer(&critical)))
	return r != 0 && critical != 0
}

// CriticalPID 自己开句柄判定，用于列表渲染。打不开的进程一律返回 false。
func CriticalPID(pid uint32) bool {
	if pid == 0 || pid == 4 {
		return true
	}
	h, err := windows.OpenProcess(processQueryLimitedInformation, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	return IsCritical(h)
}

func imagePath(h windows.Handle) string {
	buf := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		if size <= uint32(len(buf)) {
			return ""
		}
		buf = make([]uint16, size)
		if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
			return ""
		}
	}
	if size == 0 {
		return ""
	}
	return windows.UTF16ToString(buf[:size])
}

// commandLine 走 NtQueryInformationProcess(ProcessCommandLineInformation)，
// 比读 PEB 简单，也不需要 PROCESS_VM_READ。
func commandLine(h windows.Handle) string {
	if procNtQueryInformationProcess.Find() != nil {
		return ""
	}
	var need uint32
	procNtQueryInformationProcess.Call(uintptr(h), processCommandLineInformation, 0, 0,
		uintptr(unsafe.Pointer(&need)))
	if need < uint32(unsafe.Sizeof(unicodeString{})) {
		return ""
	}
	buf := make([]byte, need)
	var got uint32
	st, _, _ := procNtQueryInformationProcess.Call(uintptr(h), processCommandLineInformation,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(need), uintptr(unsafe.Pointer(&got)))
	if int32(st) < 0 {
		return ""
	}
	us := (*unicodeString)(unsafe.Pointer(&buf[0]))
	if us.Buffer == nil || us.Length < 2 {
		return ""
	}
	return windows.UTF16ToString(unsafe.Slice(us.Buffer, int(us.Length)/2))
}

func tokenUser(h windows.Handle) string {
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		return ""
	}
	defer tok.Close()

	u, err := tok.GetTokenUser()
	if err != nil || u.User.Sid == nil {
		return ""
	}
	account, domain, _, err := u.User.Sid.LookupAccount("")
	if err != nil {
		return u.User.Sid.String()
	}
	if domain == "" {
		return account
	}
	return domain + "\\" + account
}
