// Package service 是 friend 服务的 gRPC service 层(2026-06-15)。
//
// 职责:
//   - 实现 friendv1.FriendServiceServer 接口
//   - 从 ctx 取 JWT player_id(R5:override request 字段,防伪造他人身份)
//   - proto Request/Response ↔ biz 入参/出参互转
//   - errcode.Code → commonv1.ErrCode 1:1 映射
//
// 协议原则(R5):所有 RPC 强制用 ctx 中的 player_id,忽略请求体里的 player_id 字段;
// player_id=0 → ERR_UNAUTHORIZED(Envoy jwt_authn 已在路由层 require JWT,这里兜底)。
package service

import (
	"context"

	"github.com/luyuancpp/pandora/pkg/errcode"
	plog "github.com/luyuancpp/pandora/pkg/log"
	commonv1 "github.com/luyuancpp/pandora/proto/gen/go/pandora/common/v1"
	friendv1 "github.com/luyuancpp/pandora/proto/gen/go/pandora/friend/v1"

	"github.com/luyuancpp/pandora/services/social/friend/internal/biz"
)

// snowflakeGen 是 snowflake.Node 的最小接口,避免 service 直接依赖 snowflake 包。
type snowflakeGen interface {
	Generate() uint64
}

// FriendService 实现 friendv1.FriendServiceServer。
type FriendService struct {
	friendv1.UnimplementedFriendServiceServer
	uc *biz.FriendUsecase
	sf snowflakeGen
}

// NewFriendService 构造。
func NewFriendService(uc *biz.FriendUsecase, sf snowflakeGen) *FriendService {
	return &FriendService{uc: uc, sf: sf}
}

// AddFriend 发起好友请求。requester 以 JWT ctx 为准(R5)。
func (s *FriendService) AddFriend(ctx context.Context, req *friendv1.AddFriendRequest) (*friendv1.AddFriendResponse, error) {
	playerID := callerID(ctx)
	if playerID == 0 {
		return &friendv1.AddFriendResponse{Code: commonv1.ErrCode_ERR_UNAUTHORIZED}, nil
	}
	if req.GetTargetPlayerId() == 0 {
		return &friendv1.AddFriendResponse{Code: commonv1.ErrCode_ERR_INVALID_ARG}, nil
	}

	requestID, err := s.uc.AddFriend(ctx, playerID, req.GetTargetPlayerId(), s.sf.Generate())
	if err != nil {
		return &friendv1.AddFriendResponse{Code: toProtoCode(err)}, nil
	}
	return &friendv1.AddFriendResponse{Code: commonv1.ErrCode_OK, RequestId: requestID}, nil
}

// AcceptFriend 接受好友请求。接受者以 JWT ctx 为准(R5)。
func (s *FriendService) AcceptFriend(ctx context.Context, req *friendv1.AcceptFriendRequest) (*friendv1.AcceptFriendResponse, error) {
	playerID := callerID(ctx)
	if playerID == 0 {
		return &friendv1.AcceptFriendResponse{Code: commonv1.ErrCode_ERR_UNAUTHORIZED}, nil
	}
	if req.GetRequestId() == 0 {
		return &friendv1.AcceptFriendResponse{Code: commonv1.ErrCode_ERR_INVALID_ARG}, nil
	}

	if err := s.uc.AcceptFriend(ctx, playerID, req.GetRequestId()); err != nil {
		return &friendv1.AcceptFriendResponse{Code: toProtoCode(err)}, nil
	}
	return &friendv1.AcceptFriendResponse{Code: commonv1.ErrCode_OK}, nil
}

// RejectFriend 拒绝好友请求。拒绝者以 JWT ctx 为准(R5)。
func (s *FriendService) RejectFriend(ctx context.Context, req *friendv1.RejectFriendRequest) (*friendv1.RejectFriendResponse, error) {
	playerID := callerID(ctx)
	if playerID == 0 {
		return &friendv1.RejectFriendResponse{Code: commonv1.ErrCode_ERR_UNAUTHORIZED}, nil
	}
	if req.GetRequestId() == 0 {
		return &friendv1.RejectFriendResponse{Code: commonv1.ErrCode_ERR_INVALID_ARG}, nil
	}

	if err := s.uc.RejectFriend(ctx, playerID, req.GetRequestId()); err != nil {
		return &friendv1.RejectFriendResponse{Code: toProtoCode(err)}, nil
	}
	return &friendv1.RejectFriendResponse{Code: commonv1.ErrCode_OK}, nil
}

// ListFriendRequests 列待处理(收到的)好友请求。player_id 以 JWT ctx 为准(R5)。
func (s *FriendService) ListFriendRequests(ctx context.Context, _ *friendv1.ListFriendRequestsRequest) (*friendv1.ListFriendRequestsResponse, error) {
	playerID := callerID(ctx)
	if playerID == 0 {
		return &friendv1.ListFriendRequestsResponse{Code: commonv1.ErrCode_ERR_UNAUTHORIZED}, nil
	}

	requests, err := s.uc.ListFriendRequests(ctx, playerID)
	if err != nil {
		return &friendv1.ListFriendRequestsResponse{Code: toProtoCode(err)}, nil
	}
	return &friendv1.ListFriendRequestsResponse{Code: commonv1.ErrCode_OK, Requests: requests}, nil
}

