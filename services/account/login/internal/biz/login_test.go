// login_test.go — LoginUsecase.resolveHub 行为单测(W4 ⑥,2026-06-06)。
//
// 覆盖 hub_allocator 弱依赖三态:
//   - hubAssigner 非 nil 且 AssignHub 成功 → 用 allocator 返回的 hub_ds_addr + hub_ticket
//   - hubAssigner 为 nil → 回退自签 hub 票据 + 静态 hubDSAddr
//   - hubAssigner 返回错误 → 回退自签(不阻断登录)
package biz

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/luyuancpp/pandora/pkg/auth"
	"github.com/luyuancpp/pandora/pkg/passwd"
	"github.com/luyuancpp/pandora/pkg/snowflake"
	"github.com/luyuancpp/pandora/services/account/login/internal/data"
)

const testSecret = "pandora-dev-jwt-secret-change-me-32!" // 36 字节,满足 HS256 ≥32

// mustBcrypt 用 DevCost 哈希明文密码,失败 fatal。
func mustBcrypt(t *testing.T, plain string) string {
	t.Helper()
	h, err := passwd.Hash(plain, passwd.DevCost)
	if err != nil {
		t.Fatalf("passwd.Hash: %v", err)
	}
	return h
}

// ---- fakes ----

type fakeAccountRepo struct {
	playerID     uint64
	passwordHash string
	banned       bool
}

func (f *fakeAccountRepo) FindByAccount(_ context.Context, _ string) (uint64, string, error) {
	return f.playerID, f.passwordHash, nil
}
func (f *fakeAccountRepo) CreateAccount(_ context.Context, _ uint64, _, _ string) error { return nil }
func (f *fakeAccountRepo) CheckBanned(_ context.Context, _ uint64, _ string) (bool, error) {
	return f.banned, nil
}
func (f *fakeAccountRepo) TouchDevice(_ context.Context, _ uint64, _ string) error { return nil }

type fakeSessionRepo struct{}

func (fakeSessionRepo) Set(_ context.Context, _ uint64, _, _, _ string, _ time.Duration) error {
	return nil
}
func (fakeSessionRepo) Delete(_ context.Context, _ uint64) error { return nil }

type fakeHubAssigner struct {
	res *data.HubAssignment
	err error

	gotPlayerID uint64
	gotRegion   string
	gotTeamID   uint64
}

func (f *fakeHubAssigner) AssignHub(_ context.Context, playerID uint64, region string, teamID uint64) (*data.HubAssignment, error) {
	f.gotPlayerID = playerID
	f.gotRegion = region
	f.gotTeamID = teamID
	if f.err != nil {
		return nil, f.err
	}
	return f.res, nil
}

// newTestUsecase 构造一个登录用例(密码 bcrypt 校验在 biz 之外,这里直接给明文等值匹配)。
func newTestUsecase(t *testing.T, hub data.HubAssigner) *LoginUsecase {
	t.Helper()
	cfg := auth.Config{Secret: []byte(testSecret)}
	signer, err := auth.NewSigner(cfg)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	verifier, err := auth.NewVerifier(cfg)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	// bcrypt 哈希一个固定密码 "pw",让 passwd.Verify 通过。
	hash := mustBcrypt(t, "pw")
	repo := &fakeAccountRepo{playerID: 42, passwordHash: hash}
	sf := snowflake.NewNode(1)
	return NewLoginUsecase(repo, fakeSessionRepo{}, nil, hub, sf, "127.0.0.1:7777", "cn", signer, verifier)
}

func TestLogin_HubAssignerSuccess(t *testing.T) {
	hub := &fakeHubAssigner{res: &data.HubAssignment{
		HubDSAddr:  "10.0.0.9:7777",
		HubTicket:  "", // 见下:用真实签名替换以便 verifier 能解析 exp
		HubPodName: "pandora-hub-cn-2",
		ShardID:    2,
	}}
	uc := newTestUsecase(t, hub)

	// 用 uc.signer 真实签一张 hub 票据塞进 allocator 返回,模拟 hub_allocator 用共享 secret 签的票。
	tk, _, err := uc.signer.SignDSTicket(42, auth.DSTypeHub, 0, "jti-hub")
	if err != nil {
		t.Fatalf("sign hub ticket: %v", err)
	}
	hub.res.HubTicket = tk

	res, err := uc.Login(context.Background(), "acc", "pw", "dev-1")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if res.HubDSAddr != "10.0.0.9:7777" {
		t.Errorf("HubDSAddr = %q, want allocator addr", res.HubDSAddr)
	}
	if res.HubTicket != tk {
		t.Errorf("HubTicket not the allocator-signed ticket")
	}
	if res.HubTicketExpMs <= 0 {
		t.Errorf("HubTicketExpMs = %d, want >0 (parsed from ticket)", res.HubTicketExpMs)
	}
	if hub.gotPlayerID != 42 || hub.gotRegion != "cn" || hub.gotTeamID != 0 {
		t.Errorf("AssignHub args = (%d,%q,%d), want (42,\"cn\",0)", hub.gotPlayerID, hub.gotRegion, hub.gotTeamID)
	}
}

func TestLogin_HubAssignerNil_FallbackSelfSign(t *testing.T) {
	uc := newTestUsecase(t, nil)

	res, err := uc.Login(context.Background(), "acc", "pw", "dev-1")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if res.HubDSAddr != "127.0.0.1:7777" {
		t.Errorf("HubDSAddr = %q, want static fallback addr", res.HubDSAddr)
	}
	// 自签票据应能被 verifier 验通过且是 hub 类型。
	claims, verr := uc.verifier.VerifyDSTicket(res.HubTicket)
	if verr != nil {
		t.Fatalf("self-signed hub ticket not verifiable: %v", verr)
	}
	if claims.DSType != string(auth.DSTypeHub) || claims.PlayerID() != 42 {
		t.Errorf("self-signed ticket claims = (%s, pid=%d), want (hub, 42)", claims.DSType, claims.PlayerID())
	}
}

func TestLogin_HubAssignerError_FallbackSelfSign(t *testing.T) {
	hub := &fakeHubAssigner{err: errors.New("hub_allocator down")}
	uc := newTestUsecase(t, hub)

	res, err := uc.Login(context.Background(), "acc", "pw", "dev-1")
	if err != nil {
		t.Fatalf("Login should fall back, got err: %v", err)
	}
	if res.HubDSAddr != "127.0.0.1:7777" {
		t.Errorf("HubDSAddr = %q, want static fallback addr on AssignHub error", res.HubDSAddr)
	}
	if _, verr := uc.verifier.VerifyDSTicket(res.HubTicket); verr != nil {
		t.Fatalf("fallback hub ticket not verifiable: %v", verr)
	}
}
