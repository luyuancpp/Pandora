// Package etcdtable 用 etcd watch 给 cellroute 提供「logical_cell → (region, cell)」
// 映射表的热更新(opt-in,独立 module 隔离重型 etcd client 依赖)。
//
// 为什么单独成 module:与 pkg/snowflake/etcdnode、pkg/killswitch/etcdkv 同理——
// go.etcd.io/etcd/client/v3 依赖较重,不让核心 pkg/cellroute 及所有业务服务无条件背上
// etcd client。只有真正进入多 Cell 部署、需要运行期热改映射表的服务,才在 main 里 import 本包。
//
// 分工(对照 pkg/cellroute/table_hotreload.go):解析 / 校验逻辑全在父包 cellroute
// (无 etcd 依赖,已单测);本包只做 etcd I/O —— 全量 Get 初始化 + Watch 增量,
// 每次变更后整理出全量 KV,调 cellroute.BuildStaticTableFromRaw 解析校验,再原子
// 替换 AtomicTable。读路径(Router.Route)始终看到完整一致快照(不变量②:整表替换不原地改)。
//
// ⚠️ 本 module 引入 go.etcd.io/etcd/client/v3,需 Codex 执行(对照 etcdnode 落地步骤):
//  1. 把 `use ./pkg/cellroute/etcdtable` 加入根 go.work
//  2. 在本目录 `go mod tidy` 拉取 etcd client 并生成 go.sum
//     版本号(v3.5.x)对齐 pkg/snowflake/etcdnode,可由 tidy 按可用版本微调。
//
// 用法(进入多 Cell 部署阶段、需运行期热改映射的服务 main.go):
//
//	w, err := etcdtable.Start(ctx, etcdtable.Config{
//	    Endpoints: cfg.CellRoute.EtcdEndpoints,
//	    Prefix:    "/pandora/cellroute/table/", // key = <prefix><logical_cell>, value="region:cell"
//	})
//	if err != nil { log.Fatal(err) }
//	defer w.Close()
//	router, _ := cellroute.NewRouter(w.Table()) // *AtomicTable,后续 watch 自动热更新
//
// dev / 单 Cell 仍走 cellroute.NewStaticTable(本地配置铺表),不引入本包。
package etcdtable

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	klog "github.com/go-kratos/kratos/v2/log"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/luyuancpp/pandora/pkg/cellroute"
)

const (
	// DefaultPrefix 是映射表 key 前缀。实际 key = <prefix><logical_cell>,value = "region:cell"。
	DefaultPrefix = "/pandora/cellroute/table/"
	// DefaultDialTimeout 是 etcd 连接默认超时。
	DefaultDialTimeout = 5 * time.Second
)

// Config 是热更新 watcher 的配置。
type Config struct {
	// Endpoints etcd 地址(必填)。
	Endpoints []string
	// Prefix key 前缀,留空用 DefaultPrefix。
	Prefix string
	// DialTimeout etcd 连接超时,留空用 DefaultDialTimeout。
	DialTimeout time.Duration
	// Logger 可选;为 nil 用 klog 默认。
	Logger klog.Logger
}

// Watcher 持有一张随 etcd 变更自动热更新的 AtomicTable。
type Watcher struct {
	cli    *clientv3.Client
	prefix string
	table  *cellroute.AtomicTable
	log    *klog.Helper

	cancel    context.CancelFunc
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// Start 连接 etcd,做一次全量 Get 铺出初始映射表(必须完整覆盖 [0, LogicalCellCount)),
// 然后起后台 goroutine watch 前缀做增量热更新。初始映射不完整 / 非法时返回错误(fail-fast,
// 不带半残映射上线)。
func Start(ctx context.Context, cfg Config) (*Watcher, error) {
	if len(cfg.Endpoints) == 0 {
		return nil, fmt.Errorf("etcdtable: empty endpoints")
	}
	prefix := cfg.Prefix
	if prefix == "" {
		prefix = DefaultPrefix
	}
	dial := cfg.DialTimeout
	if dial <= 0 {
		dial = DefaultDialTimeout
	}
	logger := cfg.Logger
	if logger == nil {
		logger = klog.DefaultLogger
	}
	help := klog.NewHelper(logger)

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   cfg.Endpoints,
		DialTimeout: dial,
	})
	if err != nil {
		return nil, fmt.Errorf("etcdtable: dial etcd: %w", err)
	}

	// 1. 全量 Get 铺初始表。
	getCtx, cancelGet := context.WithTimeout(ctx, dial)
	resp, err := cli.Get(getCtx, prefix, clientv3.WithPrefix())
	cancelGet()
	if err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("etcdtable: initial get: %w", err)
	}

	raw, err := parseKVs(resp.Kvs, prefix)
	if err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("etcdtable: parse initial: %w", err)
	}
	initial, err := cellroute.BuildStaticTableFromRaw(raw)
	if err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("etcdtable: build initial table: %w", err)
	}
	at, err := cellroute.NewAtomicTable(initial)
	if err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("etcdtable: new atomic table: %w", err)
	}

	wctx, cancel := context.WithCancel(context.Background())
	w := &Watcher{
		cli:    cli,
		prefix: prefix,
		table:  at,
		log:    help,
		cancel: cancel,
	}

	// 2. 后台 watch 增量。任一 key 变更后,重新全量 Get + 整表替换(简单稳妥;映射表小、
	//    变更低频,不做逐 key 增量合并以避免半残状态)。从当前 revision+1 开始 watch。
	w.wg.Add(1)
	go w.watchLoop(wctx, resp.Header.Revision+1)

	help.Infow("msg", "cellroute_etcdtable_started", "prefix", prefix, "logical_cells", initial.Len())
	return w, nil
}

