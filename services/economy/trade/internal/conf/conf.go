// Package conf 是 trade 服务的私有配置结构(2026-06-16)。
package conf

import (
	"time"

	"github.com/luyuancpp/pandora/pkg/config"
)

// Config 是 trade 服务的完整配置。
type Config struct {
	config.Base `yaml:",inline" mapstructure:",squash"`

	Trade TradeConf `yaml:"trade" json:"trade"`
}

// TradeConf 是 trade 服务私有配置。
type TradeConf struct {
	// OrderTTL 订单 Redis key 存活时长(默认 "10m")。应 > OrderExpire,
	// 给已结算 / 已取消订单留一段查询窗口(ListMyOrders / 客户端回看)。
	OrderTTL config.Duration `yaml:"order_ttl,omitempty" json:"order_ttl,omitempty"`

	// OrderExpire 订单从创建到自动过期的时长(默认 "5m")。
	// 超时未完成两阶段确认的订单在被访问时惰性置 EXPIRED。
	OrderExpire config.Duration `yaml:"order_expire,omitempty" json:"order_expire,omitempty"`

	// OptimisticRetry WATCH/MULTI/EXEC 乐观锁最大重试次数(默认 3)。
	// 耗尽 → ErrTradeLockFailed。
	OptimisticRetry int `yaml:"optimistic_retry,omitempty" json:"optimistic_retry,omitempty"`

	// MaxItemsPerOrder 单订单最大物品条目数(默认 20)。
	MaxItemsPerOrder int `yaml:"max_items_per_order,omitempty" json:"max_items_per_order,omitempty"`

	// AllowNoopLedger 显式允许在没有接入真实资源账本时退回 NoopResourceLedger(占位,结算永远成功、
	// 不真实扣转背包 / 货币)。默认 false:未接真实账本即 fail-fast,防止生产漏配后仍以「成交不扣减」静默启动
	// (审计:trade 静默 Noop 结算降级)。真实账本(inventory P2P 原子对转 RPC)接入前,仅联调 / 单测显式置 true。
	AllowNoopLedger bool `yaml:"allow_noop_ledger,omitempty" json:"allow_noop_ledger,omitempty"`
}

// Defaults 填默认值,防止 yaml 缺字段时零值引发非预期行为。
func (c *Config) Defaults() {
	if c.Trade.OrderTTL <= 0 {
		c.Trade.OrderTTL = config.Duration(10 * time.Minute)
	}
	if c.Trade.OrderExpire <= 0 {
		c.Trade.OrderExpire = config.Duration(5 * time.Minute)
	}
	if c.Trade.OptimisticRetry <= 0 {
		c.Trade.OptimisticRetry = 3
	}
	if c.Trade.MaxItemsPerOrder <= 0 {
		c.Trade.MaxItemsPerOrder = 20
	}
	if c.Server.Grpc.Addr == "" {
		c.Server.Grpc.Addr = ":50012"
	}
	if c.Server.Http.Addr == "" {
		c.Server.Http.Addr = ":51012"
	}
}
