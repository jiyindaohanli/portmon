// Package store 负责 %APPDATA%\PortMon\config.json 的读写。
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config 目前只存盯守端口。字段少就不做通用的 KV 抽象。
type Config struct {
	WatchPorts []int `json:"watchPorts"`
}

func path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "PortMon", "config.json"), nil
}

// Load 读取配置。文件不存在或损坏都返回空配置，不报错——配置坏了不该让应用起不来。
func Load() Config {
	var cfg Config
	p, err := path()
	if err != nil {
		return cfg
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(b, &cfg)
	return cfg
}

// Normalize 丢掉越界与重复的端口，保持输入顺序。
func Normalize(ports []int) []int {
	seen := make(map[int]bool, len(ports))
	out := make([]int, 0, len(ports))
	for _, p := range ports {
		if p < 1 || p > 65535 || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func Save(cfg Config) error {
	p, err := path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// 先写临时文件再 rename：避免写到一半断电/被杀，留下半个 JSON
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
