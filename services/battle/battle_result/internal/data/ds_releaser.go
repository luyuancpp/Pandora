// ds_releaser.go 实现 biz.DSReleaser:通过 gRPC 调 ds_allocator 内部接口 ReleaseBattle,
// 在一场对局正常结算落库后幂等回收战斗 DS 的后端账本(Redis 镜像 + active ZSET)。
//
// 设计(修复"ReleaseBattle 无生产调用方,正常局账本要等 ~15s sweep / 2h TTL 才清"):
//   - ds_allocator 暴露 ReleaseBattle(对局结束/异常回收 DS),原本只有 service handler + 单测,
//     matchmaker / battle_result 都没调 → 正常局结束只靠 DS 自身 Agones Shutdown(pod 优雅下线)
//   - 心跳停止后被 sweep 的 ended 分支移出 active。账本(镜像/active)与现实最长差 ~15s。
//   - battle_result 正常结算落库后调此接口主动回收,使后端账本与现实即时一致;DS 自停仍是 pod
//     优雅下线的权威路径,本调用对 pod 是幂等兜底(DS 已自停 → GS 不存在,Release 走幂等 no-op)。
//   - 内部服务间调用:ds_allocator gRPC server 用内网 insecure,本调用按 match_id 操作,合法。
//   - 弱依赖语义:ds_allocator 地址未配 / 调用失败时由 biz.releaseDS 仅 Warn,不阻断结算落库。
package data

import (
	"context"

	"google.golang.org/grpc"

	"github.com/luyuancpp/pandora/pkg/errcode"
	"github.com/luyuancpp/pandora/pkg/grpcclient"
	commonv1 "github.com/luyuancpp/pandora/proto/gen/go/pandora/common/v1"
	dsv1 "github.com/luyuancpp/pandora/proto/gen/go/pandora/ds/v1"
)

// GrpcDSReleaser 用 ds_allocator 服务 gRPC client 实现 biz.DSReleaser。
type GrpcDSReleaser struct {
	conn *grpc.ClientConn
	cli  dsv1.DSAllocatorServiceClient
}

// NewGrpcDSReleaser 直连 ds_allocator 服务 endpoint(host:port,内网 insecure)。
func NewGrpcDSReleaser(dsAllocatorAddr string) *GrpcDSReleaser {
	conn := grpcclient.MustDialInsecure(dsAllocatorAddr)
	return &GrpcDSReleaser{
		conn: conn,
		cli:  dsv1.NewDSAllocatorServiceClient(conn),
	}
}

// Close 关闭底层连接。
func (g *GrpcDSReleaser) Close() error {
	if g.conn != nil {
		return g.conn.Close()
	}
	return nil
}

// ReleaseBattle 调 ds_allocator.ReleaseBattle 回收战斗 DS 账本(幂等;镜像不存在视为已释放)。
func (g *GrpcDSReleaser) ReleaseBattle(ctx context.Context, matchID uint64, reason string) error {
	resp, err := g.cli.ReleaseBattle(ctx, &dsv1.ReleaseBattleRequest{
		MatchId: matchID,
		Reason:  reason,
	})
	if err != nil {
		return err
	}
	if resp.GetCode() != commonv1.ErrCode_OK {
		return errcode.New(errcode.Code(resp.GetCode()), "ds_allocator.ReleaseBattle code=%d match=%d", resp.GetCode(), matchID)
	}
	return nil
}
