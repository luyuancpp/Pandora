// Package service 是 trade 服务的 gRPC service 层(2026-06-16)。
//
// 职责:
//   - 实现 tradev1.TradeServiceServer 接口
//   - 从 ctx 取 JWT player_id(R5:override request 字段,防伪造他人身份)
//   - proto Request/Response ↔ biz 入参/出参互转
//   - errcode.Code → commonv1.ErrCode 1:1 映射
//
// 协议原则(R5):CreateOrder 的 seller_id、Confirm/Cancel/List 的 player_id 一律以
// ctx 中的 JWT player_id 为准,忽略请求体里的对应字段;player_id=0 → ERR_UNAUTHORIZED。
package service

import (
	"context"

	"github.com/luyuancpp/pandora/pkg/errcode"
	plog "github.com/luyuancpp/pandora/pkg/log"
	commonv1 "github.com/luyuancpp/pandora/proto/gen/go/pandora/common/v1"
	tradev1 "github.com/luyuancpp/pandora/proto/gen/go/pandora/trade/v1"

	"github.com/luyuancpp/pandora/services/economy/trade/internal/biz"
)

// TradeService 实现 tradev1.TradeServiceServer。
type TradeService struct {
	tradev1.UnimplementedTradeServiceServer
	uc *biz.TradeUsecase
}

// NewTradeService 构造。
func NewTradeService(uc *biz.TradeUsecase) *TradeService {
	return &TradeService{uc: uc}
}

// CreateOrder 卖方挂单。seller 以 JWT ctx 为准(R5)。
func (s *TradeService) CreateOrder(ctx context.Context, req *tradev1.CreateOrderRequest) (*tradev1.CreateOrderResponse, error) {
	sellerID := callerID(ctx)
	if sellerID == 0 {
		return &tradev1.CreateOrderResponse{Code: commonv1.ErrCode_ERR_UNAUTHORIZED}, nil
	}

	orderID, err := s.uc.CreateOrder(ctx, sellerID, req.GetBuyerId(), req.GetItems(), req.GetBuyerItems(), req.GetPrice())
	if err != nil {
		return &tradev1.CreateOrderResponse{Code: toProtoCode(err)}, nil
	}
	return &tradev1.CreateOrderResponse{Code: commonv1.ErrCode_OK, OrderId: orderID}, nil
}

// ConfirmOrder 确认订单(两阶段)。player 以 JWT ctx 为准(R5)。
func (s *TradeService) ConfirmOrder(ctx context.Context, req *tradev1.ConfirmOrderRequest) (*tradev1.ConfirmOrderResponse, error) {
	playerID := callerID(ctx)
	if playerID == 0 {
		return &tradev1.ConfirmOrderResponse{Code: commonv1.ErrCode_ERR_UNAUTHORIZED}, nil
	}
	if req.GetOrderId() == 0 {
		return &tradev1.ConfirmOrderResponse{Code: commonv1.ErrCode_ERR_INVALID_ARG}, nil
	}

	newState, err := s.uc.ConfirmOrder(ctx, playerID, req.GetOrderId())
	if err != nil {
		return &tradev1.ConfirmOrderResponse{Code: toProtoCode(err), NewState: newState}, nil
	}
	return &tradev1.ConfirmOrderResponse{Code: commonv1.ErrCode_OK, NewState: newState}, nil
}

// CancelOrder 取消订单。player 以 JWT ctx 为准(R5)。
func (s *TradeService) CancelOrder(ctx context.Context, req *tradev1.CancelOrderRequest) (*tradev1.CancelOrderResponse, error) {
	playerID := callerID(ctx)
	if playerID == 0 {
		return &tradev1.CancelOrderResponse{Code: commonv1.ErrCode_ERR_UNAUTHORIZED}, nil
	}
	if req.GetOrderId() == 0 {
		return &tradev1.CancelOrderResponse{Code: commonv1.ErrCode_ERR_INVALID_ARG}, nil
	}

	if err := s.uc.CancelOrder(ctx, playerID, req.GetOrderId()); err != nil {
		return &tradev1.CancelOrderResponse{Code: toProtoCode(err)}, nil
	}
	return &tradev1.CancelOrderResponse{Code: commonv1.ErrCode_OK}, nil
}

// ListMyOrders 列玩家订单。player 以 JWT ctx 为准(R5)。
func (s *TradeService) ListMyOrders(ctx context.Context, req *tradev1.ListMyOrdersRequest) (*tradev1.ListMyOrdersResponse, error) {
	playerID := callerID(ctx)
	if playerID == 0 {
		return &tradev1.ListMyOrdersResponse{Code: commonv1.ErrCode_ERR_UNAUTHORIZED}, nil
	}

	orders, err := s.uc.ListMyOrders(ctx, playerID, req.GetActiveOnly())
	if err != nil {
		return &tradev1.ListMyOrdersResponse{Code: toProtoCode(err)}, nil
	}
	return &tradev1.ListMyOrdersResponse{Code: commonv1.ErrCode_OK, Orders: orders}, nil
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
