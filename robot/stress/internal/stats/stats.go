// Package stats 负责压测指标的并发计数与每分钟落盘(robot-stats.jsonl)。
//
// 输出格式见 docs/design/stress-single-cell-client.md §8,是 stress_summarize.ps1
// 五段二维表的输入之一(另一来源是各服务 prom 快照)。
package stats

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Counters 是一组原子计数器,VU 各 goroutine 无锁累加。
type Counters struct {
	VUOnline        atomic.Int64
	LoginOK         atomic.Int64
	LoginFail       atomic.Int64
	SubscribeActive atomic.Int64
	MatchEnqueue    atomic.Int64
	MatchConfirmed  atomic.Int64
	MatchDispatched atomic.Int64
	BattleReported  atomic.Int64
	RPCErrors       atomic.Int64
	// DrainCanceled 统计关停排空阶段在途 RPC 被 context.Canceled 中断的次数。
	// 这不是后端错误(是压测进程主动收敛取消 ctx 造成的),单列出来,不混入 RPCErrors,
	// 避免每轮压测因关停排空凭空多记一批假 error 污染 error 指标。
	DrainCanceled atomic.Int64
}

// latencyDigest 收集 RPC 时延样本,按分钟算 p50/p99。
// 用有界蓄水池,避免几十万 VU 把内存撑爆。
type latencyDigest struct {
	mu       sync.Mutex
	samples  []float64
	capacity int
	seen     int64
}

func newLatencyDigest(capacity int) *latencyDigest {
	return &latencyDigest{capacity: capacity, samples: make([]float64, 0, capacity)}
}

// Observe 记录一次 RPC 时延(毫秒)。超过容量后按蓄水池抽样替换。
func (d *latencyDigest) Observe(ms float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen++
	if len(d.samples) < d.capacity {
		d.samples = append(d.samples, ms)
		return
	}
	// 简单蓄水池:用 seen 取模做确定性替换,足够给压测时延一个稳定近似。
	idx := int(d.seen % int64(d.capacity))
	d.samples[idx] = ms
}

// drain 取出当前样本并清空,返回 (p50, p99)。无样本返回 (0,0)。
func (d *latencyDigest) drain() (p50, p99 float64) {
	d.mu.Lock()
	s := d.samples
	d.samples = make([]float64, 0, d.capacity)
	d.seen = 0
	d.mu.Unlock()
	if len(s) == 0 {
		return 0, 0
	}
	sort.Float64s(s)
	return percentile(s, 0.50), percentile(s, 0.99)
}

