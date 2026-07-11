# DAIMON — 重铸蓝图

> 性质: 全权改造的设计蓝图。记录目标态与设计依据；当前行为以 `docs/architecture/` 的 as-built 文档为准。
> 日期: 2026-06-12
> 基线: `main` @ 63166d2（约 42k LOC Go，26 个迁移，session/plan/Reflexion/taskruntime/workflow 均已在线）
> 目标: 把一个 coding-agent runtime 重铸为主权个人代理（sovereign personal agent），探寻 agent 极限实践。

---

## 目录

0. [第三轮思考：对前两轮结论的修正](#0-第三轮思考对前两轮结论的修正)
1. [命名](#1-命名)
2. [宪法：七条不变量](#2-宪法七条不变量)
3. [总体架构与包布局](#3-总体架构与包布局)
4. [模块详细设计](#4-模块详细设计)
5. [数据层总览](#5-数据层总览)
6. [现有代码处置清单](#6-现有代码处置清单)
7. [实施路径](#7-实施路径)
8. [北极星指标](#8-北极星指标)
9. [风险登记册](#9-风险登记册)
10. [第一天清单](#10-第一天清单)

---

## 0. 第三轮思考：对前两轮结论的修正

重新评估后，五处修正。其余结论（事件有机体、无状态情节、世界模型、可逆性行动层、睡眠整固、回放评测、预期引擎、影子脑）维持原判。

### 修正一：session 缓刑，不处死

前两轮说"杀 session 中心抽象"。复查后修正：**杀的是中心地位，不是表本身。**
聊天仍是事件源之一，用户期待对话连续性；`sessions` 表降格为聊天事件源的 transcript 存储，由聊天触发的情节在组装上下文时引用最近 transcript 片段。`session.Manager` 保留瘦身版，从 agent 主路径上摘除。

### 修正二：裸 ReAct 加两个框架强制例外

前两轮说"剥掉一切认知脚手架"。修正为：**不替模型思考，但强制模型交账。** 两个例外保留框架强制：

1. **退出契约**：情节必须以结构化 `Outcome` 收尾（schema 校验），否则架构失忆。这是整个重组范式的承重墙，必须框架强制。
2. **客观验证**：当行动有客观判据（测试、编译、断言）时，框架直接跑判据，不是"提示模型去验"。

`plan` 工具不删除，降级改造：从"注入 system prompt 的会话状态"变为"世界模型事项层的普通读写工具"——模型想做计划就写进事项账本，框架不再每轮注入、不再 Reflexion 提示（`internal/agent/reflection.go` 删除）。

### 修正三：注意力路由分两步，不被本地模型阻塞

本地小模型（llama.cpp/ollama）是新依赖、集成摩擦大。修正：Phase 2 用 **规则引擎 + haiku 档云模型** 上线路由（每次分诊 <0.01 元，可接受），本地模型推迟到 Phase 4 作为成本优化项。路由是架构问题，不是部署问题。

### 修正四：沙箱捡回来，分域安全

第一轮说"可逆性 > 沙箱"，把 P0-3 几乎否掉。修正为**分域**：

- **生活域**（邮件/消息/日历/支付）：沙箱无意义 → 可逆性分类 + 信任账本 + hold 队列。
- **代码域**（bash/file 工具）：可逆性难定义（`rm` 不可逆且无 hold 可言）→ OS 沙箱（macOS Seatbelt）是正解，远程触发（Telegram/timer）的 bash 强制走沙箱档。

两套机制统一挂在行动层下，按工具域分流。

### 修正五：不开新仓库，原地换心脏（绞杀者模式）

复查代码后发现可征用资产远多于预想：`task_ledger`/`task_checkpoints`（019/018）、`execution_events`（020）、`agent_replays`（021）、`temporal_facts`（023）、`memory/autobiographical.go`、workflow step-cache（026）、`telemetry/jsonl.go`。回放评测需要持续运行的真实数据流——推倒重写会中断数据积累。结论：**新包在旁边长，gateway 逐步换接线，旧路径跑到新路径验收通过再拆。** 每个 Phase 结束时二进制都可用。

---

## 1. 命名

### 定名：**Daimon**（δαίμων）

三重契合，缺一个都只是好听：

1. **daemon（双关一）**：unix 常驻后台进程。这个代理字面上就是一个 daemon——永远在跳的事件心脏。技术身份和名字同构。
2. **daimonion（双关二）**：苏格拉底的守护灵——只对一个人低语、只忠于一个人的内在声音。主权个人代理的本质：单一委托人、结构性忠诚、local-first。
3. **论题同构**：希腊人认为 daimon 是人格中不死的部分。本蓝图的中心命题正是 agent = 不死的部分（传记/信任/技能/价值观），model = 会换的认知引擎。

落地：二进制 `daimon`，用户目录 `~/.daimon`，模块路径 `github.com/<owner>/daimon`。`ironclaw` 二进制名保留一个 Phase 作为兼容软链。

备选（记录备查）：**Anima**（荣格：使身体活着的灵魂，"that which animates"）、**Famulus**（魔法师的随从精灵）、**Keel**（龙骨——换桅换帆不换龙骨）。均不及 Daimon 的 daemon 双关。

---

## 2. 宪法：七条不变量

每个模块设计必须同时满足，冲突时按序号优先：

1. **状态在外**：一切持久信息住在世界模型（磁盘/SQLite），上下文是从状态重组出来的缓存，任何时刻丢弃上下文不丢失事实。
2. **换脑无感**：任何模块不得依赖特定模型的行为特征；模型 ID 只出现在 `mind` 包和配置里。判据：换模型 → 回放评测回归数为零。
3. **交账强制**：每个情节必须以 schema 校验的 Outcome 收尾；每个行动必须留下 Receipt。无账不行动。
4. **可逆优先**：行动默认按最保守类别处理，升级自治必须有信任账本的战绩支撑；不可逆高风险永远人签。
5. **认知是贵的**：任何"每个事件都调一次大模型"的设计直接否决；成本必须逐级过滤（规则 → 小模型 → 大模型）。
6. **本地主权**：传记数据不出本机（API 调用中的瞬时上下文除外）；全部行动可审计、可回放。
7. **不替模型思考**：禁止新增"注入提示替模型规划/反思"类机制（修正二的两个例外除外）。模型变强时，runtime 不应变成阻力。

---

## 3. 总体架构与包布局

```
        ┌──────────────────────────────────────────────────────┐
        │ heart 事件心脏                                         │
        │ telegram / mail / calendar / fs / timer / webhook / tui│
        └────────────────────────┬─────────────────────────────┘
                                 ▼ Event(持久化后路由)
        ┌──────────────────────────────────────────────────────┐
        │ attention 注意力路由   rules → haiku档 → 默认Cognize    │
        └──────┬──────────────────────────┬────────────────────┘
               ▼ Cognize(~1%)             ▼ Reflex(~99%)
        ┌─────────────────┐       ┌─────────────────────┐
        │ episode 情节内核 │       │ skill/workflow 反射  │
        │ 组装→裸ReAct→交账│       │ (蒸馏产物,确定性)     │
        └──────┬──────────┘       └──────────┬──────────┘
               ▼ 一切行动经过                  ▼
        ┌──────────────────────────────────────────────────────┐
        │ action 行动层                                          │
        │ values→trust→classify→hold→execute→undo→verify→audit  │
        └────────────────────────┬─────────────────────────────┘
                                 ▼ 读写
        ┌──────────────────────────────────────────────────────┐
        │ world 世界模型 (唯一事实源)                             │
        │ identity文件层 · commitments账本层 · journal日志层      │
        └────────────────────────┬─────────────────────────────┘
                          空闲时 ▼
        ┌──────────────────────────────────────────────────────┐
        │ sleep 睡眠整固: 记忆和解·技能蒸馏·规则合成·漂移检测       │
        │ proposals 预期引擎: 提案队列                            │
        │ replay 回放评测 + mind.shadow 影子脑                    │
        │ economy 经济核算 · selfops 自我运维                     │
        └──────────────────────────────────────────────────────┘
```

### 包布局（目标态）

```
cmd/daimon/                 入口(改名自 cmd/ironclaw)
internal/
  heart/        新建   事件类型·事件源接口·去重·持久化      ← channel/ + scheduler 改造
  attention/    新建   三级路由·误判回流
  episode/      新建   组装器·运行器·退出契约               ← agent/ 主环抽离
  world/        新建   三层世界模型·检索门面                ← memory/ + taskruntime/ 合并升格
  values/       新建   价值条目·问一次流程·漂移检测
  action/       新建   可逆性分类·undo·hold·信任账本        ← tool/interceptor* 升格
  mind/         新建   Provider 分档·影子脑·热切换          ← agent/provider 拆出
  sleep/        新建   整固作业调度与各 job
  proposals/    新建   预期引擎·提案队列
  replay/       新建   情节录制·回放 harness                ← telemetry/ + agent_replays 表
  economy/      新建   成本核算·ROI 账本·节流
  selfops/      新建   看门狗·金丝雀·回滚
  gateway/      保留   组合根(接线对象换血)
  tool/         保留   工具实现(拦截链迁出至 action)
  skill/        保留   技能库 = 蒸馏流水线输出端
  workflow/     保留   确定性编排 = 反射执行底座
  session/      降格   聊天 transcript 存储
  memory/       吸收   检索引擎并入 world,其余拆解
  store/ config/ feature/ hook/ mcp/ errors/ userdir/ util/  保留
```

---

## 4. 模块详细设计

每个模块按统一模板：职责 / 核心类型 / 数据 / 关键流程 / 迁移来源 / 验收。

---

### 4.1 heart — 事件心脏

**职责**：把世界的一切变化统一为持久化事件流。代理的感官。

**核心类型**

```go
package heart

type Event struct {
    ID       string          // ulid
    Source   string          // "telegram" | "mail" | "calendar" | "fs" | "timer" | "webhook" | "internal"
    Kind     string          // "message" | "mail.received" | "calendar.changed" | "timer.fired" | ...
    Payload  json.RawMessage
    Occurred time.Time
    DedupKey string          // 源内幂等键(邮件 Message-ID、telegram update_id)
}

type Source interface {
    Name() string
    Run(ctx context.Context, emit func(Event)) error   // 长驻,断线自重连
}

type Heart struct { /* 注册 sources, 持久化, 投递给 attention */ }
```

**数据**（迁移 027）

```sql
CREATE TABLE events (
    id TEXT PRIMARY KEY, source TEXT NOT NULL, kind TEXT NOT NULL,
    payload TEXT NOT NULL, occurred_at INTEGER NOT NULL,
    dedup_key TEXT, routed_at INTEGER, verdict TEXT,        -- 路由结果回填
    UNIQUE(source, dedup_key)
);
```

**关键流程**：emit → 去重(UNIQUE 冲突即丢弃) → 落库 → 投递 attention → verdict 回填。**先落库后路由**，崩溃恢复 = 扫 `routed_at IS NULL` 重投。at-least-once，幂等靠 dedup_key + 情节侧 Receipt 查重。

**迁移来源**：`internal/channel/channel.go` 的 `Channel` 接口拆成两半——inbound 一侧变 `Source` 适配器（telegram/tui 各包一层），outbound 一侧（`StreamUpdater`/`NotificationSender`/`ApprovalSender`）保留为投递接口供 episode/proposals 使用。`internal/channel/scheduler/` 改写为 `timer` Source（cron 语义保留）。新增 Source：`mail`（IMAP IDLE）、`calendar`（CalDAV 轮询）、`fs`（fsnotify 监视配置目录）。

**验收**：杀进程重启后未路由事件全部补投且不重复处理；邮件/定时/聊天三源并发一周无丢失（对账 events 表与源侧计数）。

---

### 4.2 attention — 注意力路由

**职责**：判断每个事件值不值得醒来。成本结构的根基。

**核心类型**

```go
package attention

type Action int // Ignore | Reflex | Cognize | WakeUser

type Verdict struct {
    Action   Action
    ReflexID string  // Action==Reflex 时:技能/workflow id
    Priority int     // 0 紧急 … 3 闲时批处理
    Reason   string  // 审计用
}

type Router interface {
    Route(ctx context.Context, ev heart.Event) (Verdict, error)
}
```

**三级实现**（责任链，前级命中即返回）：

1. `rulesRouter`：`~/.daimon/attention/rules.yaml`，按 source/kind/payload 字段匹配。用户可编辑，sleep 作业也会合成新规则写入（带 `synthesized: true` 标记，可一键回滚）。
2. `modelRouter`：haiku 档模型，单次调用，输入 = 事件摘要 + 当前事项层 digest（≤1k tokens），输出 = Verdict JSON。
3. 兜底：`Cognize`。**宁可误醒**——漏掉重要事件的代价远大于多花一次认知。

**误判回流**：用户纠正（"这个不用管"/"这个怎么没告诉我"）记入 `attention_feedback` 表，sleep 作业据此合成/调整规则。

**数据**（迁移 028）：`attention_feedback(event_id, expected_action, given_action, note, created_at)`。

**验收**：路由成本占总成本 <5%；注入 100 个标注事件的测试集，WakeUser 召回率 100%（漏报零容忍），Ignore 准确率 >80%。

---

### 4.3 episode — 情节内核

**职责**：认知的执行单元。组装上下文 → 裸 ReAct → 强制交账 → 丢弃上下文。

**核心类型**

```go
package episode

type Episode struct {
    ID      string
    Trigger heart.Event
    Goal    string        // attention/调用方给定
    Budget  Budget        // maxTokens / maxToolCalls / deadline
    Parent  string        // 子情节时非空(吸收原 subagent)
}

type Outcome struct {                    // 退出契约 —— schema 强制
    Status      string                   // done | blocked | handed_off
    Summary     string                   // ≤500字,写入 journal
    WorldWrites []world.Mutation         // 学到的事实/事项变更
    Receipts    []action.ReceiptRef      // 本情节产生的行动
    FollowUps   []FollowUp               // 要种下的定时器/订阅
    OpenQuestion *string                 // blocked 时:卡在哪个待用户输入
}

type Composer interface {                // 上下文新鲜组装
    Compose(ctx context.Context, ep Episode) (mind.Request, error)
}
type Runner struct { /* 裸 ReAct: stream → tools → loop */ }
```

**Composer 组装清单**（每次认知调用从零构建，顺序即 prompt 布局，缓存边界在静态段后）：

| 段 | 来源 | 预算 |
|---|---|---|
| 人格 + 宪法摘要 | 静态 | 1k |
| 身份层 digest | `world/identity/digest.md`（sleep 维护） | 1.5k |
| 价值观 digest | `values` 高置信条目 | 1k |
| 相关事项 | commitments 检索 top-8 | 2k |
| 相关记忆 | journal/识别层混合检索 top-10 | 2.5k |
| 触发事件 + 必要 transcript | event payload；聊天事件附最近对话片段 | 2k |
| 工具清单 | 按情节 Goal 域过滤 | — |

**退出契约流程**：
1. Runner 注册保留工具 `episode_close(outcome)`，schema 即 `Outcome`。
2. 模型自然收敛（无 tool_calls）但未调 `episode_close` → 注入一次"调用 episode_close 交账"提示重试。
3. 仍未交账 → 框架抢救：haiku 档从 transcript 提取 Outcome，标记 `salvaged=true`（北极星指标盯这个比率）。
4. Outcome 落库 → WorldWrites 应用到 world → FollowUps 种入 heart → 上下文丢弃。

**长任务**：情节不追求一口气做完。Budget 到顶 → `Status=handed_off` + WorldWrites 记录进度 → FollowUp 种一个续跑定时器 → 下一个情节从世界模型重组继续。**续跑靠重组，不靠 checkpoint 反序列化**（`task_checkpoints` 表退役）。

**循环内剩余机制**：413 反应式压缩保留为兜底（`compression.go` 瘦身后并入），其余（plan 注入、Reflexion、预算告警话术）全部移除。

**迁移来源**：`internal/agent/linear_loop.go` + `loop_common.go` 抽出 Runner；`agent.go:buildSystemPrompt` 重写为 Composer；`subagent.go` 改为 `Spawn(child Episode)`；`reflection.go`、plan 注入、`maybeReflect` 删除。

**验收**：交账率 >98%（salvaged <2%）；同一事件 + 同一世界模型快照重组的上下文逐字节可复现（回放的前提）；并行 10 情节互不串扰。

---

### 4.4 world — 世界模型

**职责**：唯一事实源。代理的传记、当下与记忆。

**三层结构**

```
~/.daimon/world/
  identity/                 身份层(月级变化) —— 代理可自编辑的 markdown
    profile.md              是谁:角色/关系/环境
    preferences/*.md        偏好(按域分文件)
    digest.md               sleep 维护的≤1.5k压缩摘要,Composer 直接用
  values/                   价值层(见 4.5,物理上同住)
```

```sql
-- 事项层(天级变化): 改造现有 task_ledger (迁移 029 重命名+扩列)
CREATE TABLE commitments (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,        -- project | promise | deadline | watch | routine
    title TEXT NOT NULL, body TEXT,
    state TEXT NOT NULL,       -- active | waiting | done | dropped
    due_at INTEGER, horizon TEXT,            -- 预期引擎扫描用
    source_episode TEXT, updated_at INTEGER
);

-- 日志层(append-only): 情节 Outcome + 决策记录
CREATE TABLE journal (
    id TEXT PRIMARY KEY, episode_id TEXT, kind TEXT,   -- outcome | decision | correction
    summary TEXT NOT NULL, detail TEXT,
    occurred_at INTEGER, rollup_id TEXT                -- sleep 卷叠后指向摘要条目
);
```

**核心接口**

```go
package world

type Mutation struct {        // Outcome.WorldWrites 的元素
    Op     string             // fact.upsert | commitment.create | commitment.update | identity.edit
    Target string
    Body   json.RawMessage
}
type Model interface {
    Apply(ctx context.Context, episodeID string, muts []Mutation) error  // 事务性
    Retrieve(ctx context.Context, q Query) ([]Hit, error)               // 跨三层混合检索
    IdentityDigest() string
    CommitmentsDigest(horizon time.Duration) string
}
```

**自编辑**：模型通过 `world_read` / `world_edit` / `commitment` 三个工具直接读写（Letta/MemGPT 路线）。`world_edit` 走行动层（identity 文件改动 = Reversible，git 仓库化 `~/.daimon/world/` 即天然 undo）。

**检索**：现有 FTS5 + 向量 RRF 引擎（`memory/file_store.go`、`retriever.go`）整体征用，索引范围扩到三层。`temporal_facts`（023）的时效语义并入 journal 事实。

**消息级抽事实管线退役**：`memory/facts.go` 的 per-message LLM 抽取停用——事实进入世界模型的唯一通道是情节交账的 WorldWrites（修正：抽取时机从"每条消息"变为"每次认知收尾"，由模型在交账时自报，sleep 整固兜底查漏）。

**迁移来源**：`taskruntime/ledger.go` → commitments 引擎；`memory/autobiographical.go` → journal 的决策记录；`memory/file_store.go` + `retriever.go` + `cache.go` → 检索门面；`memory/lifecycle.go` 的和解逻辑移入 sleep。

**验收**：杀掉一切运行时内存后，仅凭 `~/.daimon` 目录重启，代理对"我是谁/你在帮我做什么/上周发生了什么"三问的回答与重启前一致（连续性测试，进回归集）。

---

### 4.5 values — 价值模型

**职责**：显式、可溯源、可编辑的用户价值观。自主行动的许可来源。

**条目格式**（`~/.daimon/world/values/<domain>/<slug>.md`）

```markdown
---
id: v-travel-no-redeye
domain: travel
statement: 宁可贵 500 以内也不订红眼航班
confidence: 0.9
provenance: [{episode: ep-xxxx, date: 2026-03-14, quote: "原话片段"}]
state: active            # active | drifting | retired
---
```

**两个流程**

1. **问一次（ask-once）**：情节中遇到无现有条目覆盖的价值权衡 → 行动层拒绝自主放行 → 情节 `Status=blocked` + `OpenQuestion` → 经渠道问用户 → 回答写成新条目 → 续跑情节。同类权衡此后不再问。
2. **漂移检测**（sleep 作业）：journal 中的用户纠正与现有条目矛盾 ≥2 次 → 条目标 `drifting` → 下次相关情节主动确认 → 更新或 retire。

**验收**：同一价值权衡在条目建立后 30 天内零重复提问；每个自主行动的 Receipt 都能引用到许可它的 value id（或 trust 等级）。

---

### 4.6 action — 行动层

**职责**：一切副作用的唯一出口。安全的主线。

**可逆性分类**

```go
package action

type Class int
const (
    Reversible   Class = iota // 文件改动(git兜底)/世界模型写入 → 直接执行+undo记录
    Compensable               // 发邮件/发消息/下单(可取消窗口) → hold队列延迟执行
    Irreversible              // 支付/删除不可恢复物/法律承诺 → 按信任等级走审批
)

type Receipt struct {
    ID string; EpisodeID string; Tool string; Class Class
    ValueRef string            // 许可来源: value id 或 "trust:L2"
    Undo *UndoSpec             // Reversible: 如何撤销
    Hold *HoldRef              // Compensable: hold 队列引用
    Verified *bool             // 客观判据结果(有判据时)
}
```

**执行管线**（重铸现有拦截链，顺序固定）：

```
values → trust → classify → hold/approve → execute → undo-journal → verify → audit
```

- `values`：查价值模型，无许可且非低风险 → 触发 ask-once。
- `trust`：查信任账本定自治等级。
- `classify`：工具静态声明 + 参数动态修正（`bash` 按 AST 解析命令分类——`mvdan.cc/sh`，原 P0-3 方案征用；`file_write` 在 git 仓库内 = Reversible，外 = Irreversible）。
- `hold`：Compensable 进 hold 队列（默认 120s，按行动类配置），期间用户可一键撤回；到期自动执行。
- `verify`：有客观判据（测试/编译/断言）时框架直接跑，结果写 Receipt.Verified（修正二例外 2）。
- 代码域工具叠加 OS 沙箱档（修正四）：`tools.exec.backend = host | seatbelt`，远程触发事件的情节强制 `seatbelt`。

**信任账本**（迁移 030）

```sql
CREATE TABLE trust_ledger (
    action_class TEXT, context_key TEXT,     -- 如 ("mail.send", "to:domain=company.com")
    attempts INTEGER, verified_ok INTEGER, corrected INTEGER,
    level INTEGER NOT NULL DEFAULT 0,        -- 0问每次 1问首次 2hold后自动 3完全自主
    updated_at INTEGER, PRIMARY KEY(action_class, context_key)
);
CREATE TABLE undo_journal (
    receipt_id TEXT PRIMARY KEY, undo_spec TEXT, expires_at INTEGER, undone_at INTEGER
);
CREATE TABLE holds (
    id TEXT PRIMARY KEY, receipt_id TEXT, execute_at INTEGER,
    state TEXT  -- pending | executed | recalled
);
```

**升降级规则**（确定性，不经模型）：`verified_ok ≥ N 且 corrected = 0` 连续达标 → 升一级并通知用户（可否决）；任一 corrected → 降一级 + 该类行动进入冷却。Irreversible 类 level 封顶 2（宪法第 4 条）。

**迁移来源**：`tool/interceptor*.go` 责任链骨架直接升格；`interceptor_verify.go` 改造为客观判据执行器；`permissions.go` 的审批决策持久化进 trust_ledger（替代 in-memory "always approve"）；`policy.go` 子串黑名单换 AST。

**验收**：注入对抗用例集（变形命令、路径逃逸、伪装域名邮件）零放行；hold 撤回端到端 <1s 生效；trust 升级路径全程有通知与审计记录；断电重启后 holds 队列正确恢复。

---

### 4.7 mind — 模型层

**职责**：可热插拔的认知引擎，分档供给，影子评测。

**核心类型**

```go
package mind

type Tier int // Router(haiku档) | Cognition(frontier档) | Embedding

type Provider interface {       // 现 agent.Provider 迁入,签名基本不动
    Stream(ctx context.Context, req Request) (Stream, error)
    Capabilities() Caps         // thinking通道/缓存协商/工具格式 —— 消灭 CACHE_BOUNDARY 注释
}

type Mind struct { /* tier→provider 路由表, 来自配置 */ }

type Shadow struct { /* 订阅事件副本, 只推理不执行, 结果进 replay 对比 */ }
```

**缓存协商**：`Caps.CacheBreakpoints` 由 Provider 声明，Composer 按声明放置边界——清除现在硬编码在 Anthropic 路径的 `<!-- CACHE_BOUNDARY -->` 魔法注释（原 P1-1 方案征用）。

**影子脑**：配置 `mind.shadow.model` 后，attention 判为 Cognize 的事件按采样率复制给 Shadow；Shadow 用同一 Composer 组装、推理、**行动全部 dry-run**（行动层短路为记录模式）；周报对比真脑/影子的 Outcome 质量（replay 评分）与成本。换脑 = 改配置一行 + 回放回归为零。

**迁移来源**：`agent/provider.go`、`claude_provider.go`、`openai.go`、`circuit_breaker.go`、`cache_metrics.go` 整体迁入。

**验收**：换 Cognition 模型不触碰 mind 包外任何代码；影子周报能给出"每千 token 质量分"对比；thinking 通道跨 provider 统一透传。

---

### 4.8 sleep — 睡眠整固

**职责**：空闲时段的离线作业群。复利发生的地方。

**作业清单**（统一调度器，按 idle 检测 + 每日窗口触发，全部可单独开关）：

| 作业 | 做什么 | 吸收自 |
|---|---|---|
| `reconcile` | 记忆和解：时间衰减、访问强化、嵌入级去重、矛盾事实 UPDATE+SoftInvalidate | 原 P1-2 全部内容，`memory/lifecycle.go` |
| `rollup` | journal 按周卷叠成摘要，原条目标 rollup_id（可追溯不删除） | 新写 |
| `distill` | 技能蒸馏：扫描 journal 中重复 ≥3 次且全 verified 的情节模式 → 生成 workflow spec 或 SKILL.md + 测试 → 金丝雀注册（首次执行仍 hold）→ 转正进 attention 反射表 | 新写，输出端 = 现有 skill/ + workflow/ |
| `synthesize-rules` | 从 attention_feedback 合成路由规则 | 新写 |
| `drift` | 价值漂移检测（见 4.5） | 新写 |
| `digest` | 重算 identity/digest.md 与 commitments digest | 新写 |

**蒸馏闭环是本模块的灵魂**：情节（贵）→ 技能（便宜）→ 反射（免费）。每个转正技能都在把一类认知成本永久清零。

**验收**：和解后矛盾事实检索只返回新条目；连续 4 周蒸馏出 ≥1 个转正技能且其后该模式零认知调用；sleep 作业全程不阻塞事件处理（独立 goroutine + 低优先级）。

---

### 4.9 proposals — 预期引擎

**职责**：从"它答"到"它提"。极限态的标志性器官。

**机制**：每日窗口 + 闲时触发**模拟情节**——Goal 固定为"扫描 commitments(horizon 72h) + 日历 + watches，找出 LO 接下来需要但还没要求的事"，Outcome 的 WorldWrites 写入提案：

```sql
CREATE TABLE proposals (        -- 迁移 031
    id TEXT PRIMARY KEY, title TEXT, body TEXT,
    action_plan TEXT,            -- 被采纳后点燃的情节 Goal
    urgency INTEGER, expires_at INTEGER,
    state TEXT,                  -- pending | accepted | dismissed | expired
    decided_at INTEGER
);
```

**投递**：Telegram 推送，inline 按钮 [做 / 不做 / 改一下]。accepted → 点燃执行情节；dismissed → 记入 attention_feedback（预期质量的训练信号）。每日提案数硬上限（默认 5），防骚扰。

**验收**：提案采纳率 >30% 起步（北极星目标 >50%）；被 dismiss 的同类提案频次自动下降。

---

### 4.10 replay — 回放评测

**职责**：免疫系统。一切改动（prompt/模型/规则/技能）的裁判。

**录制**：每个情节全量落盘——`(trigger event, 组装的完整上下文, model id, 全部 tool 往返, Outcome, 用户后续反应)`。征用现有 `execution_events`（020）+ `agent_replays`（021）表扩列；`telemetry/jsonl.go` 改造为录制器的导出端。

**回放模式**：
1. **离线重打分**：历史情节的上下文原样喂给新配置，haiku 档裁判对比新旧 Outcome（行动 dry-run）。
2. **回归集**：用户纠正过的情节自动入集（`journal.kind=correction` 关联的情节），改动必须全过。
3. **金丝雀**：selfops 的自我修改先在最近 50 个情节上回放，通过才转正。

**验收**：`daimon replay --against <config>` 产出可对比报告（质量分/成本/回归数）；回归集随纠正自动增长；4.3 的"上下文可复现"保证回放保真。

---

### 4.11 economy — 经济系统

**职责**：让代理能核算自己的价值，为更深授权提供依据。

**机制**：每情节成本（token 单价表 × 用量 + API 费）写 `costs` 表（迁移 032）；按 activity class（路由 kind 聚合）月度 ROI 报表：成本 vs 产出（采纳提案数、verified 行动数、为用户节省的可计量金额——情节交账时可选申报 `value_created` 字段）。节流策略：某 class 连续两月 ROI 为负且无 WakeUser 记录 → 自动降级该类 watch 并通知。

**验收**：月报能回答"它这个月花了多少、值不值"；节流触发有通知可否决。

---

### 4.12 selfops — 自我运维

**职责**：让"运维它"这件事也消失。

**机制**：`timer` 源每日发 `internal.health` 事件 → 健康情节检查：salvaged 率、路由漏报、holds 积压、磁盘、错误日志聚类 → 异常写提案或直接 WakeUser。自我修改（prompt 段、attention 规则、技能）一律走金丝雀回放 + 单独 git commit（`~/.daimon` 整目录 git 化），回滚 = revert。

**验收**：注入故障（断网/磁盘满/provider 5xx）后代理能自报症状；任何自我修改可单独回滚。

---

### 4.13 渠道与交互

- **Telegram = 主渠道**：提案/审批/hold 撤回全部 inline 按钮化；现有 adapter 升级，"always approve" 内存态废除（由 trust_ledger 接管）。
- **TUI = 调试控制台**：保留全部现有能力，新增 `/episodes`、`/trust`、`/holds`、`/proposals`、`/replay` 检视命令。定位变化写进文档，代码基本不动。
- **每日早报**：固定 timer 情节，输出过去 24h 摘要 + 提案队列 + 待审批，Telegram 投递。

---

## 5. 数据层总览

```
~/.daimon/
  world/            身份层+价值层(markdown, git 仓库)
  attention/        路由规则(yaml, git 同仓)
  skills/           技能库(SKILL.md + workflow specs)
  daimon.db         SQLite: 下表
  replays/          情节录制(jsonl, 滚动归档)
```

| 表 | 来源 | 迁移 |
|---|---|---|
| events | 新建 | 027 |
| attention_feedback | 新建 | 028 |
| commitments | task_ledger 改造 | 029 |
| journal | 新建（autobiographical 并入） | 029 |
| trust_ledger / undo_journal / holds | permissions 持久化升格 | 030 |
| proposals | 新建 | 031 |
| costs | 新建 | 032 |
| episodes / outcomes | execution_events + agent_replays 扩列 | 033 |
| sessions / messages | 保留（降格为 transcript） | — |
| memory_index / 向量 / FTS | 保留（检索引擎） | — |
| task_checkpoints | 退役（重组取代 checkpoint） | 034 drop |

---

## 6. 现有代码处置清单

| 现有 | 处置 | 去向 |
|---|---|---|
| `gateway/`（组合根+subsystem 模式） | **保留** | 接线对象逐 Phase 换血 |
| `tool/interceptor*.go` 责任链 | **升格** | → `action/` 执行管线骨架 |
| `taskruntime/ledger.go` | **升格** | → `world/` commitments 引擎 |
| `memory/file_store.go` `retriever.go` `cache.go` | **征用** | → `world/` 检索门面 |
| `memory/autobiographical.go` | **征用** | → `world/` journal |
| `memory/lifecycle.go` | **迁移** | → `sleep/reconcile` |
| `memory/facts.go` per-message 抽取 | **退役** | 交账 WorldWrites + sleep 兜底取代 |
| `agent/provider*.go` `circuit_breaker.go` `cache_metrics.go` | **迁入** | → `mind/` |
| `agent/linear_loop.go` `loop_common.go` | **瘦身抽出** | → `episode/` Runner（裸 ReAct） |
| `agent/buildSystemPrompt` | **重写** | → `episode/` Composer |
| `agent/subagent*.go` | **吸收** | → `episode.Spawn`（子情节） |
| `agent/reflection.go` + plan 注入 + Reflexion | **删除** | 宪法第 7 条 |
| `agent/compression.go` | **降格** | episode 内 413 兜底 |
| `tool/plan.go` | **改造** | → `commitment` 工具（写事项层） |
| `tool/permissions.go` `policy.go` | **重写** | → `action/` trust + AST 分类 |
| `workflow/` | **保留** | 反射执行底座 + 蒸馏输出格式 |
| `skill/` | **保留** | 蒸馏输出端 |
| `session/` | **降格** | 聊天 transcript 存储 |
| `channel/telegram` | **升格** | 主渠道 + inline 审批 |
| `channel/tui` | **降格** | 调试控制台 |
| `channel/scheduler` | **改写** | → `heart/` timer Source |
| `telemetry/jsonl.go` | **改造** | → `replay/` 录制导出 |
| `mcp/` `hook/` `config/` `feature/` `store/` `userdir/` | **保留** | 小幅适配 |

---

## 7. 实施路径

绞杀者模式：新包在旁边长，gateway 逐步换接线，每个 Phase 结束二进制可用、回放数据不断流。

### Phase 0 · 改名与录制先行（第 0 周，2–3 天）

最重要的一步是**立刻开始录制**——后面一切评测都吃这份数据。

1. 仓库/模块/二进制改名 Daimon，`~/.ironclaw` → `~/.daimon` 迁移逻辑 + 兼容软链。
2. `telemetry/jsonl.go` 扩为情节级录制器（先挂在现有 LinearLoop 上：消息、完整 prompt、tool 往返、最终回复全落 `replays/`）。
3. `~/.daimon` git 仓库化。
4. **验收**：改名后 `make build-bin && make vet && make test` 全绿；跑一天真实使用，replays/ 有完整可读数据。

### Phase 1 · 地基：情节 + 世界模型（第 1–4 周）

1. `world/`：三层落地（029 迁移、Mutation/Apply、检索门面包装现有引擎、`world_read/world_edit/commitment` 工具）。
2. `episode/`：Composer + Runner + 退出契约 + `episode_close` 工具 + salvage 路径。
3. 现有 `HandleMessage` 改为"每条消息点燃一个聊天情节"——session 降格在此完成。
4. 删除 plan 注入 / Reflexion / per-message 抽事实（一个 commit 一刀，便于回退）。
5. **验收**：连续性测试过（4.4）；交账率 >98%；回放可复现组装上下文；全量测试绿。

### Phase 2 · 器官：心脏 + 路由 + 行动层（第 5–8 周）

1. `heart/`：Event 落库投递 + telegram/tui/timer 三源改造 + mail 源（IMAP，第一个非聊天感官）。
2. `attention/`：rules + haiku 档两级上线，feedback 表。
3. `action/`：管线重铸（classify/hold/undo/trust 持久化/AST/verify 判据执行），seatbelt 沙箱档给代码域。
4. Telegram inline 审批 + hold 撤回 UX。
5. **验收**：无人值守跑 7 天零不可逆事故；路由成本 <5%；对抗用例零放行；4.1/4.2/4.6 各验收项过。

### Phase 3 · 生命：整固 + 提案 + 影子（第 9–12 周）

1. `sleep/`：reconcile + rollup + digest 三作业先行，distill 随后。
2. `proposals/`：模拟情节 + 提案队列 + Telegram 投递 + 早报。
3. `replay/` harness 成型（离线重打分 + 回归集）；`mind/` 影子脑接入。
4. **验收**：第一个蒸馏技能转正且该模式认知调用归零；第一次"没问它，它先做了，且做对了"；影子周报产出。

### Phase 4 · 生长：开放期（第 13 周起，按价值排序不设截止）

- `economy/` 月报与节流 → `selfops/` 看门狗与金丝雀 → calendar/fs 感官 → 本地路由模型（替 haiku 降本）→ values 漂移检测成熟 → A2A（等协议生态成熟，不抢跑）。

### 依赖关系

```
Phase0(录制) ─▶ Phase1(episode+world) ─▶ Phase2(heart+attention+action) ─▶ Phase3(sleep+proposals+shadow) ─▶ Phase4
                      │                                                        ▲
                      └── replay 数据从 Phase0 起持续积累 ──────────────────────┘
```

---

## 8. 北极星指标

| # | 指标 | 含义 | 方向 |
|---|---|---|---|
| 1 | 提案采纳率 | 预期质量 | ↑，目标 >50% |
| 2 | 自治率（无审批行动占比） | 信任深度 | ↑，护栏：纠正/回滚率 <2% |
| 3 | 单位价值认知成本 | 复利在发生 | ↓ 持续 |
| 4 | "从零回答"率（已告知事项被再问） | 连续性 | → 0 |
| 5 | 换脑回归数（回放） | 身份在状态里 | → 0 |
| 6 | salvaged 交账率 | 退出契约健康度 | <2% |
| 7 | WakeUser 漏报 | 路由安全 | = 0（零容忍） |

实现：economy 的 costs 表 + replay 的情节记录天然产出 1/3/5/6；2/4/7 由 journal 与 attention_feedback 推导。月度自动报表（selfops 情节生成）。

---

## 9. 风险登记册

| 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|
| 退出契约形同虚设（模型不好好交账，salvage 率高） | 中 | 架构承重墙失效 | schema 强制 + 指标 6 盯死 + 回归集覆盖；Claude 类模型对保留工具调用服从性已验证良好 |
| 注意力路由漏掉重要事件 | 中 | 信任崩塌 | 宁可误醒偏置 + WakeUser 零容忍指标 + 高风险 kind 硬规则白名单永不下放给模型路由 |
| 重组上下文质量 < 连续会话（丢隐式语境） | 中 | 体验下降 | 聊天情节附 transcript 片段（修正一）；回放对比两种组装的 Outcome 质量，用数据裁决预算分配 |
| 改造期双轨复杂度（新旧路径并存） | 高 | 进度拖期 | 绞杀者纪律：每 Phase 末删旧路径，不留长期双轨；每刀独立 commit |
| 蒸馏技能带病转正 | 低 | 错误被廉价复制 | 金丝雀 + 首次执行强制 hold + 全 verified 才候选 |
| 单人项目工程量 | 高 | 烂尾 | Phase 0/1 即产生日常价值（录制+连续性），每 Phase 自含可用;Phase 4 不设死线 |
| Telegram 单点（主渠道被封/宕机） | 低 | 失联 | TUI 永远可用；heart 多源设计天然支持加渠道 |

---

## 10. 第一天清单

1. `git checkout -b refound/daimon`
2. 仓库改名三件套：module path、`cmd/ironclaw` → `cmd/daimon`、`~/.ironclaw` 迁移逻辑。
3. `telemetry/jsonl.go` → 情节级录制（挂现有 LinearLoop）。
4. `~/.daimon` `git init` + 每日自动 commit 定时任务。
5. 跑 `make build-bin && make vet && make test` 确认绿。
6. 开始正常使用——**从今天起的每一次交互都是未来回放评测的种子数据。**

---

## 终句

这份蓝图的每一个设计决定都通过同一个测试：**三年后换了三代模型，它还在不在。**

在的——世界模型、信任账本、技能库、价值文档、回放数据——全部重投。
不在的——提示注入、反思脚手架、为当前模型缺陷打的补丁——一行不写。

Daimon 不是一个更聪明的聊天机器人。它是一个跑得越久越便宜、问得越来越少、被托付得越来越多的常驻进程：**铁打的爪，流水的脑。**
