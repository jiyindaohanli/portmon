package monitor

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"portmon/internal/win32"
	"portmon/internal/wsl"
)

// Entry 列表一行：一个监听中的端口（或一个 UDP 绑定）。
type Entry struct {
	Key      string `json:"key"`
	Proto    string `json:"proto"`
	Addr     string `json:"addr"`
	Port     int    `json:"port"`
	Remote   string `json:"remote"`
	State    string `json:"state"`
	PID      uint32 `json:"pid"`
	Proc     string `json:"proc"`
	Guard    string `json:"guard"`
	GuardMsg string `json:"guardMsg"`
	Conns    int    `json:"conns"`
	// Source 是 win 或 wsl。wsl 行没有宿主 PID，不能从宿主机结束。
	Source string `json:"source"`
	Detail string `json:"detail"`
}

// Snapshot 一次完整视图。只有内容真的变了才会推给前端。
type Snapshot struct {
	Seq      int64   `json:"seq"`
	At       int64   `json:"at"`
	Entries  []Entry `json:"entries"`
	Listen   int     `json:"listen"`
	Conns    int     `json:"conns"`
	Procs    int     `json:"procs"`
	Paused   bool    `json:"paused"`
	Elevated bool    `json:"elevated"`
	WSL      int     `json:"wsl"`
	Ms       int64   `json:"ms"`
}

// Poller 自适应轮询：窗口聚焦时快，失焦/托盘时慢，省 CPU。
type Poller struct {
	mu       sync.RWMutex
	procs    map[uint32]win32.ProcBasic
	procsAt  time.Time
	sig      string
	snapshot Snapshot
	paused   bool
	fast     bool
	wslProbe *wsl.Prober
}

func New() *Poller {
	return &Poller{fast: true, wslProbe: wsl.NewProber()}
}

// Interval 当前采样间隔。
func (p *Poller) Interval() time.Duration {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.paused {
		return 2 * time.Second
	}
	if p.fast {
		return 250 * time.Millisecond
	}
	return 1500 * time.Millisecond
}

func (p *Poller) SetFast(fast bool) {
	p.mu.Lock()
	p.fast = fast
	p.mu.Unlock()
}

func (p *Poller) SetPaused(paused bool) {
	p.mu.Lock()
	p.paused = paused
	p.mu.Unlock()
}

func (p *Poller) Paused() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.paused
}

// Current 返回最近一次快照的副本，供前端首次加载。
func (p *Poller) Current() Snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.snapshot
}

// Loop 持续采样，内容变化时回调。签名不变就不回调，避免前端空转。
func (p *Poller) Loop(onChange func(Snapshot), onErr func(error)) {
	// WSL 探测必须独立跑：单次 wsl.exe 150~400ms，塞进高频采样会直接拖垮刷新
	go p.wslProbe.Run(p.wslRunning, 8*time.Second)

	for {
		if p.Paused() {
			time.Sleep(p.Interval())
			continue
		}
		snap, sig, err := p.collect()
		if err != nil {
			if onErr != nil {
				onErr(err)
			}
			time.Sleep(time.Second)
			continue
		}

		p.mu.Lock()
		changed := sig != p.sig
		p.sig = sig
		p.snapshot = snap
		paused := p.paused
		p.mu.Unlock()

		if changed {
			snap.Paused = paused
			onChange(snap)
		}
		time.Sleep(p.Interval())
	}
}

