// W3 ④(2026-06-05)PushToPlayers helper 单测。
//
// 用 sarama/mocks.NewSyncProducer 直接注入 KeyOrderedProducer.producer 字段,
// 跳过 sarama.NewClient 真实连接 broker;**测试只能在 package kafkax 内**(访问未导出字段)。
package kafkax

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/IBM/sarama/mocks"

	"github.com/luyuancpp/pandora/pkg/config"
)

// 回归用例:KafkaConfig 未配置 Net 超时(全 0)时,buildProducerConfig 必须保留 sarama
// 30s 默认并通过 Validate();否则 ds_allocator/battle_result/team/matchmaker 的弱依赖
// producer 会因 "Net.DialTimeout must be > 0" 静默构造失败,禁用对应 kafka 事件链(不变量 §4)。
func TestBuildProducerConfig_ZeroTimeoutsUseSaramaDefaults(t *testing.T) {
	c := buildProducerConfig(config.KafkaConfig{Brokers: []string{"127.0.0.1:9093"}})

	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() with zero timeouts err=%v, want nil (应回退 sarama 默认)", err)
	}
	if c.Net.DialTimeout <= 0 || c.Net.ReadTimeout <= 0 || c.Net.WriteTimeout <= 0 {
		t.Fatalf("Net timeouts must be > 0, got dial=%v read=%v write=%v",
			c.Net.DialTimeout, c.Net.ReadTimeout, c.Net.WriteTimeout)
	}
}

// 回归用例:只配置部分超时(如 team/matchmaker 的 dial+write 缺 read)也必须通过 Validate,
// 且显式给出的值要生效。
func TestBuildProducerConfig_PartialTimeoutsOverrideAndValidate(t *testing.T) {
	cfg := config.KafkaConfig{Brokers: []string{"127.0.0.1:9093"}}
	cfg.DialTimeout = config.Duration(2 * time.Second)
	cfg.WriteTimeout = config.Duration(5 * time.Second) // 故意不配 read

	c := buildProducerConfig(cfg)

	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() with partial timeouts err=%v, want nil", err)
	}
	if c.Net.DialTimeout != cfg.DialTimeout.Std() {
		t.Fatalf("DialTimeout=%v want=%v", c.Net.DialTimeout, cfg.DialTimeout.Std())
	}
	if c.Net.WriteTimeout != cfg.WriteTimeout.Std() {
		t.Fatalf("WriteTimeout=%v want=%v", c.Net.WriteTimeout, cfg.WriteTimeout.Std())
	}
	if c.Net.ReadTimeout <= 0 {
		t.Fatalf("ReadTimeout 未配置应回退 sarama 默认 > 0,got=%v", c.Net.ReadTimeout)
	}
}

func TestBuildProducerConfig_IdempotentDefaultsRetry(t *testing.T) {
	c := buildProducerConfig(config.KafkaConfig{
		Brokers:    []string{"127.0.0.1:9093"},
		Idempotent: true,
	})

	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() with idempotent default retry err=%v, want nil", err)
	}
	if c.Producer.Retry.Max < 1 {
		t.Fatalf("Retry.Max=%d want >=1", c.Producer.Retry.Max)
	}
}

// newTestProducer 构造一个不依赖 broker 的 KeyOrderedProducer,
// producer 字段用 sarama/mocks 注入,consistent 用 4 个虚拟 partition。
func newTestProducer(t *testing.T, mp sarama.SyncProducer) *KeyOrderedProducer {
	t.Helper()
	p := &KeyOrderedProducer{
		producer:     mp,
		topic:        "pandora.team.update",
		partitionCnt: 4,
		consistent:   NewConsistent(),
	}
	for i := int32(0); i < 4; i++ {
		p.consistent.AddPartition(i)
	}
	return p
}

// 用例 1:caller 在目标列表里,必须被跳过(只发剩下 2 个)。
func TestPushToPlayers_SkipsCaller(t *testing.T) {
	mp := mocks.NewSyncProducer(t, nil)
	defer func() { _ = mp.Close() }()

	// 预期 2 次 SendMessage 成功(玩家 200/300,跳过 caller=100)
	mp.ExpectSendMessageAndSucceed()
	mp.ExpectSendMessageAndSucceed()

	p := newTestProducer(t, mp)

	sent, err := p.PushToPlayers(context.Background(), 100, []uint64{100, 200, 300}, []byte("payload"))
	if err != nil {
		t.Fatalf("PushToPlayers err=%v", err)
	}
	if sent != 2 {
		t.Fatalf("sent=%d want=2", sent)
	}
}

// 用例 2:callerPlayerID=0 时不跳过任何人(原则 3 例外,匹配进度全发)。
func TestPushToPlayers_CallerZeroSendsAll(t *testing.T) {
	mp := mocks.NewSyncProducer(t, nil)
	defer func() { _ = mp.Close() }()

	mp.ExpectSendMessageAndSucceed()
	mp.ExpectSendMessageAndSucceed()
	mp.ExpectSendMessageAndSucceed()

	p := newTestProducer(t, mp)

	sent, err := p.PushToPlayers(context.Background(), 0, []uint64{1, 2, 3}, []byte("payload"))
	if err != nil {
		t.Fatalf("PushToPlayers err=%v", err)
	}
	if sent != 3 {
		t.Fatalf("sent=%d want=3", sent)
	}
}

// 用例 3:单条失败不阻断其他玩家;返回 sent + lastErr。
func TestPushToPlayers_PartialFailureContinues(t *testing.T) {
	mp := mocks.NewSyncProducer(t, nil)
	defer func() { _ = mp.Close() }()

	// 第 1 条成功,第 2 条失败,第 3 条成功
	mp.ExpectSendMessageAndSucceed()
	mp.ExpectSendMessageAndFail(errors.New("simulated broker err"))
	mp.ExpectSendMessageAndSucceed()

	p := newTestProducer(t, mp)

	sent, err := p.PushToPlayers(context.Background(), 0, []uint64{1, 2, 3}, []byte("payload"))
	if sent != 2 {
		t.Fatalf("sent=%d want=2", sent)
	}
	if err == nil {
		t.Fatal("expected lastErr non-nil")
	}
}

// 用例 4:目标全是 caller 自己 → sent=0,err=nil。
func TestPushToPlayers_AllCallerNoSend(t *testing.T) {
	mp := mocks.NewSyncProducer(t, nil)
	defer func() { _ = mp.Close() }()
	// 不 Expect 任何 Send

	p := newTestProducer(t, mp)

	sent, err := p.PushToPlayers(context.Background(), 100, []uint64{100}, []byte("payload"))
	if err != nil {
		t.Fatalf("unexpected err=%v", err)
	}
	if sent != 0 {
		t.Fatalf("sent=%d want=0", sent)
	}
}
