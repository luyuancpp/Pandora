// Command stressbot 是阶段 1 单 Cell 压测客户端机群的入口。
//
// 用法:
//
//	stressbot -config robot/stress/config/single-cell-40w.json
//	stressbot -config <cfg> -vu 10000 -ramp 60 -steady 300   # 命令行覆盖
//
// 行为:读配置 → 建连接池 → 线性爬坡起 N 个 VU → 稳态保持 → 优雅停。
// 每分钟把聚合指标追加到 robot-stats.jsonl。
//
// ⚠️ 本进程只施压,不清库 / 不碰 k8s;跑测前的清库与 prom 快照由
// tools/scripts/dev_tools.ps1 + stress_snap.ps1 负责。
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/luyuancpp/pandora/robot/stress/internal/behavior"
	"github.com/luyuancpp/pandora/robot/stress/internal/client"
	"github.com/luyuancpp/pandora/robot/stress/internal/scenario"
	"github.com/luyuancpp/pandora/robot/stress/internal/stats"
	"github.com/luyuancpp/pandora/robot/stress/internal/vu"
)

func main() {
	var (
		configPath = flag.String("config", "", "压测场景 JSON 配置路径(空=内置默认)")
		vuCount    = flag.Int("vu", 0, "覆盖 vu_count(>0 生效)")
		ramp       = flag.Int("ramp", -1, "覆盖 ramp_seconds(≥0 生效)")
		steady     = flag.Int("steady", -1, "覆盖 steady_seconds(≥0 生效)")
		machine    = flag.String("machine", "", "覆盖 machine_id")
		dryRun     = flag.Bool("dry-run", false, "只打印解析后的配置后退出,不连后端、不施压")
	)
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := scenario.Load(*configPath)
	if err != nil {
		logger.Error("加载配置失败", "err", err)
		os.Exit(1)
	}
	if *vuCount > 0 {
		cfg.VUCount = *vuCount
	}
	if *ramp >= 0 {
		cfg.RampSeconds = *ramp
	}
	if *steady >= 0 {
		cfg.SteadySeconds = *steady
	}
	if *machine != "" {
		cfg.MachineID = *machine
	}
	if err := cfg.Validate(); err != nil {
		logger.Error("配置校验失败", "err", err)
		os.Exit(1)
	}

	logger.Info("压测场景已加载",
		"name", cfg.Name,
		"vu", cfg.VUCount,
		"ramp_s", cfg.RampSeconds,
		"steady_s", cfg.SteadySeconds,
		"ds_mode", cfg.DSMode,
		"machine", cfg.MachineID,
		"region", cfg.Router.RegionID,
		"cell", cfg.Router.CellID,
		"stats_file", cfg.StatsFile,
	)

	if *dryRun {
		logger.Info("dry-run:仅打印配置,不施压")
		return
	}

	// 连接池(共享连接,HTTP/2 多路复用)。
	pool, err := client.New(cfg.Targets)
	if err != nil {
		logger.Error("建立连接池失败", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	// 指标采集器。
	collector, err := stats.New(cfg.MachineID, cfg.StatsFile)
	if err != nil {
		logger.Error("初始化指标采集失败", "err", err)
		os.Exit(1)
	}

	sched := behavior.NewScheduler(cfg.Behavior)

	// 信号 / 时长控制。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("收到停止信号,开始优雅收敛")
		cancel()
	}()

	// 指标落盘 goroutine。collector 的停止信号刻意与 ctx 解耦:ctx 一取消 VU 才开始排空,
	// 若此时就写最终快照,会把还在收敛的 VU 漏记(最终行 vu_online≠0、error 计数不全)。
	// 改为等 VU 全部退出(wg.Wait)后再关 collectorStop,保证最终 jsonl 行计数完整。
	collectorStop := make(chan struct{})
	statsDone := make(chan struct{})
	go func() {
		collector.Run(collectorStop, func(e error) { logger.Warn("写 robot-stats.jsonl 失败", "err", e) })
		close(statsDone)
	}()

	// 稳态结束后自动收敛(ramp + steady)。
	if cfg.SteadySeconds > 0 {
		total := time.Duration(cfg.RampSeconds+cfg.SteadySeconds) * time.Second
		go func() {
			t := time.NewTimer(total)
			defer t.Stop()
			select {
			case <-ctx.Done():
			case <-t.C:
				logger.Info("到达 ramp+steady 时长,自动收敛", "total_s", int(total.Seconds()))
				cancel()
			}
		}()
	}

	// 线性爬坡启动 VU。
	var wg sync.WaitGroup
	rampInterval := cfg.RampInterval()
	logger.Info("开始爬坡启动 VU", "interval_us", rampInterval.Microseconds())
	for i := 0; i < cfg.VUCount; i++ {
		select {
		case <-ctx.Done():
			logger.Info("爬坡期间收到停止,停止继续起 VU", "started", i)
			goto wait
		default:
		}
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			vu.New(idx, cfg, pool, sched, collector).Run(ctx)
		}(i)
		if rampInterval > 0 {
			time.Sleep(rampInterval)
		}
	}
	logger.Info("全部 VU 已启动,进入稳态", "vu", cfg.VUCount)

wait:
	wg.Wait()
	cancel()
	// VU 已全部退出,现在才让 collector 写最终快照(vu_online=0,error/drain 计数完整),
	// 再等它收尾。这样最终 jsonl 行与下面的分项统计口径一致。
	close(collectorStop)
	<-statsDone

	// 收尾按调用点归因打印两类计数:
	//   1) RPC error 分项 = 真实后端错误(含 DeadlineExceeded 等),是要关注的质量信号。
	//   2) 关停排空 canceled 分项 = ctx 取消中断的在途 RPC,非后端错误,单列便于区分。
	// 都按计数降序排列,便于直接定位落在哪些 RPC,不用对日志手 grep。
	logBreakdown := func(title string, total int64, m map[string]int64) {
		if len(m) == 0 {
			logger.Info(title, "total", total)
			return
		}
		ops := make([]string, 0, len(m))
		for op := range m {
			ops = append(ops, op)
		}
		sort.Slice(ops, func(i, j int) bool {
			if m[ops[i]] != m[ops[j]] {
				return m[ops[i]] > m[ops[j]]
			}
			return ops[i] < ops[j]
		})
		attrs := make([]any, 0, len(ops)+1)
		attrs = append(attrs, "total", total)
		for _, op := range ops {
			attrs = append(attrs, op, m[op])
		}
		logger.Info(title, attrs...)
	}
	logBreakdown("RPC error 分项(真实后端错误)", collector.Counters.RPCErrors.Load(), collector.ErrorBreakdown())
	logBreakdown("关停排空 canceled 分项(非后端错误)", collector.Counters.DrainCanceled.Load(), collector.DrainBreakdown())

	logger.Info("压测进程结束")
}
