package store

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// isolate 把配置目录和“老位置”都指到临时目录：不隔离的话测试会写进真 exe 旁边，
// 还可能把本机 %APPDATA%\PortMon\config.json 搬进测试目录，结果随机器变。
func isolate(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	t.Setenv(dirEnvVar, d)
	t.Setenv("AppData", t.TempDir()) // 老位置留空，migrate 不会误搬
	return d
}

func TestNormalize(t *testing.T) {
	cases := []struct {
		name string
		in   WatchList
		want WatchList
	}{
		{
			"保持顺序",
			WatchList{{Port: 3306, Notify: true}, {Port: 6379, Notify: true}, {Port: 315, Notify: true}},
			WatchList{{Port: 3306, Notify: true}, {Port: 6379, Notify: true}, {Port: 315, Notify: true}},
		},
		{
			"去重时保留第一次出现（顺序权威）",
			WatchList{{Port: 3306, Note: "先", Notify: true}, {Port: 3306, Note: "后", Notify: false}, {Port: 6379, Notify: true}},
			WatchList{{Port: 3306, Note: "先", Notify: true}, {Port: 6379, Notify: true}},
		},
		{
			"丢弃越界",
			WatchList{{Port: 0}, {Port: -1}, {Port: 65536}, {Port: 70000}, {Port: 80, Notify: true}},
			WatchList{{Port: 80, Notify: true}},
		},
		{"全非法", WatchList{{Port: 0}, {Port: -5}, {Port: 99999}}, WatchList{}},
		{"空输入", nil, WatchList{}},
		{
			"备注清洗",
			WatchList{{Port: 3306, Note: "  MySQL\t\n ", Notify: true}},
			WatchList{{Port: 3306, Note: "MySQL", Notify: true}},
		},
		{
			"备注按 rune 截断",
			WatchList{{Port: 3306, Note: strings.Repeat("测试", 20), Notify: true}},
			WatchList{{Port: 3306, Note: strings.Repeat("测试", 12), Notify: true}},
		},
	}
	for _, c := range cases {
		got := Normalize(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: Normalize(%v) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	isolate(t)

	// 文件不存在时应该拿到空配置而不是报错
	if got := Load().WatchPorts; len(got) != 0 {
		t.Fatalf("空目录下 Load() = %v, want 空", got)
	}

	want := WatchList{
		{Port: 6379, Note: "Redis", Notify: true},
		{Port: 3306, Note: "MySQL", Notify: false},
		{Port: 16389, Notify: true},
	}
	if err := Save(Config{WatchPorts: want}); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}
	if got := Load().WatchPorts; !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %v, want %v", got, want)
	}
}

// 老版本写的是 [3306, 6379]，升级后必须原样读出来并且通知默认开着。
func TestLoadLegacyPortArray(t *testing.T) {
	isolate(t)

	p, err := path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"watchPorts":[3306,6379]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	want := WatchList{{Port: 3306, Notify: true}, {Port: 6379, Notify: true}}
	if got := Load().WatchPorts; !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %v, want %v", got, want)
	}
}

// 新格式里没有 notify 字段时也要默认开着，不能静默把通知关掉。
func TestLoadMissingNotifyDefaultsTrue(t *testing.T) {
	isolate(t)

	p, err := path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"watchPorts":[{"port":3306,"note":"MySQL"},{"port":6379,"notify":false}]}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	want := WatchList{{Port: 3306, Note: "MySQL", Notify: true}, {Port: 6379, Notify: false}}
	if got := Load().WatchPorts; !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %v, want %v", got, want)
	}
}

func TestLoadIgnoresBrokenFile(t *testing.T) {
	isolate(t)

	p, err := path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{ 这不是 json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := Load().WatchPorts; len(got) != 0 {
		t.Fatalf("配置损坏时应返回空配置，实际 %v", got)
	}
}

func TestNotifyPortsAndNotes(t *testing.T) {
	list := WatchList{
		{Port: 3306, Note: "MySQL", Notify: true},
		{Port: 6379, Note: "Redis", Notify: false},
		{Port: 8080, Notify: true},
	}
	if got, want := list.NotifyPorts(), []int{3306, 8080}; !reflect.DeepEqual(got, want) {
		t.Errorf("NotifyPorts() = %v, want %v", got, want)
	}
	if got, want := list.Notes(), map[int]string{3306: "MySQL", 6379: "Redis"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Notes() = %v, want %v", got, want)
	}
}

// 没设 PORTMON_CONFIG_DIR 时，配置默认落在 exe 旁边（绿色版：拷走整个目录就行）。
func TestDirPrefersExeDir(t *testing.T) {
	t.Setenv(dirEnvVar, "")

	exe, err := os.Executable()
	if err != nil {
		t.Skipf("拿不到 exe 路径: %v", err)
	}
	want := filepath.Dir(exe)
	got, err := Dir()
	if err != nil {
		t.Fatalf("Dir() 报错: %v", err)
	}
	if got != want {
		t.Fatalf("Dir() = %v, want %v", got, want)
	}
}

// 老用户的配置在 %APPDATA%\PortMon 里，换成“跟 exe 走”之后必须自动搬过来。
func TestMigrateFromLegacyAppData(t *testing.T) {
	newDir := t.TempDir()
	legacyRoot := t.TempDir()
	t.Setenv(dirEnvVar, newDir)
	t.Setenv("AppData", legacyRoot)

	legacy := filepath.Join(legacyRoot, appDir, fileName)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"watchPorts":[{"port":5173,"note":"cms","notify":false}]}`
	if err := os.WriteFile(legacy, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	want := WatchList{{Port: 5173, Note: "cms", Notify: false}}
	if got := Load().WatchPorts; !reflect.DeepEqual(got, want) {
		t.Fatalf("迁移后 Load() = %v, want %v", got, want)
	}
	if _, err := os.Stat(filepath.Join(newDir, fileName)); err != nil {
		t.Fatalf("配置没有搬到新位置: %v", err)
	}
	// 老文件保留：回退老版本还能用
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("老配置不该被删: %v", err)
	}
}

// 老文件是坏的，不要把它搬到新位置把新位置也弄脏。
func TestMigrateSkipsBrokenLegacy(t *testing.T) {
	newDir := t.TempDir()
	legacyRoot := t.TempDir()
	t.Setenv(dirEnvVar, newDir)
	t.Setenv("AppData", legacyRoot)

	legacy := filepath.Join(legacyRoot, appDir, fileName)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("{ 这不是 json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := Load().WatchPorts; len(got) != 0 {
		t.Fatalf("坏配置不该被搬过来，实际 %v", got)
	}
	if _, err := os.Stat(filepath.Join(newDir, fileName)); err == nil {
		t.Fatalf("坏配置被写到了新位置")
	}
}
