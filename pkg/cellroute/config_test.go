package cellroute

import "testing"

func TestBuildRouter_OffReturnsNil(t *testing.T) {
	r, err := BuildRouter(RouterConfig{Mode: ModeOff})
	if err != nil || r != nil {
		t.Fatalf("off mode want (nil,nil), got (%v,%v)", r, err)
	}
}

func TestBuildRouter_StaticRoutesPlayer(t *testing.T) {
	cfg := RouterConfig{
		Mode:  ModeStatic,
		Cells: []CellEntry{{RegionID: 1, CellID: 10}, {RegionID: 2, CellID: 20}},
	}
	r, err := BuildRouter(cfg)
	if err != nil || r == nil {
		t.Fatalf("static build failed: %v", err)
	}
	loc, err := r.Route(12345)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if loc.RegionID == 0 || loc.CellID == 0 {
		t.Fatalf("expected mapped cell, got %+v", loc)
	}
}

func TestBuildRouter_StaticNeedsCells(t *testing.T) {
	if _, err := BuildRouter(RouterConfig{Mode: ModeStatic}); err == nil {
		t.Fatal("static without cells should error")
	}
}

func TestBuildRouter_EtcdNotInProc(t *testing.T) {
	if _, err := BuildRouter(RouterConfig{Mode: ModeEtcd, EtcdEndpoints: []string{"x:2379"}}); err == nil {
		t.Fatal("etcd mode must be built via etcdtable, expected error")
	}
}

func TestRouterConfig_Validate(t *testing.T) {
	if err := (RouterConfig{Mode: ModeOff}).Validate(); err != nil {
		t.Fatalf("off valid: %v", err)
	}
	if err := (RouterConfig{Mode: "bad"}).Validate(); err == nil {
		t.Fatal("unknown mode should fail")
	}
}

func TestMarketPeerList_Dedup(t *testing.T) {
	c := RouterConfig{MarketPeers: []string{"a", "", "a", "b "}}
	got := c.MarketPeerList()
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("dedup/trim failed: %#v", got)
	}
}

func TestMarketPeerList_IncludesSelf(t *testing.T) {
	c := RouterConfig{MarketSelf: "self", MarketPeers: []string{"a", "self", "a"}}
	got := c.MarketPeerList()
	if len(got) != 2 || got[0] != "a" || got[1] != "self" {
		t.Fatalf("self/dedup failed: %#v", got)
	}

	c = RouterConfig{MarketSelf: "self", MarketPeers: []string{"a"}}
	got = c.MarketPeerList()
	if len(got) != 2 || got[0] != "a" || got[1] != "self" {
		t.Fatalf("missing self append failed: %#v", got)
	}
}
