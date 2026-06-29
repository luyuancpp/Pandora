// config.go — cellroute 的部署配置 + 路由构造,把 scale-cellular-20m.md 的「装配层」收口到一处。
//
// 设计目标:18 个服务 main 不各写一套铺表逻辑,只读 config.Base.CellRoute 喂给 BuildRouter
// 即可。三种模式覆盖 §7 三个阶段:
//
//   - off(默认):返回 nil router,所有服务回退单 Cell 行为(nil-safe setter 不变),dev 零配置。
//   - static:本地配置直接铺表(BuildBalancedEntries),适合阶段 1 单 Cell / 阶段 2 少数 Cell 联调。
//   - etcd:只在 main 配 EtcdEndpoints,真正 watch 热更新走隔离 module pkg/cellroute/etcdtable,
//     本包不背 etcd 依赖(BuildRouter 对 etcd 模式只校验配置,Router 由 main 用 etcdtable 装)。
//
// 单 Cell 上线前不配 CellRoute(mode 空)=> off,行为与改造前逐字节一致;多 Cell 上线补 static/etcd。
package cellroute

import (
	"fmt"
	"strings"
)

// 模式常量。
const (
	ModeOff    = ""       // 单 Cell:不路由,所有服务 nil-safe 回退(默认)
	ModeStatic = "static" // 本地配置铺表:CellRoute.Cells 直接均匀切 4096 逻辑分片
	ModeEtcd   = "etcd"   // etcd 热更新:EtcdEndpoints 必填,Router 在 main 经 etcdtable 装配
)

// RouterConfig 是部署拓扑配置(放进 config.Base,18 服务共用)。
type RouterConfig struct {
	// Mode 路由模式:"" / "static" / "etcd"。空=单 Cell,不构造 router。
	Mode string `yaml:"mode,omitempty" json:"mode,omitempty"`

	// SelfRegion / SelfCell 是本实例所属 region/cell(部署拓扑维度 uint32,push 归属守卫等用)。
	SelfRegion uint32 `yaml:"self_region,omitempty" json:"self_region,omitempty"`
	SelfCell   uint32 `yaml:"self_cell,omitempty" json:"self_cell,omitempty"`

	// Cells 是 static 模式的物理 Cell 列表,均匀切给 4096 逻辑分片;etcd/off 模式忽略。
	Cells []CellEntry `yaml:"cells,omitempty" json:"cells,omitempty"`

	// EtcdEndpoints / EtcdPrefix 仅 etcd 模式用(交给 pkg/cellroute/etcdtable)。
	EtcdEndpoints []string `yaml:"etcd_endpoints,omitempty" json:"etcd_endpoints,omitempty"`
	EtcdPrefix    string   `yaml:"etcd_prefix,omitempty" json:"etcd_prefix,omitempty"`

	// MarketPeers 仅 auction 用:全部 auction 实例 ID(含 self),HRW 市场归属用。
	MarketPeers []string `yaml:"market_peers,omitempty" json:"market_peers,omitempty"`
	// MarketSelf 仅 auction 用:本实例 ID;留空退化单实例拥有全部市场。
	MarketSelf string `yaml:"market_self,omitempty" json:"market_self,omitempty"`
}

// CellEntry 配置里一个物理 Cell 的归属。
type CellEntry struct {
	RegionID uint32 `yaml:"region_id" json:"region_id"`
	CellID   uint32 `yaml:"cell_id" json:"cell_id"`
}

// Enabled 报告是否需要构造 router(static/etcd 为真,off 为假)。
func (c RouterConfig) Enabled() bool {
	return c.Mode == ModeStatic || c.Mode == ModeEtcd
}

// BuildRouter 按配置构造 Router。off 模式返回 (nil, nil)(调用方注入 nil = 单 Cell 行为不变)。
// static 模式本地铺表;etcd 模式不在此构造(需 etcdtable I/O,见包注释),返回明确错误指引 main
// 改用 etcdtable.Start;调用方据此分流。
func BuildRouter(cfg RouterConfig) (*Router, error) {
	switch cfg.Mode {
	case ModeOff:
		return nil, nil
	case ModeStatic:
		if len(cfg.Cells) == 0 {
			return nil, fmt.Errorf("cellroute: static mode needs cells")
		}
		specs := make([]CellSpec, 0, len(cfg.Cells))
		for _, c := range cfg.Cells {
			specs = append(specs, CellSpec{RegionID: c.RegionID, CellID: c.CellID})
		}
		entries, regionOfCell, err := BuildBalancedEntries(specs)
		if err != nil {
			return nil, err
		}
		tbl, err := NewStaticTable(entries, regionOfCell)
		if err != nil {
			return nil, err
		}
		return NewRouter(tbl)
	case ModeEtcd:
		if len(cfg.EtcdEndpoints) == 0 {
			return nil, fmt.Errorf("cellroute: etcd mode needs etcd_endpoints")
		}
		return nil, fmt.Errorf("cellroute: etcd mode router must be built via pkg/cellroute/etcdtable in main")
	default:
		return nil, fmt.Errorf("cellroute: unknown mode %q (want off/static/etcd)", cfg.Mode)
	}
}

// Validate 在配置加载后做基础自检(空 mode 跳过)。
func (c RouterConfig) Validate() error {
	switch c.Mode {
	case ModeOff:
		return nil
	case ModeStatic:
		if len(c.Cells) == 0 {
			return fmt.Errorf("cellroute: static mode needs cells")
		}
	case ModeEtcd:
		if len(c.EtcdEndpoints) == 0 {
			return fmt.Errorf("cellroute: etcd mode needs etcd_endpoints")
		}
	default:
		return fmt.Errorf("cellroute: unknown mode %q", c.Mode)
	}
	return nil
}

// MarketPeerList 归一化 auction market peers(去空去重,确保 self 在内)。
func (c RouterConfig) MarketPeerList() []string {
	out := make([]string, 0, len(c.MarketPeers))
	seen := map[string]struct{}{}
	for _, p := range c.MarketPeers {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	self := strings.TrimSpace(c.MarketSelf)
	if self != "" {
		if _, ok := seen[self]; !ok {
			out = append(out, self)
		}
	}
	return out
}
