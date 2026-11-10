package main

import (
	"reflect"
	"testing"

	"portmon/internal/store"
)

func TestAppWatchPorts(t *testing.T) {
	t.Setenv("AppData", t.TempDir())
	a := NewApp()

	// 越界和重复必须在这里就被丢掉，前端不该为它兜底
	in := store.WatchList{
		{Port: 80, Notify: true},
		{Port: 80, Note: "重复项", Notify: true},
		{Port: 0, Notify: true},
		{Port: 70000, Notify: true},
		{Port: 3000, Note: "  前端  ", Notify: false},
	}
	if err := a.SetWatchPorts(in); err != nil {
		t.Fatalf("SetWatchPorts 失败: %v", err)
	}

	want := store.WatchList{
		{Port: 80, Notify: true},
		{Port: 3000, Note: "前端", Notify: false},
	}
	if got := a.GetWatchPorts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("GetWatchPorts() = %v, want %v", got, want)
	}

	// 顺序必须原样保留——拖动排序就是靠这个落盘的
	reordered := store.WatchList{want[1], want[0]}
	if err := a.SetWatchPorts(reordered); err != nil {
		t.Fatalf("SetWatchPorts 失败: %v", err)
	}
	if got := a.GetWatchPorts(); !reflect.DeepEqual(got, reordered) {
		t.Fatalf("排序没保住：GetWatchPorts() = %v, want %v", got, reordered)
	}

	// 清空之后必须是空列表，而不是 nil 报错
	if err := a.SetWatchPorts(nil); err != nil {
		t.Fatalf("清空失败: %v", err)
	}
	if got := a.GetWatchPorts(); len(got) != 0 {
		t.Fatalf("清空后 GetWatchPorts() = %v, want 空", got)
	}
}
