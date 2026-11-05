package monitor

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"portmon/internal/win32"
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
}

func New() *Poller {
	return &Poller{fast: true}
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

	// 进程表比端口表贵，2 秒刷新一次足够
	p.mu.Lock()
	if time.Since(p.procsAt) > 2*time.Second || p.procs == nil {
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

	all := make([]win32.Row, 0, len(tcp)+len(udp))
	all = append(all, tcp...)
	all = append(all, udp...)

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
		})
	}

	// 端口升序，同端口按 PID，保证前端 diff 时顺序稳定
	sortEntries(entries)

	h := sha1.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s|%s|%s|%d|%s\n", e.Key, e.Proc, e.Guard, e.Conns, e.State)
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
		Ms:       time.Since(start).Milliseconds(),
	}, hex.EncodeToString(h.Sum(nil)), nil
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
