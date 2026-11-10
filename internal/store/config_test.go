package store

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

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
	t.Setenv("AppData", t.TempDir())

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
	t.Setenv("AppData", t.TempDir())

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
	t.Setenv("AppData", t.TempDir())

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
	t.Setenv("AppData", t.TempDir())

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
