# Pandora 配置表热更流水线

> **状态**:方向已定,实现待排期(2026-06-30 记录)
> **本文档地位**:策划配置表(`Table`)→ JSON → 服务端热加载 的契约与目录约定。
> **关联规范**:`CLAUDE.md §5`(proto 优先 / 配置表 ID `uint32`)、`infra.md`(资源命名)、`pandora-arch.md §11`(决策行)。
> **一句话**:这是**配置发布 / 热更流水线**,不是分布式配置中心;Apollo / Nacos **现在不接**,以后只可能当"发布通知 / 版本号",不当大量表 JSON 的主存储。

## §0 核心做法(最重要,别忘)

**版本号 + checksum + staging 目录 + reload 接口 + 加载成功才切换 + 失败保留旧配置 —— 这套就是游戏配置表热更的标准做法,直接照做。**

| 环节 | 作用 | 本项目落点 |
|---|---|---|
| **版本号** | 单调递增,防回退/重放,可追溯回滚 | manifest `version`(§5) |
| **checksum** | 逐表内容哈希,防拷贝截断/篡改 | manifest 每表 `checksum`(§5) |
| **staging 目录** | 新批次先落地、不碰线上 | `staging/` → 校验通过才进 `active/`(§4) |
| **reload 接口** | 受控触发加载,不对客户端开放 | etcd 版本键 watch(多机)/ gRPC RPC(单机)(§6) |
| **加载成功才切换** | 新表加载+校验全过,才原子替换内存指针 | `atomic.Pointer` / `AtomicTable`(§3.1) |
| **失败保留旧配置** | 任一步失败不切换,线上不受影响 | 失败回退 + 告警(§3.1) |

**发布通知用 etcd**(复用现有 `pkg/cellroute/etcdtable`、`pkg/snowflake/etcdnode`),etcd 只存 `version` 键不存表体;单机/dev 连 etcd 都不必,直接调 reload RPC。**不引入 Apollo/Nacos。**

## §1 场景与定位

```
F:\work\Pandora-Client-SVN\Table        # 策划源表(Excel / CSV / 自定义表)
        │  ① 工具生成 + 校验
        ▼
        *.json                            # proto 字段对齐的 JSON 产物
        │  ② 发布(版本号 + checksum + staging)
        ▼
F:\work\XuanMing-Server  服务端           # ③ 热加载:先加载新表 → 校验 → 原子切换 → 失败保留旧表
```

本质是**游戏/业务配置表热更新流水线**,不是「服务一启动就读一次」的静态配置,也不是分布式动态配置中心。

## §2 要不要接 Apollo / Nacos —— 现在不接

**结论:现阶段不上 Apollo / Nacos / Consul 当核心。** 理由:

- 本场景核心是「大量表 JSON 的生成、校验、发布、热加载」,Apollo / Nacos 擅长的是「少量键值配置 + 实时推送 + 权限审批」,不匹配。
- 重型平台引入运维成本(部署、依赖、SDK 接入),当前单机 / dev 阶段是负收益。
- 开源库**用在局部**是合理的(读 Excel/CSV、JSON schema 校验、文件监听、diff、HTTP reload 客户端),这不等于「不用开源库」。

### §2.1 什么时候才考虑接(触发条件)

满足下列**任意一条**且确有痛点时,再评估引入,且只让它管「发布通知 / 当前版本号 / 多机统一刷新」,**不让它存大量表 JSON**:

1. 多环境配置分治需要平台化管理(dev / test / prod 不只是目录区分)。
2. 需要多人权限、发布审批、操作审计。
3. 需要 Web 控制台改配置 + 一键回滚 + 灰度发布。
4. 多台服务器要「一次发布、统一收到刷新通知」,且 §3 的自研通知方式扛不住。

> 注:多机统一刷新这一条,本项目已有 `etcd watch`(见 `pkg/cellroute/etcdtable`、`pkg/snowflake/etcdnode`)可复用作"发布通知 / 版本号广播",**优先用现有 etcd,而不是新引入 Apollo / Nacos**。

## §3 流水线设计(自研轻量版)

