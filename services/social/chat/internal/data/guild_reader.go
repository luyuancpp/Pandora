// guild_reader.go 实现 biz.GuildReader:通过 gRPC 拉 guild 服务的公会成员名单,
// 供公会频道(GUILD)解析成员(弱依赖,addr 空则 main 不注入)。
package data

import (
	"context"

	"google.golang.org/grpc"

	"github.com/luyuancpp/pandora/pkg/grpcclient"
	commonv1 "github.com/luyuancpp/pandora/proto/gen/go/pandora/common/v1"
	guildv1 "github.com/luyuancpp/pandora/proto/gen/go/pandora/guild/v1"
)

// GrpcGuildReader 用 guild 服务 gRPC client 实现 biz.GuildReader。
type GrpcGuildReader struct {
	conn *grpc.ClientConn
	cli  guildv1.GuildServiceClient
}

// NewGrpcGuildReader 直连 guild 服务 endpoint(host:port,内网 insecure)。
func NewGrpcGuildReader(guildAddr string) *GrpcGuildReader {
	conn := grpcclient.MustDialInsecure(guildAddr)
	return &GrpcGuildReader{conn: conn, cli: guildv1.NewGuildServiceClient(conn)}
}

// Close 关闭底层连接。
func (g *GrpcGuildReader) Close() error {
	if g.conn != nil {
		return g.conn.Close()
	}
	return nil
}

// GetGuildMembers 调 guild 服务 ListMembers,返回公会成员 player_id 列表。
// guild 服务返回非 OK code 或公会不存在 → (nil, false, nil)(由 biz 决定降级行为)。
func (g *GrpcGuildReader) GetGuildMembers(ctx context.Context, guildID uint64) ([]uint64, bool, error) {
	resp, err := g.cli.ListMembers(ctx, &guildv1.ListMembersRequest{GuildId: guildID})
	if err != nil {
		return nil, false, err
	}
	if resp.GetCode() != commonv1.ErrCode_OK {
		return nil, false, nil
	}
	ids := make([]uint64, 0, len(resp.GetMembers()))
	for _, m := range resp.GetMembers() {
		ids = append(ids, m.GetPlayerId())
	}
	return ids, true, nil
}
