# Daimon 架构文档（as-built）

> 本目录是 **Daimon**（仓库历史名 IronClaw，Go 模块 `github.com/Forest-Isle/daimon`）的权威 as-built 架构文档。
> 这里描述的是**当前代码实际是什么样子**，而非目标态；历史任务与实施过程记录已从仓库清理。

## 这是什么项目

Daimon 是一个 **主权个人代理（sovereign personal agent）**：一个常驻后台、事件驱动、跑得越久越便宜、问得越来越少、被托付得越来越多的 daemon。它由一个 coding-agent runtime 重铸而来，核心命题是 **「铁打的爪，流水的脑」**——传记 / 信任 / 技能 / 价值观是不死的部分（the agent），模型是会换的认知引擎（the mind）。

规模：33 个 `internal` 包，约 72k 行 Go，41 个数据库迁移。主二进制 `cmd/daimon`。

## 文档地图

| 文件 | 内容 |
|---|---|
| [00-overview.md](00-overview.md) | 项目定位、IronClaw→Daimon 重铸缘起、**七条宪法不变量**、高层架构图、设计哲学 |
| [01-architecture.md](01-architecture.md) | 包布局、依赖方向、组合根装配模式、**端到端事件流**、双执行路径、跨包接缝 |
| **核心认知路径** | |
| [02-heart.md](02-heart.md) | `heart` 事件心脏：事件流持久化、去重、崩溃恢复、感官源 |
| [03-attention.md](03-attention.md) | `attention` 注意力路由：三级责任链、硬白名单、误判回流 |
| [04-episode.md](04-episode.md) | `episode` 情节内核：裸 ReAct、退出契约、salvage、父子情节链 |
| [05-mind.md](05-mind.md) | `mind` 模型层：Provider 抽象、缓存协商、重试熔断、影子脑 |
| **状态与行动** | |
| [06-world.md](06-world.md) | `world` 世界模型：身份 / 事项 / 日志三层、混合检索、幂等交账 |
| [07-values.md](07-values.md) | `values` 价值模型：ask-once 门控、漂移检测、自主行动许可源 |
| [08-action.md](08-action.md) | `action` 行动层：可逆性分类、信任账本、hold 队列、undo、AST 分类、沙箱 |
| **离线 / 元系统** | |
| [09-sleep.md](09-sleep.md) | `sleep` 睡眠整固：全离线作业、蒸馏闭环、自治调度 |
| [10-proposals.md](10-proposals.md) | `proposals` 预期引擎：提案队列、typed accept、投递闭环 |
| [11-replay.md](11-replay.md) | `replay` 回放评测：录制、离线重打分、回归集、金丝雀 |
| [12-economy.md](12-economy.md) | `economy` 经济系统：成本台账、ROI 报表、节流 |
| [13-selfops.md](13-selfops.md) | `selfops` 自我运维：健康看门狗、失败安全、错误聚类 |
| **基础设施** | |
| [14-gateway.md](14-gateway.md) | `gateway` 组合根：子系统装配、事件分发、timer 源 |
| [15-tools.md](15-tools.md) | `tool` 工具层：注册表、拦截链、内置工具、沙箱后端 |
| [16-channels-agent.md](16-channels-agent.md) | `channel` + `agent` 运行时：渠道、认知内核集成、子代理 |
| [17-skills-workflow.md](17-skills-workflow.md) | `skill` + `workflow` 反射执行底座、蒸馏输出端 |
| [18-supporting.md](18-supporting.md) | 支撑包：config / feature / hook / mcp / session / memory / vcs / 等 |
| **参考** | |
| [19-data-layer.md](19-data-layer.md) | 数据层：SQLite 全表 schema、41 迁移逐条、`~/.daimon` 磁盘布局 |
| [20-security-governance.md](20-security-governance.md) | 安全与治理（横切）：可逆性分域、路径围栏、信任、审批、审计 |
| [21-cli-reference.md](21-cli-reference.md) | CLI 参考：`daimon` 全部子命令 |
| [22-glossary.md](22-glossary.md) | 术语表、北极星指标、设计不变量速查 |

## 推荐阅读路径

- **快速理解全貌（30 分钟）**：00 → 01 → 22。看完知道 Daimon 是什么、怎么连、关键术语。
- **理解一次认知如何发生**：01（端到端事件流）→ 02 heart → 03 attention → 04 episode → 06 world。这是「一封邮件如何让代理醒来、思考、落账」的主线。
- **理解安全与自治**：08 action → 07 values → 20 security-governance。可逆性分类、信任如何升级、什么永远人签。
- **理解复利飞轮**：09 sleep → 11 replay → 10 proposals → 12 economy。离线整固如何让代理越跑越便宜。
- **维护者 / 改代码**：14 gateway（装配）→ 15 tools（拦截链）→ 19 data-layer（schema）→ 18 supporting。

## 与其它文档的关系

| 文档 | 性质 | 用途 |
|---|---|---|
| 本目录 `docs/architecture/` | **as-built** 权威 | 理解现状、改代码、上手 |
| `DAIMON_BLUEPRINT.md` | 愿景 / 目标态 | 设计意图、为什么这么设计、宪法原文 |

现状与蓝图不符处（如 mail/calendar/fs 感官源、distill 自治转正受金丝雀阻塞等"诚实墙"），本目录以**现状**为准并注明。
