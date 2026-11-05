package store

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNormalize(t *testing.T) {
	cases := []struct {
		name string
		in   []int
		want []int
	}{
		{"保持顺序", []int{3306, 6379, 315}, []int{3306, 6379, 315}},
		{"去重", []int{3306, 3306, 6379}, []int{3306, 6379}},
		{"丢弃越界", []int{0, -1, 65536, 70000, 80}, []int{80}},
		{"全非法", []int{0, -5, 99999}, []int{}},
		{"空输入", nil, []int{}},
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

	want := []int{3306, 6379, 16389}
	if err := Save(Config{WatchPorts: want}); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}
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