func (p *Poller) collect() (Snapshot, string, error) {
	start := time.Now()

	tcp, err := win32.TCPTable()
	if err != nil {
		return Snapshot{}, "", err
	}
	udp, _ := win32.UDPTable()

	all := make([]win32.Row, 0, len(tcp)+len(udp))
	all = append(all, tcp...)
	all = append(all, udp...)

	// 进程表比端口表贵，平时 2 秒刷新一次；
	// 但有新进程刚监听时它还不在表里，列表和通知的进程名会空白，所以要立刻补一次。
	p.mu.Lock()
	needRefresh := p.procs == nil || time.Since(p.procsAt) > 2*time.Second
	if !needRefresh {
		for _, r := range all {
			if r.State != "LISTEN" && r.State != "BOUND" {
				continue
			}
			if _, ok := p.procs[r.PID]; !ok && r.PID > 0 {
				needRefresh = true
				break
			}
		}
	}
	if needRefresh {
		p.procs = win32.ProcTable()
		p.procsAt = time.Now()
	}
	procs := p.procs
	p.mu.Unlock()

	conns := make(map[string]int, len(tcp))
	established := 0
	for _, r := range tcp {
		if r.State == "ESTABLISHED" {
			established++
			conns[fmt.Sprintf("%d/%d", r.Port, r.PID)]++
		}
	}

	entries := make([]Entry, 0, 160)
	listen := 0
	for _, r := range all {
		isListen := r.State == "LISTEN" || r.State == "BOUND"
		if !isListen {
			continue
		}
		listen++
		name := procs[r.PID].Name
		guard, guardMsg := win32.Guard(r.PID, name, r.Port)
		entries = append(entries, Entry{
			Key:      fmt.Sprintf("%s/%s/%d/%d", r.Proto, r.Addr, r.Port, r.PID),
			Proto:    r.Proto,
			Addr:     r.Addr,
			Port:     r.Port,
			State:    r.State,
			PID:      r.PID,
			Proc:     name,
			Guard:    guard,
			GuardMsg: guardMsg,
			Conns:    conns[fmt.Sprintf("%d/%d", r.Port, r.PID)],
			Source:   "win",
		})
	}

	// WSL2 的 localhost 转发不在宿主 TCP 表里，只能进虚拟机内部查
	var wslPorts []wsl.Port
	if p.wslRunning() {
		wslPorts = p.wslProbe.Ports()
	}
	for _, wp := range wslPorts {
		listen++
		entries = append(entries, Entry{
			Key:      fmt.Sprintf("wsl/%s/%d", wp.Addr, wp.Port),
			Proto:    "WSL",
			Addr:     wp.Addr,
			Port:     wp.Port,
			State:    "LISTEN",
			Proc:     wp.Proc,
			Guard:    win32.GuardWSL,
			GuardMsg: "WSL 内部的监听端口，宿主机上没有对应进程，请在 WSL 内处理",
			Source:   "wsl",
			Detail:   wp.Detail,
		})
	}

	// 端口升序，同端口按 PID，保证前端 diff 时顺序稳定
	sortEntries(entries)

	h := sha1.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s|%s|%s|%d|%s|%s\n", e.Key, e.Proc, e.Guard, e.Conns, e.State, e.Detail)
	}

	seq := time.Now().UnixNano()
	return Snapshot{
		Seq:      seq,
		At:       time.Now().UnixMilli(),
		Entries:  entries,
		Listen:   listen,
		Conns:    established,
		Procs:    len(procs),
		Elevated: win32.IsElevated(),
		WSL:      len(wslPorts),
		Ms:       time.Since(start).Milliseconds(),
	}, hex.EncodeToString(h.Sum(nil)), nil
}

// wslRunning 只在虚拟机真的在跑时才去探测，避免无谓地反复拉起 wsl.exe。
func (p *Poller) wslRunning() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, proc := range p.procs {
		switch proc.Name {
		case "vmmemWSL", "wslservice.exe", "vmmemWSL.exe":
			return true
		}
	}
	return false
}

func sortEntries(e []Entry) {
	// 插入排序足够：数据量 ~200，且大多已接近有序
	for i := 1; i < len(e); i++ {
		for j := i; j > 0 && less(e[j], e[j-1]); j-- {
			e[j], e[j-1] = e[j-1], e[j]
		}
	}
}

func less(a, b Entry) bool {
	if a.Port != b.Port {
		return a.Port < b.Port
	}
	if a.PID != b.PID {
		return a.PID < b.PID
	}
	return a.Proto < b.Proto
}
