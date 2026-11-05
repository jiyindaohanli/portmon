package main

import (
	"reflect"
	"testing"
)

func TestAppWatchPorts(t *testing.T) {
	t.Setenv("AppData", t.TempDir())
	a := NewApp()

	// 越界和重复必须在这里就被丢掉，前端不该为它兜底
	if err := a.SetWatchPorts([]int{80, 80, 0, 70000, 3000}); err != nil {
		t.Fatalf("SetWatchPorts 失败: %v", err)
	}
	if got, want := a.GetWatchPorts(), []int{80, 3000}; !reflect.DeepEqual(got, want) {
		t.Fatalf("GetWatchPorts() = %v, want %v", got, want)
	}

	// 清空之后必须是空列表，而不是 nil 报错
	if err := a.SetWatchPorts(nil); err != nil {
		t.Fatalf("清空失败: %v", err)
	}
	if got := a.GetWatchPorts(); len(got) != 0 {
		t.Fatalf("清空后 GetWatchPorts() = %v, want 空", got)
	}
}
