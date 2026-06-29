// Package conf 是 guild 服务的私有配置结构(2026-06-27)。
package conf

import (
	"github.com/luyuancpp/pandora/pkg/config"
)

// Config 是 guild 服务的完整配置(公会 + 临时群同进程共用)。
type Config struct {
	config.Base `yaml:",inline" mapstructure:",squash"`

	Guild GuildConf `yaml:"guild" json:"guild"`
}

// GuildConf 是 guild 服务私有配置(公会 + 群上限)。
type GuildConf struct {
	// MaxGuildMembers 单公会成员上限(默认 100)。
	MaxGuildMembers int `yaml:"max_guild_members,omitempty" json:"max_guild_members,omitempty"`

	// MaxGroupMembers 单临时群成员上限(默认 50)。
	MaxGroupMembers int `yaml:"max_group_members,omitempty" json:"max_group_members,omitempty"`

	// MaxNameLen 公会 / 群名最大长度(utf8 rune,默认 24)。
	MaxNameLen int `yaml:"max_name_len,omitempty" json:"max_name_len,omitempty"`
}

// Defaults 填默认值,防止 yaml 缺字段时零值引发非预期行为。
func (c *Config) Defaults() {
	if c.Guild.MaxGuildMembers <= 0 {
		c.Guild.MaxGuildMembers = 100
	}
	if c.Guild.MaxGroupMembers <= 0 {
		c.Guild.MaxGroupMembers = 50
	}
	if c.Guild.MaxNameLen <= 0 {
		c.Guild.MaxNameLen = 24
	}
	if c.Server.Grpc.Addr == "" {
		c.Server.Grpc.Addr = ":50008"
	}
	if c.Server.Http.Addr == "" {
		c.Server.Http.Addr = ":51008"
	}
}
