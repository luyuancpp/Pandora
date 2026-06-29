// Package conf 是 chat 服务的私有配置结构(2026-06-16)。
package conf

import (
	"github.com/luyuancpp/pandora/pkg/config"
)

// Config 是 chat 服务的完整配置。
type Config struct {
	config.Base `yaml:",inline" mapstructure:",squash"`

	Chat ChatConf `yaml:"chat" json:"chat"`
}

// ChatConf 是 chat 服务私有配置。
type ChatConf struct {
	// MaxContentLen 单条消息最大字符数(按 utf8 rune 计,默认 256)。
	// 超长 → ErrChatMessageTooLong。
	MaxContentLen int `yaml:"max_content_len,omitempty" json:"max_content_len,omitempty"`

	// HistoryLimit PullHistory 单次返回上限(默认 50)。请求 limit 超过此值时按此值截断。
	HistoryLimit int `yaml:"history_limit,omitempty" json:"history_limit,omitempty"`

	// TeamAddr team 服务 gRPC 地址(host:port)。
	// 空 → 队伍频道无法解析成员,TEAM 消息静默降级(弱依赖)。
	TeamAddr string `yaml:"team_addr,omitempty" json:"team_addr,omitempty"`

	// GuildAddr guild 服务 gRPC 地址(host:port)。GuildService + GroupService 同进程共用此地址。
	// 空 → 公会 / 群频道无法解析成员,GUILD / GROUP 消息静默降级(弱依赖)。
	GuildAddr string `yaml:"guild_addr,omitempty" json:"guild_addr,omitempty"`

	// SensitiveWords 敏感词列表(命中后整词替换为等长 *,大小写不敏感)。
	// 默认空 → 不过滤。仅做最小化屏蔽,真正风控由独立服务接管(后续)。
	SensitiveWords []string `yaml:"sensitive_words,omitempty" json:"sensitive_words,omitempty"`
}

// Defaults 填默认值,防止 yaml 缺字段时零值引发非预期行为。
func (c *Config) Defaults() {
	if c.Chat.MaxContentLen <= 0 {
		c.Chat.MaxContentLen = 256
	}
	if c.Chat.HistoryLimit <= 0 {
		c.Chat.HistoryLimit = 50
	}
	if c.Server.Grpc.Addr == "" {
		c.Server.Grpc.Addr = ":50005"
	}
	if c.Server.Http.Addr == "" {
		c.Server.Http.Addr = ":51005"
	}
}
