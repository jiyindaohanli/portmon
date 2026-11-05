// Package sysinfo 采样整机 CPU / 内存 / 磁盘占用。
// 和端口监控互不相干，所以单独一个采样器，不走 ports:snapshot 那条高频链路。
package sysinfo

import (
	"math"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modKernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemTimes       = modKernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx = modKernel32.NewProc("GlobalMemoryStatusEx")
)

// DRIVE_FIXED，只统计本地固定磁盘，跳过 U 盘/光驱/网络盘
const driveFixed = 3

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// Disk 单个盘符的占用。
type Disk struct {
	Drive   string  `json:"drive"`
	Total   uint64  `json:"total"`
	Free    uint64  `json:"free"`
	Percent float64 `json:"percent"`
}

// Stats 一次整机快照。
type Stats struct {
	At          int64   `json:"at"`
	CPU         float64 `json:"cpu"`
	MemPercent  float64 `json:"memPercent"`
	MemTotal    uint64  `json:"memTotal"`
	MemAvail    uint64  `json:"memAvail"`
	DiskPercent float64 `json:"diskPercent"`
	Disks       []Disk  `json:"disks"`
}

// Sampler 周期采样。CPU 是两次采样的差值，所以第一次采样只用来建立基准。
type Sampler struct {
	mu    sync.RWMutex
	stats Stats

	prevIdle   atomic.Uint64
	prevKernel atomic.Uint64
	prevUser   atomic.Uint64
	primed     atomic.Bool
}

func NewSampler() *Sampler { return &Sampler{} }

// Current 返回最近一次结果，供前端首屏加载。
func (s *Sampler) Current() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stats
}

// Run 阻塞循环，每 interval 采样一次并回调。
func (s *Sampler) Run(interval time.Duration, emit func(Stats)) {
	s.sample() // 建立 CPU 基准
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		st := s.sample()
		if emit != nil {
			emit(st)
		}
	}
}

func (s *Sampler) sample() Stats {
	st := Stats{At: time.Now().UnixMilli()}

	var ms memoryStatusEx
	ms.Length = uint32(unsafe.Sizeof(ms))
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms))); r != 0 {
		st.MemPercent = float64(ms.MemoryLoad)
		st.MemTotal = ms.TotalPhys
		st.MemAvail = ms.AvailPhys
	}

	st.CPU = s.cpuPercent()
	st.Disks, st.DiskPercent = disks()

	s.mu.Lock()
	s.stats = st
	s.mu.Unlock()
	return st
}

// cpuPercent GetSystemTimes 的 kernel 时间已经包含 idle，所以忙碌量 = (kernel+user) - idle。
func (s *Sampler) cpuPercent() float64 {
	var idle, kernel, user windows.Filetime
	r, _, _ := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if r == 0 {
		return 0
	}

	curIdle := filetime64(idle)
	curKernel := filetime64(kernel)
	curUser := filetime64(user)

	if !s.primed.Load() {
		s.prevIdle.Store(curIdle)
		s.prevKernel.Store(curKernel)
		s.prevUser.Store(curUser)
		s.primed.Store(true)
		return 0
	}

	dIdle := float64(curIdle - s.prevIdle.Load())
	dTotal := float64((curKernel - s.prevKernel.Load()) + (curUser - s.prevUser.Load()))

	s.prevIdle.Store(curIdle)
	s.prevKernel.Store(curKernel)
	s.prevUser.Store(curUser)

	if dTotal <= 0 {
		return 0
	}
	return round1(min((dTotal-dIdle)/dTotal*100, 100))
}

func disks() ([]Disk, float64) {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil, 0
	}

	var out []Disk
	var totalAll, freeAll uint64

	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		letter := string(rune('A' + i))
		root, err := windows.UTF16PtrFromString(letter + ":\\")
		if err != nil {
			continue
		}
		if windows.GetDriveType(root) != driveFixed {
			continue
		}

		var freeToCaller, total, totalFree uint64
		if err := windows.GetDiskFreeSpaceEx(root, &freeToCaller, &total, &totalFree); err != nil {
			continue
		}
		if total == 0 {
			continue
		}

		out = append(out, Disk{
			Drive:   letter + ":",
			Total:   total,
			Free:    totalFree,
			Percent: round1(float64(total-totalFree) / float64(total) * 100),
		})
		totalAll += total
		freeAll += totalFree
	}

	if totalAll == 0 {
		return out, 0
	}
	return out, round1(float64(totalAll-freeAll) / float64(totalAll) * 100)
}

func filetime64(f windows.Filetime) uint64 {
	return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime)
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
