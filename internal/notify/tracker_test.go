package notify

import (
	"reflect"
	"testing"
)

func TestDiffPrimesSilently(t *testing.T) {
	tr := NewTracker()
	// 首次出现只建基线：启动时端口已被占用不应该弹通知
	got := tr.Diff([]int{5173}, map[int]Owner{5173: {Proc: "node.exe", PID: 100}})
	if len(got) != 0 {
		t.Fatalf("首次 Diff 不应产生事件，实际 %v", got)
	}
}

func TestDiffDetectsTakeAndRelease(t *testing.T) {
	tr := NewTracker()
	tr.Diff([]int{5173}, nil) // 基线：空闲

	got := tr.Diff([]int{5173}, map[int]Owner{5173: {Proc: "node.exe", PID: 100}})
	if len(got) != 1 || !got[0].InUse || got[0].Port != 5173 {
		t.Fatalf("应检测到被占用，实际 %v", got)
	}
	if got[0].Owner.Proc != "node.exe" {
		t.Fatalf("占用者信息丢失: %v", got[0].Owner)
	}

	got = tr.Diff([]int{5173}, nil)
	if len(got) != 1 || got[0].InUse {
		t.Fatalf("应检测到被释放，实际 %v", got)
	}

	// 状态没变就不该再报
	if got := tr.Diff([]int{5173}, nil); len(got) != 0 {
		t.Fatalf("状态未变不应产生事件，实际 %v", got)
	}
}

func TestDiffMultiplePorts(t *testing.T) {
	tr := NewTracker()
	tr.Diff([]int{5173, 3306, 8080}, nil)

	got := tr.Diff([]int{5173, 3306, 8080}, map[int]Owner{
		3306: {Proc: "mysqld.exe", PID: 7},
	})
	if len(got) != 1 || got[0].Port != 3306 {
		t.Fatalf("只应报 3306，实际 %v", got)
	}
}

func TestDiffDropsRemovedPorts(t *testing.T) {
	tr := NewTracker()
	tr.Diff([]int{5173}, nil)

	// 取消盯守后再加回来，应该重新静默建基线，而不是和旧状态比对
	tr.Diff([]int{}, nil)
	if got := tr.Diff([]int{5173}, map[int]Owner{5173: {Proc: "node.exe"}}); len(got) != 0 {
		t.Fatalf("重新加入盯守应静默建基线，实际 %v", got)
	}
	if len(tr.state) != 1 {
		t.Fatalf("state 应只剩 1 个端口，实际 %v", tr.state)
	}
}

func TestReset(t *testing.T) {
	tr := NewTracker()
	tr.Diff([]int{5173}, map[int]Owner{5173: {Proc: "node.exe", PID: 1}})
	tr.Reset()
	if !reflect.DeepEqual(tr.state, map[int]bool{}) {
		t.Fatalf("Reset 后应清空，实际 %v", tr.state)
	}
}