1. **生成**:工具读 `Table` 源表 → 输出 proto 字段对齐的 `*.json`。
2. **校验**:生成阶段严格校验(字段名、类型、枚举、引用完整性、未知字段一律报错),不通过不产出。
3. **版本**:产出 `version`(单调递增 / 内容哈希)+ 每个文件 `checksum`,写一个 manifest。
4. **发布**:产物拷贝到服务端 **staging 目录**(不直接覆盖线上目录)。
5. **通知**:调服务端 `reload` 接口 / 写 etcd 版本号触发 watch。
6. **热加载**:服务端先把新表加载进**临时结构**并校验通过,再**原子切换**内存指针(参考 `pkg/cellroute` 的 `AtomicTable` 整表替换思路)。
7. **失败回退**:加载 / 校验任一步失败,保留旧表,不影响线上,记日志告警。

### §3.1 不变量

- **加载成功才切换**:任何一张表加载或校验失败,整批不切换(或按表粒度回退),线上始终是一份完整自洽的配置。
- **原子整表替换**:运行时读配置走原子指针,不做"边读边改",避免读到半截表。
- **版本可追溯**:每次生效记录 version + checksum,便于回滚定位。

## §4 目录约定

源表在 UE 客户端仓库,产物落后端仓库。各阶段目录职责单一,**不允许 ad-hoc 路径**(对齐 `infra.md` 命名总则)。

```
# ① 源表(UE 客户端仓库,SVN)
F:\work\Pandora-Client-SVN\Table\
    hero.xlsx                       # 策划维护的源表(Excel / CSV / 自定义)
    skill.xlsx
    item.xlsx
    ...

# ② 生成产物(后端仓库,git 跟踪;proto 字段对齐的 JSON + manifest)
F:\work\XuanMing-Server\configtable\dist\
    manifest.json                   # 本批次清单(version + 每表 checksum),见 §5
    hero.json
    skill.json
    item.json
    ...

# ③ 服务端运行态目录(git 不跟踪,发布时落地)
<deploy-root>\configtable\
    staging\                        # 新批次先落这里,未生效
        manifest.json
        *.json
    active\                         # 当前生效批次(热加载从这里读 / 切换后指向这里)
        manifest.json
        *.json
    history\                        # 旧批次留档,按 version 命名,供回滚
        v<version>\
```

约定:

- **源表只在 SVN 仓库**,后端仓库**不放 Excel 源表**,只放生成出来的 JSON(可审 diff)。
- **文件名 = 表名(snake_case)**,一表一文件,文件名与 proto message / 加载注册键一一对应。
- `staging` / `active` / `history` 三段分离:发布只写 `staging`,生效才原子切到 `active`,旧批次进 `history`。

## §5 manifest 契约

每批产物带一个 `manifest.json`,是发布与热加载的**唯一权威清单**(服务端以它为准决定加载哪些表、校验是否完整):

```jsonc
{
  "version": 20260630_001,          // 单调递增版本号(日期+序号 或 纯自增)
  "generated_at_ms": 1751270400000, // 生成时间(毫秒)
  "generator": "configtable-gen@1.0.0",
  "source_rev": "svn-r12345",       // 源表 SVN 版本,便于追溯
  "tables": [
    {
      "name": "hero",               // 表名 = 文件名(去 .json)= 注册键
      "file": "hero.json",
      "proto": "pandora.config.HeroTable", // 对应 proto message 全名
      "checksum": "sha256:ab12...",  // 文件内容哈希
      "rows": 128                    // 行数,加载后断言一致
    }
    // ... 其余表
  ]
}
```

不变量:

- 服务端**只加载 manifest 列出的表**;`active` 目录里有 manifest 之外的文件 = 视为脏数据,告警。
- 加载前**逐表校验 checksum**,不匹配整批拒绝(防止拷贝过程被截断)。
- `version` **单调递增**;收到的 version ≤ 当前 active version 时拒绝(防回退 / 重放),除非显式回滚指令。

## §6 reload 接口契约

服务端暴露一个**受控的** reload 入口(运维 / 调试用,**不对客户端 / Envoy 开放**,对齐 `CLAUDE.md §5.11` 例外条款:鉴权 + 不经 Envoy)。

- **触发方式二选一(可并存)**:
  1. **etcd 版本键**:发布方写 `pandora/configtable/version` = 新 version,服务端 watch 到变更后自行从 `staging` 拉取加载。**多机统一刷新优先用这个**(复用现有 `pkg/cellroute/etcdtable` 模式)。
  2. **gRPC reload RPC**:`ReloadConfigTable(version)` → 服务端校验 + 加载 + 原子切换。单机 / dev 直接调。
