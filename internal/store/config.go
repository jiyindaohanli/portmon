// Package store 负责 config.json 的读写。
//
// 配置文件跟着 exe 走（绿色版）：优先放在 exe 所在目录，其次看 PORTMON_CONFIG_DIR，
// 都不可写（比如装在 Program Files）时才回落到 %APPDATA%\PortMon。
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

const (
	// MaxNoteRunes 备注长度上限。前端输入框也限制成同一个数字，两边不许对不上。
	MaxNoteRunes = 24

	fileName  = "config.json"
	dirEnvVar = "PORTMON_CONFIG_DIR"
	appDir    = "PortMon"
)

// WatchPort 一条盯守项：端口 + 备注 + 是否参与系统通知。
type WatchPort struct {
	Port   int    `json:"port"`
	Note   string `json:"note,omitempty"`
	Notify bool   `json:"notify"`
}

// WatchList 盯守列表。**数组顺序就是界面顺序**，拖动排序靠它持久化。
type WatchList []WatchPort

// Config 目前只存盯守端口。字段少就不做通用的 KV 抽象。
type Config struct {
	WatchPorts WatchList `json:"watchPorts"`
}

// UnmarshalJSON 自己吃下旧格式。
//
// 老版本的 config.json 是纯端口数组（[3306, 6379]），新版是对象数组。
// 逐项尝试解析，于是两种格式都能读，用户升级一次不用手工迁移。
func (l *WatchList) UnmarshalJSON(b []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		// 不是数组（null / 类型损坏）→ 当成空列表，配置坏了不该让应用起不来
		return nil
	}

	out := make(WatchList, 0, len(raw))
	for _, item := range raw {
		var port int
		if err := json.Unmarshal(item, &port); err == nil {
			out = append(out, WatchPort{Port: port, Notify: true})
			continue
		}
		var wp WatchPort
		if err := wp.UnmarshalJSON(item); err != nil {
			continue
		}
		out = append(out, wp)
	}
	*l = out
	return nil
}

// UnmarshalJSON 让缺省的 notify 默认为 true——老配置里没有这个字段，
// 不能因为解析成 false 就把用户已有的通知悄悄关掉。
func (w *WatchPort) UnmarshalJSON(b []byte) error {
	var raw struct {
		Port   int    `json:"port"`
		Note   string `json:"note"`
		Notify *bool  `json:"notify"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	notify := true
	if raw.Notify != nil {
		notify = *raw.Notify
	}
	*w = WatchPort{Port: raw.Port, Note: raw.Note, Notify: notify}
	return nil
}

// Dir 配置目录：PORTMON_CONFIG_DIR → exe 所在目录 → %APPDATA%\PortMon。
// 不缓存：每次调用最多探一次目录可写性，比“路径一变测试就互相污染”划算。
func Dir() (string, error) {
	if v := strings.TrimSpace(os.Getenv(dirEnvVar)); v != "" {
		return v, nil
	}

	if exe, err := os.Executable(); err == nil {
		d := filepath.Dir(exe)
		// 已经有配置文件就直接用，不用每次发探测
		if _, err := os.Stat(filepath.Join(d, fileName)); err == nil || writable(d) {
			return d, nil
		}
	}

	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, appDir), nil
}

// writable 用“试着建一个临时文件”来判，比看权限位靠得住。
func writable(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".portmon-write-test")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

func path() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, fileName), nil
}

// legacyPath 旧版本固定把配置放在 %APPDATA%\PortMon。
func legacyPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, appDir, fileName), nil
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
		// 新位置还没有配置：把老 %APPDATA% 里的搬过来，用户升级一次不会丢盯守列表
		b = migrate(p)
		if b == nil {
			return cfg
		}
	}

	_ = json.Unmarshal(b, &cfg)
	return cfg
}

// migrate 把老位置（%APPDATA%\PortMon\config.json）的配置搬到 dst。
// 只在目标文件不存在、且源文件确实是一份能解析的配置时搬；老文件保留不删，回退老版本还能用。
func migrate(dst string) []byte {
	src, err := legacyPath()
	if err != nil || src == dst {
		return nil
	}

	b, err := os.ReadFile(src)
	if err != nil || len(b) == 0 {
		return nil
	}

	var probe Config
	if err := json.Unmarshal(b, &probe); err != nil {
		return nil // 坏的配置不要搬过去把好位置也污染了
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		return nil
	}
	return b
}

// Normalize 丢掉越界与重复的端口，保持输入顺序（顺序即界面顺序），顺带清洗备注。
func Normalize(list WatchList) WatchList {
	seen := make(map[int]bool, len(list))
	out := make(WatchList, 0, len(list))
	for _, w := range list {
		if w.Port < 1 || w.Port > 65535 || seen[w.Port] {
			continue
		}
		seen[w.Port] = true
		w.Note = cleanNote(w.Note)
		out = append(out, w)
	}
	return out
}

// cleanNote 去掉控制字符、首尾空白，并按 rune 截断——按字节截会切出半个汉字。
func cleanNote(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > MaxNoteRunes {
		s = string(r[:MaxNoteRunes])
	}
	return s
}

// Notes 端口 → 备注，通知文案要带名字。
func (l WatchList) Notes() map[int]string {
	if len(l) == 0 {
		return nil
	}
	out := make(map[int]string, len(l))
	for _, w := range l {
		if w.Note != "" {
			out[w.Port] = w.Note
		}
	}
	return out
}

// NotifyPorts 只取参与系统通知的端口，关掉通知的端口不进 tracker。
func (l WatchList) NotifyPorts() []int {
	out := make([]int, 0, len(l))
	for _, w := range l {
		if w.Notify {
			out = append(out, w.Port)
		}
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
