package stats

import (
	"path/filepath"
	"testing"
)

// newTestCollector 建一个写到临时目录的 Collector,避免污染工作区。
func newTestCollector(t *testing.T) *Collector {
	t.Helper()
	c, err := New("test", filepath.Join(t.TempDir(), "robot-stats.jsonl"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// ObserveErr 累加 RPCErrors 并按 op 归因;不触碰 DrainCanceled。
func TestObserveErr_CountsAndAttributes(t *testing.T) {
	c := newTestCollector(t)
	c.ObserveErr("match.StartMatch")
	c.ObserveErr("match.StartMatch")
	c.ObserveErr("team.SetReady")

	if got := c.Counters.RPCErrors.Load(); got != 3 {
		t.Fatalf("RPCErrors=%d, want 3", got)
	}
	if got := c.Counters.DrainCanceled.Load(); got != 0 {
		t.Fatalf("DrainCanceled=%d, want 0", got)
	}
	bd := c.ErrorBreakdown()
	if bd["match.StartMatch"] != 2 || bd["team.SetReady"] != 1 {
		t.Fatalf("ErrorBreakdown=%v, want StartMatch=2 SetReady=1", bd)
	}
	if len(c.DrainBreakdown()) != 0 {
		t.Fatalf("DrainBreakdown 应为空, got %v", c.DrainBreakdown())
	}
}

// ObserveDrain 累加 DrainCanceled 并按 op 归因;刻意不进 RPCErrors,
// 这是「关停排空 canceled 不污染 error 指标」的核心保证。
func TestObserveDrain_SeparateFromErrors(t *testing.T) {
	c := newTestCollector(t)
	c.ObserveErr("match.ConfirmMatch") // 1 个真实错误
	c.ObserveDrain("match.GetMatchProgress")
	c.ObserveDrain("match.GetMatchProgress")
	c.ObserveDrain("match.ConfirmMatch")

	if got := c.Counters.RPCErrors.Load(); got != 1 {
		t.Fatalf("RPCErrors=%d, want 1(drain 不得混入)", got)
	}
	if got := c.Counters.DrainCanceled.Load(); got != 3 {
		t.Fatalf("DrainCanceled=%d, want 3", got)
	}
	drain := c.DrainBreakdown()
	if drain["match.GetMatchProgress"] != 2 || drain["match.ConfirmMatch"] != 1 {
		t.Fatalf("DrainBreakdown=%v, want GetMatchProgress=2 ConfirmMatch=1", drain)
	}
	if c.ErrorBreakdown()["match.ConfirmMatch"] != 1 {
		t.Fatalf("ErrorBreakdown ConfirmMatch=%v, want 1", c.ErrorBreakdown()["match.ConfirmMatch"])
	}
}

// 无错误时两个 breakdown 都返回空 map(收尾打印走 total=0 分支)。
func TestBreakdown_EmptyByDefault(t *testing.T) {
	c := newTestCollector(t)
	if len(c.ErrorBreakdown()) != 0 || len(c.DrainBreakdown()) != 0 {
		t.Fatalf("默认应为空 breakdown")
	}
}
