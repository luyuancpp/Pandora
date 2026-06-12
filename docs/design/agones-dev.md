# UE 主链路 + 本地 Agones 联调设计（W4 ⑬）

> 2026-06-09。承接「UE↔后端 gRPC-Web Login + Subscribe + Kafka Push 已通过」，推进 UE 主链路：
> **登录 → 拉/分配 Hub DS → 进大厅 → 匹配 → 拉/分配 Battle DS → 进战斗 → 结算 → 回大厅**。
>
> 本文是设计/契约层；本地 Agones 环境搭建与 apply 命令见 [`deploy/k8s/agones/README.md`](../../deploy/k8s/agones/README.md)。
> UE 侧代码在独立仓库 `Pandora-Client`，命名一律 **Pandora**。

---

## 1. 主链路全景 + 各段责任

```
[UE Client] --gRPC-Web/Envoy--> login.Login
     login --gRPC--> hub_allocator.AssignHub  ──► 真实 hub_ds_addr + hub_ticket(JWT)
[UE Client] --NetDriver--> Hub DS(进大厅, 全图自由 PvP)
     Hub DS --gRPC every5s--> hub_allocator.Heartbeat
     Hub DS --gRPC--> player_locator.SetLocation(HUB)            ← 数据面上报
[UE Client] --gRPC-Web--> matchmaker.StartMatch ... ConfirmMatch
     matchmaker --gRPC--> ds_allocator.AllocateBattle ──► 真实 battle_ds_addr
     matchmaker 签 battle_ticket + player_locator.SetLocation(MATCHING/BATTLE)
     matchmaker --kafka pandora.match.progress--> push --stream--> Client(进战斗通知)
[UE Client] --NetDriver--> Battle DS(5v5 战斗)
     Battle DS --gRPC every5s--> ds_allocator.Heartbeat
     Battle DS --kafka pandora.battle.result--> battle_result(结算 + Elo MMR)
     战斗结束 → Client 回 Hub DS, Hub DS SetLocation(HUB, fence=match_id)
```

### 各段当前状态（后端 vs UE）

| 链路段 | 后端 | UE（Pandora-Client，独立仓库）|
|---|---|---|
| 登录 gRPC-Web | ✅ login（W3）| ✅ `UPandoraBackendSubsystem.Login`（已通）|
| 分配 Hub | ✅ hub_allocator.AssignHub（W4 ⑤/⑥）+ **Agones 发现（W4 ⑬）** | ⬜ NetDriver 连 Hub DS（客户端段）|
| 进大厅 | ✅ login 返真实 hub_ds_addr（agones.enabled=true 后）| ⬜ NetDriver 连 Hub DS |
| Hub 心跳 | ✅ hub_allocator.Heartbeat | 🟡 `APandoraHubGameMode` 骨架已落（每 5s 调，§3 契约）|
| 组队 | ✅ team（W3 ⑦）| ✅ `UPandoraBackendSubsystem` 7 RPC（CreateTeam/Invite/Accept/Leave/Kick/SetReady/GetTeam，§6）|
| 匹配 | ✅ matchmaker（W4 ①/⑦）| ✅ `UPandoraBackendSubsystem` 4 RPC（StartMatch/Cancel/Confirm/GetMatchProgress，§6）|
| 分配 Battle | ✅ ds_allocator.AllocateBattle + **真 Agones（W4 ⑫）** | ⬜ NetDriver 连 Battle DS（客户端段）|
| 进战斗推送 | ✅ kafka match.progress → push stream | ✅ OnPushFrame 已通 |
| Battle 心跳 | ✅ ds_allocator.Heartbeat | 🟡 `APandoraBattleGameMode` 骨架已落（每 5s 调，§3 契约）|
| 结算 | ✅ battle_result（W4 ③/⑨）| 🟡 Battle DS 经 `ReportResult` 同步上报（§5，非 kafka）|
| locator HUB/BATTLE 上报 | ✅ guard + fence（W4 ⑩/⑪）| 🟡 Hub DS `SetLocation(HUB)` 骨架已落（带 fence，§4）|

> **结论**：后端主链路骨架已全部就位；UE DS 后端联调骨架（心跳 / SetLocation / ReportResult）
> 已在 Pandora-Client 落地（见 §5）。剩余是 (a) 本地 Agones 联调让 allocator 返回真实地址，
> (b) 内部服务前补 gRPC-Web 入口让 UE DS 客户端打通（§5.1 wiring），(c) UE NetDriver 连 DS 的客户端段。

---

## 2. Agones 两模型（后端已实现，详见 deploy README §0）

- **战斗 DS = 按需分配**：`ds_allocator/internal/data/agones_allocator.go`（W4 ⑫）POST GameServerAllocation。
- **大厅 Hub DS = 常驻分片**：`hub_allocator/internal/biz/agones_fleet.go`（W4 ⑬）LIST GameServer
  （`agones.dev/fleet=pandora-hub,pandora.dev/region=<region>`），lazy-seed 分片到 Redis。