// ListFriends 列好友。player_id 以 JWT ctx 为准(R5)。
func (s *FriendService) ListFriends(ctx context.Context, _ *friendv1.ListFriendsRequest) (*friendv1.ListFriendsResponse, error) {
	playerID := callerID(ctx)
	if playerID == 0 {
		return &friendv1.ListFriendsResponse{Code: commonv1.ErrCode_ERR_UNAUTHORIZED}, nil
	}

	friends, err := s.uc.ListFriends(ctx, playerID)
	if err != nil {
		return &friendv1.ListFriendsResponse{Code: toProtoCode(err)}, nil
	}
	return &friendv1.ListFriendsResponse{Code: commonv1.ErrCode_OK, Friends: friends}, nil
}

// RemoveFriend 删好友。player_id 以 JWT ctx 为准(R5)。
func (s *FriendService) RemoveFriend(ctx context.Context, req *friendv1.RemoveFriendRequest) (*friendv1.RemoveFriendResponse, error) {
	playerID := callerID(ctx)
	if playerID == 0 {
		return &friendv1.RemoveFriendResponse{Code: commonv1.ErrCode_ERR_UNAUTHORIZED}, nil
	}
	if req.GetTargetPlayerId() == 0 {
		return &friendv1.RemoveFriendResponse{Code: commonv1.ErrCode_ERR_INVALID_ARG}, nil
	}

	if err := s.uc.RemoveFriend(ctx, playerID, req.GetTargetPlayerId()); err != nil {
		return &friendv1.RemoveFriendResponse{Code: toProtoCode(err)}, nil
	}
	return &friendv1.RemoveFriendResponse{Code: commonv1.ErrCode_OK}, nil
}

// Block 拉黑 target。player_id 以 JWT ctx 为准(R5)。
func (s *FriendService) Block(ctx context.Context, req *friendv1.BlockRequest) (*friendv1.BlockResponse, error) {
	playerID := callerID(ctx)
	if playerID == 0 {
		return &friendv1.BlockResponse{Code: commonv1.ErrCode_ERR_UNAUTHORIZED}, nil
	}
	if req.GetTargetPlayerId() == 0 {
		return &friendv1.BlockResponse{Code: commonv1.ErrCode_ERR_INVALID_ARG}, nil
	}

	if err := s.uc.Block(ctx, playerID, req.GetTargetPlayerId()); err != nil {
		return &friendv1.BlockResponse{Code: toProtoCode(err)}, nil
	}
	return &friendv1.BlockResponse{Code: commonv1.ErrCode_OK}, nil
}

// Unblock 取消拉黑。player_id 以 JWT ctx 为准(R5)。
func (s *FriendService) Unblock(ctx context.Context, req *friendv1.UnblockRequest) (*friendv1.UnblockResponse, error) {
	playerID := callerID(ctx)
	if playerID == 0 {
		return &friendv1.UnblockResponse{Code: commonv1.ErrCode_ERR_UNAUTHORIZED}, nil
	}
	if req.GetTargetPlayerId() == 0 {
		return &friendv1.UnblockResponse{Code: commonv1.ErrCode_ERR_INVALID_ARG}, nil
	}

	if err := s.uc.Unblock(ctx, playerID, req.GetTargetPlayerId()); err != nil {
		return &friendv1.UnblockResponse{Code: toProtoCode(err)}, nil
	}
	return &friendv1.UnblockResponse{Code: commonv1.ErrCode_OK}, nil
}

// ListBlocks 列黑名单。player_id 以 JWT ctx 为准(R5)。
func (s *FriendService) ListBlocks(ctx context.Context, _ *friendv1.ListBlocksRequest) (*friendv1.ListBlocksResponse, error) {
	playerID := callerID(ctx)
	if playerID == 0 {
		return &friendv1.ListBlocksResponse{Code: commonv1.ErrCode_ERR_UNAUTHORIZED}, nil
	}

	blocks, err := s.uc.ListBlocks(ctx, playerID)
	if err != nil {
		return &friendv1.ListBlocksResponse{Code: toProtoCode(err)}, nil
	}
	return &friendv1.ListBlocksResponse{Code: commonv1.ErrCode_OK, Blocks: blocks}, nil
}

// RecommendFriends 推荐好友(每批默认 5 个,客户端回传 exclude_player_ids 刷新)。player_id 以 JWT ctx 为准(R5)。
func (s *FriendService) RecommendFriends(ctx context.Context, req *friendv1.RecommendFriendsRequest) (*friendv1.RecommendFriendsResponse, error) {
	playerID := callerID(ctx)
	if playerID == 0 {
		return &friendv1.RecommendFriendsResponse{Code: commonv1.ErrCode_ERR_UNAUTHORIZED}, nil
	}

	recs, err := s.uc.RecommendFriends(ctx, playerID, int(req.GetLimit()), req.GetExcludePlayerIds())
	if err != nil {
		return &friendv1.RecommendFriendsResponse{Code: toProtoCode(err)}, nil
	}
	return &friendv1.RecommendFriendsResponse{Code: commonv1.ErrCode_OK, Recommendations: recs}, nil
}

// ── 辅助 ──────────────────────────────────────────────────────────────────────

// callerID 从 ctx 取 JWT 注入的 player_id。
func callerID(ctx context.Context) uint64 {
	id, _ := ctx.Value(plog.CtxKeyPlayerID).(uint64)
	return id
}

// toProtoCode 把 pkg/errcode 1:1 映射成 proto enum(数值相同)。
func toProtoCode(err error) commonv1.ErrCode {
	return commonv1.ErrCode(errcode.As(err))
}
