// group_reader.go 实现 biz.GroupReader:通过 gRPC 拉 guild 服务的临时群成员名单,
// 供群频道(GROUP)解析成员(弱依赖,addr 空则 main 不注入)。
//
// 注:GroupService 与 GuildService 同进程(guild 服务),共用同一 gRPC 地址。
package data

import (
	"context"

	"google.golang.org/grpc"

	"github.com/luyuancpp/pandora/pkg/grpcclient"
	commonv1 "github.com/luyuancpp/pandora/proto/gen/go/pandora/common/v1"
	groupv1 "github.com/luyuancpp/pandora/proto/gen/go/pandora/group/v1"
)

// GrpcGroupReader 用 group 服务 gRPC client 实现 biz.GroupReader。
type GrpcGroupReader struct {
	conn *grpc.ClientConn
	cli  groupv1.GroupServiceClient
}

// NewGrpcGroupReader 直连 guild 服务 endpoint(host:port,内网 insecure)。
func NewGrpcGroupReader(groupAddr string) *GrpcGroupReader {
	conn := grpcclient.MustDialInsecure(groupAddr)
	return &GrpcGroupReader{conn: conn, cli: groupv1.NewGroupServiceClient(conn)}
}

// Close 关闭底层连接。
func (g *GrpcGroupReader) Close() error {
	if g.conn != nil {
		return g.conn.Close()
	}
	return nil
}

// GetGroupMembers 调 group 服务 ListGroupMembers,返回群成员 player_id 列表。
// group 服务返回非 OK code 或群不存在 → (nil, false, nil)(由 biz 决定降级行为)。
func (g *GrpcGroupReader) GetGroupMembers(ctx context.Context, groupID uint64) ([]uint64, bool, error) {
	resp, err := g.cli.ListGroupMembers(ctx, &groupv1.ListGroupMembersRequest{GroupId: groupID})
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