- 两者 `agones.enabled=false` 默认走 Mock，`=true` 走真 Agones。**biz 逻辑零改**，只换 provider + main 装配。

---

## 3. DS 业务心跳上报契约（UE 侧实现）

⚠️ **Agones SDK health ≠ Pandora 业务 Heartbeat**。前者让 GameServer 进 Ready（Agones 调度用），
后者是 DS 向 allocator 上报负载/状态（容量判定 + 心跳超时补偿，不变量 §4）。UE DS 两者都要做。

### 3.1 Hub DS → `hub_allocator.Heartbeat`（每 5s 单向 unary）

`HeartbeatRequest`（`pandora/hub/v1/allocator.proto`）：

| 字段 | UE 填法 |
|---|---|
| `hub_pod_name` | Agones GameServer 名（环境变量 / SDK `GameServer().ObjectMeta.Name`）|
| `player_count` | 当前在线人数（hub_allocator 回写对账）|
| `cpu_pct` / `mem_mb` | 进程负载（可选，先填 0）|
| `state` | `"ready"` / `"draining"` / `"stopping"` |
| `ts_ms` | `now` 毫秒 |

响应 `command`：`""`=继续；`"drain"`=停止接新；`"stop"`=自行停机（孤儿分片）。

### 3.2 Battle DS → `ds_allocator.Heartbeat`（每 5s 单向 unary）

`HeartbeatRequest`（`pandora/ds/v1/allocator.proto`）：

| 字段 | UE 填法 |
|---|---|
| `ds_pod_name` | Agones GameServer 名 |
| `match_id` | 本对局 match_id（从 battle_ticket / 分配时下发取）|
| `player_count` | 当前战斗内人数 |
| `state` | `"warming"` / `"ready"` / `"running"` / `"ended"` |
| `ts_ms` | `now` 毫秒 |

响应 `command`：`""`=继续；`"stop"`=自行停机（孤儿 DS）。

> **心跳超时（默认 15s）→ allocator sweep 标记 abandoned/draining**，Battle DS abandoned 经
> `pandora.ds.lifecycle` 触发 battle_result 段位回滚补偿（W4 ⑧ at-least-once 闭环）。
>
> 补偿链两段都可在 UE DS 就绪前用 stub 端到端验：
> - 第一段（DS 心跳超时 → abandoned → `ds.lifecycle`）：`tools/scripts/ds_heartbeat_stub.ps1`
>   起 Battle DS 心跳后停掉，观察 sweep 标 abandoned + `ds_lifecycle_published`。
> - 第二段（battle_result 事务出箱 → `player.update` → player 段位回滚）：
>   `tools/scripts/battle_result_outbox_probe.ps1`（grpcurl 同步 ReportResult，验 NORMAL Elo 守恒 /
>   ABANDONED delta 全 0 / 幂等 / outbox 清零，见 W4 ⑨）。

---

## 4. player_locator HUB/BATTLE 上报闭环契约（UE 侧实现）

后端守卫（W4 ⑩/⑪）已就位：用 state 识别写入方权威，HUB 上报带 `match_id` 作 fence。
**MATCHING/BATTLE 由 matchmaker 写（控制面），HUB 由 Hub DS 写（数据面）**。UE Hub DS 负责 HUB 上报：

### 4.1 玩家进 Hub DS → `player_locator.SetLocation(HUB)`

`SetLocationRequest{ player_id, Location{ state=LOCATION_STATE_HUB, hub_pod, shard_id, match_id } }`：

| 场景 | `match_id` 填法（fence 令牌）|
|---|---|
| 全新登录进大厅 | `0`（无来源对局）|
| **战斗结束回大厅** | **填刚结束那场的 match_id**（从 battle DSTicket 取）|

- 后端 guard：`cur=BATTLE` 时，仅当 `in.match_id == cur.match_id && != 0` 才允许 `BATTLE→HUB`
  回流（合法）；不匹配/为 0 = stale hub DS 顶 active BATTLE → 拒 `ERR_LOCATOR_CONFLICT=9202`。
- 后端持久化 HUB 记录时**清零 match_id/battle_pod**（fence 仅供判定，进 HUB 后无活跃对局）。
- `cur=MATCHING`（确认期 ~15s）时 HUB 上报一律拒（玩家物理上还连着 hub，但权威态是 MATCHING）。

### 4.2 时序要点（避免顶号冲突）

- Hub DS 进大厅那刻才 SetLocation(HUB)，不要在 matchmaker 写 MATCHING 后还重复刷 HUB。
- 战斗结束回流必须带 fence match_id，否则会被后端正确拒绝（这是防 stale 的设计，不是 bug）。