// Table 返回随 etcd 热更新的 AtomicTable,直接喂 cellroute.NewRouter。
func (w *Watcher) Table() *cellroute.AtomicTable { return w.table }

// Close 停止 watch 并关闭 etcd 连接。
func (w *Watcher) Close() error {
	w.closeOnce.Do(func() {
		w.cancel()
		w.wg.Wait()
		_ = w.cli.Close()
	})
	return nil
}

func (w *Watcher) watchLoop(ctx context.Context, fromRev int64) {
	defer w.wg.Done()
	wch := w.cli.Watch(ctx, w.prefix, clientv3.WithPrefix(), clientv3.WithRev(fromRev))
	for {
		select {
		case <-ctx.Done():
			return
		case resp, ok := <-wch:
			if !ok {
				w.log.Warnw("msg", "cellroute_etcdtable_watch_closed")
				return
			}
			if err := resp.Err(); err != nil {
				w.log.Errorw("msg", "cellroute_etcdtable_watch_err", "err", err)
				continue
			}
			if len(resp.Events) == 0 {
				continue
			}
			// 有变更:重新全量 Get + 整表替换。失败保留旧表(不回退成半残),仅告警。
			w.reload(ctx)
		}
	}
}

func (w *Watcher) reload(ctx context.Context) {
	getCtx, cancel := context.WithTimeout(ctx, DefaultDialTimeout)
	resp, err := w.cli.Get(getCtx, w.prefix, clientv3.WithPrefix())
	cancel()
	if err != nil {
		w.log.Errorw("msg", "cellroute_etcdtable_reload_get_err", "err", err)
		return
	}
	raw, err := parseKVs(resp.Kvs, w.prefix)
	if err != nil {
		w.log.Errorw("msg", "cellroute_etcdtable_reload_parse_err", "err", err)
		return
	}
	tbl, err := cellroute.BuildStaticTableFromRaw(raw)
	if err != nil {
		// 映射不完整 / 非法:保留旧表,不替换。映射变更必须经合法全量(不变量②)。
		w.log.Errorw("msg", "cellroute_etcdtable_reload_build_err", "err", err)
		return
	}
	if err := w.table.Store(tbl); err != nil {
		w.log.Errorw("msg", "cellroute_etcdtable_reload_store_err", "err", err)
		return
	}
	w.log.Infow("msg", "cellroute_etcdtable_reloaded", "logical_cells", tbl.Len())
}

// parseKVs 把 etcd 的 [key=<prefix><logical_cell>, value="region:cell"] 列表整理成
// cellroute.DecodeEntries 需要的 map[logical_cell]value。纯整理,不做语义校验(交给 cellroute)。
func parseKVs(kvs []*mvccpb.KeyValue, prefix string) (map[uint32]string, error) {
	raw := make(map[uint32]string, len(kvs))
	for _, kv := range kvs {
		key := string(kv.Key)
		suffix := strings.TrimPrefix(key, prefix)
		lc, err := strconv.ParseUint(suffix, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("etcdtable: bad key %q (suffix %q not a logical_cell): %w", key, suffix, err)
		}
		raw[uint32(lc)] = string(kv.Value)
	}
	return raw, nil
}

// BuildRouter 是 18 服务 main 统一装配口:按 cfg.Mode 返回 (router, closer, err)。
//
//   - off(空):返回 (nil, nil, nil) —— 单 Cell,所有服务 nil-safe 回退,行为不变。
//   - static:本地铺表(不连 etcd),closer 为 nil。
//   - etcd:连 etcd 全量 Get + watch 热更新,closer 是 Watcher.Close(main defer 调用)。
//
// 这样 main 只写一句 router, closer, err := etcdtable.BuildRouter(ctx, cfg.CellRoute),
// 不在每个服务里重复 off/static/etcd 分流。
func BuildRouter(ctx context.Context, cfg cellroute.RouterConfig) (*cellroute.Router, func() error, error) {
	if cfg.Mode != cellroute.ModeEtcd {
		r, err := cellroute.BuildRouter(cfg) // off→nil,static→本地铺表
		return r, nil, err
	}
	w, err := Start(ctx, Config{Endpoints: cfg.EtcdEndpoints, Prefix: cfg.EtcdPrefix})
	if err != nil {
		return nil, nil, err
	}
	r, err := cellroute.NewRouter(w.Table())
	if err != nil {
		_ = w.Close()
		return nil, nil, err
	}
	return r, w.Close, nil
}

// WireRouter 构造 router 并经 set 注入业务 usecase,返回 closer(etcd 模式非 nil,main defer)。
// off 模式 set 不被调用(router=nil),单 Cell 行为不变。给 18 服务 main 一行接线。
func WireRouter(ctx context.Context, cfg cellroute.RouterConfig, set func(*cellroute.Router)) (func() error, error) {
	r, closer, err := BuildRouter(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if r != nil && set != nil {
		set(r)
	}
	return closer, nil
}
