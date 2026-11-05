// Package notify 负责盯守端口的“被占用/被释放”翻转检测。
// 只做状态比对，不关心怎么弹通知，方便单测。
package notify

import (
	"sync"
)

// Owner 占用者的摘要，仅用于通知文案。
type Owner struct {
	Proc   string
	PID    uint32
	Source string // win | wsl
}

// Transition 一个盯守端口的状态翻转。
type Transition struct {
	Port  int
	InUse bool
	Owner Owner
}

// Tracker 记录每个盯守端口上一次的占用状态。
type Tracker struct {
	mu    sync.Mutex
	state map[int]bool
}

func NewTracker() *Tracker {
	return &Tracker{state: make(map[int]bool)}
}

// Diff 比对当前占用情况，返回发生翻转的端口。
//
// 关键点：某个端口第一次出现时只建立基线、不产生事件——否则每次启动应用都会
// 把所有「当前正好被占用」的盯守端口全推一遍通知。
// 同时清掉已经不在盯守列表里的端口，避免 map 无限增长。
func (t *Tracker) Diff(ports []int, owners map[int]Owner) []Transition {
	t.mu.Lock()
	defer t.mu.Unlock()

	keep := make(map[int]bool, len(ports))
	out := make([]Transition, 0, 2)

	for _, port := range ports {
		keep[port] = true
		_, inUse := owners[port]

		prev, known := t.state[port]
		t.state[port] = inUse

		if !known || prev == inUse {
			continue
		}
		out = append(out, Transition{Port: port, InUse: inUse, Owner: owners[port]})
	}

	for port := range t.state {
		if !keep[port] {
			delete(t.state, port)
		}
	}
	return out
}

// Reset 清空全部基线，下次 Diff 会重新静默建立。
func (t *Tracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state = make(map[int]bool)
}