---

## 5. UE Hub DS / Battle DS 骨架要点（Pandora-Client 仓库，命名 Pandora）

> 仅列后端联调相关的骨架职责；GAS / Iris / Replication 细节见 `ds-arch.md`。

**🟡 已落地（2026-06-09，Pandora-Client `Source/Pandora/`）**：

| 文件 | 职责 |
|---|---|
| `Public/Net/PandoraDSBackendSubsystem.h` + `Private/Net/...cpp` | DS→后端 4 个 unary（HubHeartbeat / BattleHeartbeat / SetLocationHub / ReportBattleResult），复用 gRPC-Web codec |
| `Public/Server/PandoraAgonesProvider.h` + cpp | Agones 身份/生命周期桩（读 env：`AGONES_GAMESERVER_NAME` / `PANDORA_MATCH_ID` / `PANDORA_REGION`），Ready/Health/Shutdown 占位 |
| `Public/Server/PandoraHubGameMode.h` + cpp | 大厅 DS：5s 心跳 + PostLogin 落 `SetLocation(HUB)`（带 fence match_id，§4） |
| `Public/Server/PandoraBattleGameMode.h` + cpp | 战斗 DS：5s 心跳 + `ReportResultAndEndMatch`（结算同步上报，不报 mmr_delta，§6） |

- **模块**：当前暂放客户端模块 `Source/Pandora/`（M1.5 服务端模块未拆）；后续按 CLAUDE §11.3 迁
  `PandoraHubServer` / `PandoraBattleServer`。`UPandoraDSBackendSubsystem::ShouldCreateSubsystem`
  门控 `IsRunningDedicatedServer()`，客户端不背 DS 逻辑。
- **传输方案（与原契约偏差，刻意为之）**：原 §5 设想 DS 走**标准 gRPC**；但原生 gRPC 需引入
  grpc-cpp（80MB+）并改 UE 构建环境，触碰「客户端/DS 零额外依赖」铁律（CLAUDE §12）+ Claude 不动
  构建环境（AGENTS §11.1）。故 DS 复用**已有 gRPC-Web codec**（`FPandoraProtoWriter` +
  `FPandoraGrpcWeb` + `FHttpModule`），与客户端 `UPandoraBackendSubsystem` 同源、零新依赖。
  代价：见 §5.1 需要 grpc-web 入口 wiring。原生 gRPC 路线留作未来可选项（抽象在 subsystem 后，可换）。
- **Agones SDK**：当前是 env 桩（`FPandoraAgones`），非真 SDK。真 SDK.Ready()/Health()/WatchGameServer
  接入时替换桩实现，GameMode 调用点不变。
- **GameServer 名 / match_id**：桩从 env 读（Agones downward API / allocation label 透传）。
- **玩家身份**：Hub DS 从 ClientTravel URL option（`?PlayerId=&FenceMatchId=`）解析（骨架）；
  真实部署应改为校验 hub DSTicket(JWT) 取 player_id（DS 不可信 URL）。
- **占位验证**：UE DS 就绪前，先用 `deploy/k8s/agones` 的 simple-game-server 占位 Fleet 验
  Agones 分配链路（见 README §4 第一步）；心跳 / locator 链路用 `tools/scripts/ds_heartbeat_stub.ps1`
  当 stub（grpcurl 周期调 Heartbeat + SetLocation，第二步），真 UE DS 就绪后替换。
  战斗结算 → 段位补偿链用 `tools/scripts/battle_result_outbox_probe.ps1`（grpcurl 同步
  ReportResult + GetMatchResult，验事务出箱 → player.update → 段位回写）。

### 5.1 DS gRPC-Web 入口 wiring（运维步骤，Codex 联调时落地）

UE DS 客户端走 gRPC-Web，但内部服务（hub_allocator :50021 / ds_allocator :50020 /
player_locator :50006 / battle_result :50022）裸端口是**原生 gRPC**（HTTP/2 framing），
gRPC-Web 报文打不通。要让 UE DS 端到端跑通，需在内部服务前补一层 grpc_web 转换，三选一：

- **方案 A（推荐）**：给 Envoy 增加到这 4 个内部服务的 grpc_web route（后端 yaml 改动，
  可由后端侧落地），UE DS `SetEndpoints` 指向 Envoy 入口（如 `127.0.0.1:8443` + 路径路由
  或各服务独立 listener）。
- **方案 B**：每内部服务挂一个 grpcwebproxy / Envoy sidecar，UE DS 指向各 sidecar。
- **方案 C（长期）**：DS 换原生 gRPC（引 grpc-cpp，触碰零依赖，暂不做）。

`UPandoraDSBackendSubsystem` 的 4 个 Endpoint 默认填裸 gRPC 端口（占位），Codex 联调时经
`SetEndpoints` 或 Game.ini Config 覆盖成实际 grpc-web 入口地址。`bUseTls` 控制 http/https。

