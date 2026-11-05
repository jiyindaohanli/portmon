// Package wsl 桥接 WSL2 内部的监听端口。
//
// 为什么要单独做：WSL2 的 localhost 转发是内核级重定向，不会在 Windows 的 TCP 表里
// 留下 LISTEN 行——netstat / TCPView / GetExtendedTcpTable 都看不到，只能进虚拟机里查。
package wsl

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Port 是 WSL 内部的一个监听端口。
type Port struct {
	Port   int
	Addr   string
	Kind   string // docker | wsl
	Proc   string // 容器名，或 WSL 内部服务
	Detail string // 镜像 + 容器内端口
}

// 一次 exec 同时拿 ss 和 docker，避免每轮拉两个 wsl.exe。
const script = `ss -ltn 2>/dev/null; echo ---DOCKER---; docker ps --format '{{.Names}}|{{.Image}}|{{.Ports}}' 2>/dev/null`

// Prober 在后台低频刷新，Ports() 只读缓存，绝不阻塞高频采样。
type Prober struct {
	mu    sync.RWMutex
	ports []Port
	ready bool
}

func NewProber() *Prober { return &Prober{} }

// Run 后台循环。running 为 false 时跳过探测，避免没开 WSL 也去 spawn wsl.exe。
func (p *Prober) Run(running func() bool, interval time.Duration) {
	p.refresh()
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		if running() {
			p.refresh()
		}
	}
}

// Ports 返回最近一次探测结果（可能为空）。
func (p *Prober) Ports() []Port {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.ports
}

// Ready 是否成功探测过至少一次。探测失败过就不该在界面上宣称"桥接正常"。
func (p *Prober) Ready() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.ready
}

func (p *Prober) refresh() {
	ports := probe()
	p.mu.Lock()
	p.ports = ports
	p.ready = ports != nil
	p.mu.Unlock()
}

func probe() []Port {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "wsl.exe", "-e", "sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	var buf bytes.Buffer
	cmd.Stdout = &buf
	// sh -c 的退出码取自最后一条命令，docker 不存在时也会非零，
	// 但 ss 的输出依然有效，所以这里不看 err。
	_ = cmd.Run()

	out := buf.String()
	if out == "" {
		return nil
	}

	ssPart, dockerPart := out, ""
	if i := strings.Index(out, "---DOCKER---"); i >= 0 {
		ssPart = out[:i]
		dockerPart = out[i+len("---DOCKER---"):]
	}

	mapped := parseDocker(dockerPart)

	seen := make(map[string]bool)
	var ports []Port
	for _, line := range strings.Split(ssPart, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] != "LISTEN" {
			continue
		}
		addr, port := splitAddr(fields[3])
		if port <= 0 {
			continue
		}
		key := addr + ":" + strconv.Itoa(port)
		if seen[key] {
			continue
		}
		seen[key] = true

		pp := Port{Addr: addr, Port: port, Kind: "wsl", Proc: "WSL 内部服务"}
		if d, ok := mapped[port]; ok {
			pp.Kind = "docker"
			pp.Proc = d.name
			pp.Detail = d.detail
		}
		ports = append(ports, pp)
	}
	return ports
}

type dockerInfo struct {
	name   string
	detail string
}

// parseDocker 解析 "127.0.0.1:13316->3306/tcp, 33060/tcp, ..."
// 只取有 -> 的映射（即真的发布到宿主/WSL 的端口）。
func parseDocker(s string) map[int]dockerInfo {
	out := make(map[int]dockerInfo)
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 3 {
			continue
		}
		name, image, ports := parts[0], parts[1], parts[2]

		for _, seg := range strings.Split(ports, ",") {
			seg = strings.TrimSpace(seg)
			arrow := strings.Index(seg, "->")
			if arrow < 0 {
				continue
			}
			hostSide := seg[:arrow]
			containerSide := seg[arrow+2:]

			_, hostPort := splitAddr(hostSide)
			if hostPort <= 0 {
				continue
			}
			containerPort := strings.SplitN(containerSide, "/", 2)[0]

			out[hostPort] = dockerInfo{
				name:   name,
				detail: image + " · 容器内 " + containerPort,
			}
		}
	}
	return out
}

// splitAddr 处理 127.0.0.1:16389 / [::]:80 / *:80 / 127.0.0.53%lo:53 这几种写法。
func splitAddr(s string) (string, int) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", 0
	}
	addr := strings.Trim(s[:i], "[]")
	if j := strings.Index(addr, "%"); j >= 0 {
		addr = addr[:j]
	}
	if addr == "*" || addr == "" {
		addr = "0.0.0.0"
	}
	port, err := strconv.Atoi(s[i+1:])
	if err != nil {
		return "", 0
	}
	return addr, port
}
