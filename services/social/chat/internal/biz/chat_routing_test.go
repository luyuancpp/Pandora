// chat_routing_test.go — 私聊跨 region 投递落点 + 全局桥 key 口径单测(蜂窝扩容 ⑩)。
//
// 覆盖:
//   - PrivateBridgeKey:canonical = 接收方 player_id(§4.4 key=接收方)
//   - PrivatePeers.CrossRegionPrivate:同 region false / 跨 region true
//   - privatePeers:nil router 退化 / 双 region 路由器按收发双方解析 region
package biz

import (
	"testing"

	"github.com/luyuancpp/pandora/pkg/cellroute"
	"github.com/luyuancpp/pandora/services/social/chat/internal/conf"
)

// twoRegionRouter 构造一张把逻辑分片前半切给 (region1, cell1)、后半切给 (region2, cell2) 的路由器。
// player_id%4096 落前半 → region1,落后半 → region2,便于确定性构造跨 region 场景。
func twoRegionRouter(t *testing.T, region1, cell1, region2, cell2 uint32) *cellroute.Router {
	t.Helper()
	entries, regionOfCell, err := cellroute.BuildBalancedEntries([]cellroute.CellSpec{
		{RegionID: region1, CellID: cell1},
		{RegionID: region2, CellID: cell2},
	})
	if err != nil {
		t.Fatalf("BuildBalancedEntries: %v", err)
	}
	tbl, err := cellroute.NewStaticTable(entries, regionOfCell)
	if err != nil {
		t.Fatalf("NewStaticTable: %v", err)
	}
	r, err := cellroute.NewRouter(tbl)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return r
}

// playerInRegion1 返回一个 logical_cell 落前半(region1)的 player_id。
func playerInRegion1() uint64 { return 1 }

// playerInRegion2 返回一个 logical_cell 落后半(region2)的 player_id。
func playerInRegion2() uint64 { return uint64(cellroute.LogicalCellCount)/2 + 1 }

func newRoutingUC(t *testing.T) *ChatUsecase {
	t.Helper()
	return NewChatUsecase(&fakeRepo{}, nil, nil, nil, nil, conf.ChatConf{MaxContentLen: 10, HistoryLimit: 50})
}

func TestPrivateBridgeKey_IsTargetPlayerID(t *testing.T) {
	if got := PrivateBridgeKey(2002); got != "2002" {
		t.Fatalf("PrivateBridgeKey(2002) = %q, want %q", got, "2002")
	}
}

func TestCrossRegionPrivate_DiffRegion(t *testing.T) {
	if !(PrivatePeers{SenderRegionID: 1, TargetRegionID: 2}).CrossRegionPrivate() {
		t.Fatalf("different regions must be cross-region private")
	}
	if (PrivatePeers{SenderRegionID: 5, TargetRegionID: 5}).CrossRegionPrivate() {
		t.Fatalf("same region must not be cross-region private")
	}
}

func TestPrivatePeers_NilRouterNotOk(t *testing.T) {
	u := newRoutingUC(t)
	if _, ok := u.privatePeers(playerInRegion1(), playerInRegion2()); ok {
		t.Fatalf("nil router must yield ok=false (single-Cell behavior unchanged)")
	}
}

func TestPrivatePeers_ResolvesBothRegions(t *testing.T) {
	u := newRoutingUC(t)
	u.SetCellRouter(twoRegionRouter(t, 1, 10, 2, 20))

	peers, ok := u.privatePeers(playerInRegion1(), playerInRegion2())
	if !ok {
		t.Fatalf("router set + valid players must yield ok=true")
	}
	if peers.SenderRegionID != 1 || peers.TargetRegionID != 2 {
		t.Fatalf("peers = %+v, want sender=1 target=2", peers)
	}
	if !peers.CrossRegionPrivate() {
		t.Fatalf("region1 → region2 private must be cross-region")
	}
}

func TestPrivatePeers_SameRegionNotCross(t *testing.T) {
	u := newRoutingUC(t)
	u.SetCellRouter(twoRegionRouter(t, 1, 10, 2, 20))

	// 两名玩家都落前半 → 同 region1
	peers, ok := u.privatePeers(1, 5)
	if !ok {
		t.Fatalf("ok=true expected")
	}
	if peers.CrossRegionPrivate() {
		t.Fatalf("same-region private must not be cross-region")
	}
}

func TestPrivatePeers_ZeroPlayerNotOk(t *testing.T) {
	u := newRoutingUC(t)
	u.SetCellRouter(twoRegionRouter(t, 1, 10, 2, 20))
	if _, ok := u.privatePeers(0, playerInRegion2()); ok {
		t.Fatalf("zero sender must yield ok=false")
	}
	if _, ok := u.privatePeers(playerInRegion1(), 0); ok {
		t.Fatalf("zero target must yield ok=false")
	}
}