- **语义**:
  - **幂等**:同一 version 重复 reload 不产生副作用(已生效则直接返回当前状态)。
  - **同步返回加载结果**:成功返回生效 version;失败返回错误原因(哪张表、哪行、何种校验失败),**且不切换**(保留旧表)。
  - **原子切换**:加载进临时结构 → 全表校验通过 → 原子替换内存指针(`atomic.Pointer` / 参考 `pkg/cellroute` 的 `AtomicTable`)。
- **响应/请求结构**:按 `CLAUDE.md §5` 用 proto message 定义(`ReloadConfigTableRequest` / `ReloadConfigTableResponse`),不手写并行 struct。

## §7 校验清单(生成阶段严格执行)

生成器在产出 JSON 前**全部通过才落盘**,任一失败则整批不产出并报错定位到「表 + 行 + 列」:

1. **字段名对齐**:列名 → proto 字段名映射完整,无拼写错;`protojson` 未知字段一律报错(见 §8.3)。
2. **类型合规**:数值 / 布尔 / 字符串类型匹配 proto;配置表 ID 用 `uint32`(`CLAUDE.md §5.6`)。
3. **枚举合法**:枚举列取值在 proto enum 定义内,统一用名字或数字一种。
4. **主键唯一**:每表主键(如 `hero_id`)无重复。
5. **引用完整性(外键)**:跨表引用的 ID 必须在被引用表中存在(如 `hero.skill_id` 必须在 `skill` 表里)。
6. **非空 / 范围**:必填列不为空;有范围约束的数值在合法区间。
7. **行数一致**:产出行数写入 manifest,服务端加载后断言一致。

## §8 用 proto 读 JSON 的三个硬约束

方向正确(契合 `CLAUDE.md §5.8`「新增结构优先 proto、不写并行 struct」):给每张表定义 proto message,JSON 用 `protojson.Unmarshal` 读入 proto 结构。但 `protojson` ≠ 任意 JSON,生成器必须钉死下列三件事:

1. **字段名对齐**:`protojson` 认 `json_name`(默认 camelCase)与 proto 原名;生成 JSON 的 key **必须与 proto 字段名 / `json_name` 完全一致**,团队统一一种(建议 proto 原名 snake_case),否则字段静默丢失或报错。
2. **64 位整数是字符串**:proto3 JSON 规范规定 `uint64` / `int64` 序列化为 **string**(如 `"123"`)。配置表 ID 按 `CLAUDE.md §5.6` 默认 `uint32`,**不踩这个坑**;若某表确需 `uint64`,JSON 必须写成字符串。
3. **未知字段策略**:`protojson` 默认遇到多余字段**报错**——这对生成阶段校验是好事(能抓出列名写错)。**生成 / 发布阶段严格(不容忍未知字段);运行时加载如需向前兼容,显式 `DiscardUnknown: true`**。

补充:枚举可用名字或数字,团队统一一种;repeated / 嵌套 message 适合带子结构的表。

## §9 决策小结

- 配置表热更 = **自研轻量流水线**(生成→校验→版本→staging→通知→原子热加载→失败回退)。
- Apollo / Nacos **不作为核心**;未来若需「发布通知 / 版本号 / 多机刷新」,**优先复用现有 etcd watch**,Apollo / Nacos 仅在 §2.1 触发条件成立时再评估,且只管元数据不存表体。
- proto 读 JSON 落地前,生成器钉死「字段名对齐 / 64 位整数字符串 / 未知字段策略」三件事。

## §10 落地任务清单(实现时按序做,均待排期)

> 当前仅设计草稿,**未写代码**。实现归属按 `AGENTS.md §11.1`:业务/生成器/加载器逻辑由 Claude 实现+验证,环境/SVN/git 收尾由 Codex/人。

1. [ ] 定义各表 proto message(`proto/pandora/config/*.proto`,message 全名进 manifest 的 `proto` 字段)。
2. [ ] `configtable-gen` 生成器:读 `Table` 源表 → §7 校验 → 输出 `dist/*.json` + `manifest.json`。
3. [ ] 服务端加载器:读 `active/manifest.json` → 逐表 `protojson.Unmarshal`(`DiscardUnknown` 运行时按需)→ 原子整表替换。
4. [ ] reload 入口:etcd 版本键 watch(多机)+ gRPC `ReloadConfigTable`(单机/dev),§6 语义。
5. [ ] staging → active 原子切换 + history 留档 + 回滚命令。
6. [ ] 发布脚本(`tools/scripts/`):dist → 服务端 staging 拷贝 + 触发 reload。