---

## 6. 阶段限制（留后续）

- **Battle DS 结算走同步 `ReportResult` gRPC，非 kafka `pandora.battle.result`**（§1 图里画的是 kafka）：
  UE 直接生产 kafka 较重，改用 battle_result 已有的同步兜底 RPC（`battle_result_outbox_probe.ps1` 用的同款），
  复用同一 gRPC-Web 客户端、更轻。落库幂等 + 事务出箱 + Elo 重算逻辑后端不变（不变量 §2/§6）。
- **UE DS 走 gRPC-Web 而非原生 gRPC**（§5 传输方案偏差）：需 §5.1 grpc-web 入口 wiring 才能端到端，
  否则 DS 的 4 个 unary 打不通内部裸 gRPC 端口。
- **Agones 为 env 桩非真 SDK**（`FPandoraAgones`）：Ready/Health/Shutdown 仅日志，GameServer 名/match_id 读 env。
- **玩家身份从 URL option 解析非校验 DSTicket**：Hub DS 骨架先信 `?PlayerId=`，真实部署须校验 hub JWT。
- hub_allocator `AgonesHubFleetProvider` 只在 region 首次无分片时 lazy-seed，Fleet 扩缩容后新
  GameServer 不自动发现（周期性 reconcile 留后续）。
- 占位镜像不发业务心跳，心跳超时 sweep / locator 上报闭环须真 UE DS 或 stub 才能端到端验。
- D7（k8s 选型：ACK / 自建 / minikube）仍未拍板；当前按 minikube + Agones dev 推进，代码 provider 无关不受阻。
- 真集群（指向真 Agones）联调 + UE DS 落地后，更新本文与 PROGRESS。

---

## 7. UE 客户端 组队 / 匹配 gRPC-Web API（Pandora-Client，命名 Pandora）

> ⚠️ 区分：§5 是 **DS 侧**（Hub/Battle DS → 内部服务）；本节是 **客户端侧**（玩家
> → Envoy:8443 → team/matchmaker），两条链路不同。组队/匹配是玩家发起的、带
> SessionToken 鉴权的客户端调用，**不在 DS 子系统**，落在
> `UPandoraBackendSubsystem`（与 Login/Subscribe 同一客户端子系统）。

**落地（Source/Pandora/{Public,Private}/Net/PandoraBackendSubsystem.{h,cpp}）**：

- 复用零依赖 gRPC-Web codec（`FPandoraProtoWriter/Reader` + `FPandoraGrpcWeb`），
  经 `MakeGrpcWebRequest(FullMethod, Bytes, bWithAuth=true)` 带 `Authorization: Bearer <SessionToken>`。
- **组队 7 RPC**（`pandora.team.v1.TeamService`）：`CreateTeam` / `InviteToTeam(TeamId,Target)` /
  `AcceptTeamInvite(TeamId,InviteId)` / `LeaveTeam(TeamId)` / `KickFromTeam(TeamId,Target)` /
  `SetTeamReady(TeamId,bReady,HeroId)` / `GetTeam(TeamId)`。结果走 `OnTeamResult`（含 `FPandoraTeam`）。
  team 写 RPC 的 player_id 以 JWT sub 为准（请求体不传，方法签名不暴露）。
- **匹配 4 RPC**（`pandora.match.v1.MatchService`）：`StartMatch(TeamId)`（→ `OnStartMatchComplete` 带 MatchId）/
  `CancelMatch(MatchId)` / `ConfirmMatch(MatchId,bAccept)`（→ `OnMatchActionComplete`）/
  `GetMatchProgress(MatchId)`（→ `OnMatchProgress` 带 `FPandoraMatchProgress`）。
- **匹配进度主驱动是 push**：`pandora.match.progress` kafka → push server stream → `OnPushFrame`，
  `GetMatchProgress` 仅作主动轮询兜底。`FPandoraMatchProgress.TeamA/TeamB` 解 packed repeated uint64。

**Envoy wiring（已落地，本仓库 `deploy/envoy/envoy.yaml`）**：与 §5.1 DS 入口尚未 wiring 不同，
客户端组队/匹配的 Envoy 路由**已补齐**：

- `team_cluster`（→ :50010）+ `/pandora.team.v1.TeamService/` route，jwt_authn 要 `pandora_session`（W3 已有）。
- 本轮新增 `match_cluster`（→ :50011）+ `/pandora.match.v1.MatchService/` route + jwt_authn `pandora_session` 规则。
- 故玩家 `UPandoraBackendSubsystem` 经 Envoy:8443 调组队/匹配端到端可通（需 login 拿到 SessionToken 在先）。

**阶段限制**：UE 编辑器编译验证 + 端到端联调留 Codex/人（独立仓库，需 UE 5.7 编辑器）。