func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(q * float64(len(sorted)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// Record 是写入 robot-stats.jsonl 的一行(一分钟一行)。
type Record struct {
	TS              string  `json:"ts"`
	Minute          int64   `json:"minute"`
	Machine         string  `json:"machine"`
	VUOnline        int64   `json:"vu_online"`
	LoginOK         int64   `json:"login_ok"`
	LoginFail       int64   `json:"login_fail"`
	SubscribeActive int64   `json:"subscribe_active"`
	MatchEnqueue    int64   `json:"match_enqueue"`
	MatchConfirmed  int64   `json:"match_confirmed"`
	MatchDispatched int64   `json:"match_dispatched"`
	BattleReported  int64   `json:"battle_reported"`
	RPCP50Ms        float64 `json:"rpc_p50_ms"`
	RPCP99Ms        float64 `json:"rpc_p99_ms"`
	Errors          int64   `json:"errors"`
}

// Collector 聚合计数器并按分钟把 Record 追加到 jsonl 文件。
type Collector struct {
	Counters *Counters
	latency  *latencyDigest
	machine  string
	path     string
	// errByOp 给残留 error 做调用点归因:key=op 标签(如 "match.StartMatch"),
	// value=*atomic.Int64。只统计、不改错误计数时机(RPCErrors 仍在同一处累加),
	// 目的是让真负载跑完后能定位「哪几个 RPC 贡献了残留 error」,而非靠猜状态机边界。
	errByOp sync.Map
	// drainByOp 与 errByOp 同构,但归因的是关停排空阶段的 context.Canceled(非后端错误)。
	drainByOp sync.Map
}

// New 创建 Collector;path 为输出 jsonl 路径,目录会自动创建。
func New(machine, path string) (*Collector, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	return &Collector{
		Counters: &Counters{},
		latency:  newLatencyDigest(4096),
		machine:  machine,
		path:     path,
	}, nil
}

// ObserveRPC 记录一次 RPC 时延(毫秒)。
func (c *Collector) ObserveRPC(ms float64) { c.latency.Observe(ms) }

// ObserveErr 记录一次按调用点归因的 RPC 错误:累加全局 RPCErrors 计数,
// 并按 op 标签累加分项。op 应是稳定的低基数标签(service.Method),不要塞 player_id 等高基数值。
func (c *Collector) ObserveErr(op string) {
	c.Counters.RPCErrors.Add(1)
	cnt, _ := c.errByOp.LoadOrStore(op, new(atomic.Int64))
	cnt.(*atomic.Int64).Add(1)
}

// ObserveDrain 记录一次关停排空阶段的 context.Canceled(非后端错误):累加 DrainCanceled,
// 并按 op 标签累加分项。与 ObserveErr 镜像,但刻意不进 RPCErrors,避免污染 error 指标。
func (c *Collector) ObserveDrain(op string) {
	c.Counters.DrainCanceled.Add(1)
	cnt, _ := c.drainByOp.LoadOrStore(op, new(atomic.Int64))
	cnt.(*atomic.Int64).Add(1)
}

// ErrorBreakdown 返回 op→累计错误数的快照(无错误返回空 map)。供进程收尾时打印,
// 让运维直接看到残留 error 落在哪些 RPC,不用手 grep。
func (c *Collector) ErrorBreakdown() map[string]int64 {
	return snapshotCountMap(&c.errByOp)
}

// DrainBreakdown 返回 op→关停 canceled 数的快照(无则返回空 map)。
func (c *Collector) DrainBreakdown() map[string]int64 {
	return snapshotCountMap(&c.drainByOp)
}

// snapshotCountMap 把一个 op→*atomic.Int64 的 sync.Map 拍平成普通 map。
func snapshotCountMap(m *sync.Map) map[string]int64 {
	out := map[string]int64{}
	m.Range(func(k, v any) bool {
		out[k.(string)] = v.(*atomic.Int64).Load()
		return true
	})
	return out
}

// snapshot 生成当前分钟的 Record(计数取累计值快照,时延取并清空)。
func (c *Collector) snapshot(now time.Time) Record {
	p50, p99 := c.latency.drain()
	return Record{
		TS:              now.UTC().Format(time.RFC3339),
		Minute:          now.Unix() / 60,
		Machine:         c.machine,
		VUOnline:        c.Counters.VUOnline.Load(),
		LoginOK:         c.Counters.LoginOK.Load(),
		LoginFail:       c.Counters.LoginFail.Load(),
		SubscribeActive: c.Counters.SubscribeActive.Load(),
		MatchEnqueue:    c.Counters.MatchEnqueue.Load(),
		MatchConfirmed:  c.Counters.MatchConfirmed.Load(),
		MatchDispatched: c.Counters.MatchDispatched.Load(),
		BattleReported:  c.Counters.BattleReported.Load(),
		RPCP50Ms:        p50,
		RPCP99Ms:        p99,
		Errors:          c.Counters.RPCErrors.Load(),
	}
}

// writeRecord 追加一行 jsonl。
func (c *Collector) writeRecord(r Record) error {
	f, err := os.OpenFile(c.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	if err := enc.Encode(r); err != nil {
		return err
	}
	return w.Flush()
}

// Run 每分钟落盘一次,直到 ctx 关闭。返回的 error channel 只在写文件失败时投递。
// 调用方应在单独 goroutine 里 go collector.Run(ctx)。
func (c *Collector) Run(done <-chan struct{}, onErr func(error)) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			// 退出前补落最后一分钟。
			if err := c.writeRecord(c.snapshot(time.Now())); err != nil && onErr != nil {
				onErr(err)
			}
			return
		case now := <-ticker.C:
			if err := c.writeRecord(c.snapshot(now)); err != nil && onErr != nil {
				onErr(err)
			}
		}
	}
}
