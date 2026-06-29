// Package server — gRPC server 注册(2026-06-27)。
//
// guild 服务同进程注册两套 RPC:GuildService(公会)+ GroupService(临时群)。
package server

import (
	kgrpc "github.com/go-kratos/kratos/v2/transport/grpc"

	"github.com/luyuancpp/pandora/pkg/grpcserver"
	pmw "github.com/luyuancpp/pandora/pkg/middleware"
	groupv1 "github.com/luyuancpp/pandora/proto/gen/go/pandora/group/v1"
	guildv1 "github.com/luyuancpp/pandora/proto/gen/go/pandora/guild/v1"

	"github.com/luyuancpp/pandora/services/social/guild/internal/conf"
	"github.com/luyuancpp/pandora/services/social/guild/internal/service"
)

// NewGRPCServer 构造 gRPC server 并注册 GuildService + GroupService。
//
// 端口由 cfg.Server.Grpc.Addr 决定(默认 :50008)。
// pmw.AuthOptional() 从 Envoy 注入的 x-pandora-player-id header 读 player_id 注入 ctx。
// Envoy jwt_authn 已在路由层 require JWT;service 层再做一次 callerID==0 拦截兜底。
func NewGRPCServer(cfg *conf.Config, guildSvc *service.GuildService, groupSvc *service.GroupService) *kgrpc.Server {
	srv := grpcserver.MustNewServer(cfg.Server, pmw.AuthOptional())
	guildv1.RegisterGuildServiceServer(srv, guildSvc)
	groupv1.RegisterGroupServiceServer(srv, groupSvc)
	return srv
}
