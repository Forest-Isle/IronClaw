# 18 · 支撑包 — config / hook / mcp / session / memory / vcs / 等

> 包路径 `internal/{config,feature,hook,mcp,session,memory,appdir,userdir,vcs,store,telemetry,taskruntime,errors,netdial,util}`

本篇汇总不属于认知主路径、但被 gateway 装配依赖的叶子/支撑包。每个只给职责 + 关键签名 + 接缝。

## config — 分层配置

```go
// internal/config/config.go
type Config struct {
    LLM, Telegram, TUI, Agent, Store, Memory, Tools, Server, Health,
    Log, Telemetry, Skills, Agents, Permissions, Hooks, Economy ...
}
func ExpandEnv(data []byte) []byte  // ${VAR} → os.LookupEnv，缺失保留原样
```

- 拆分多文件（`config_agent.go`/`config_channels.go`/`config_infra.go`/`config_permissions.go`/`config_tools.go`/`config_hooks.go`）守体量红线。
- `${VAR}` 环境展开（`envVarPattern`）——凭据（DeepSeek/Telegram/OpenAI key）从环境注入，不硬编码；启用 admin 时，未解析的 `server.token` 占位符会校验失败。
- `validate.go`：加载时校验。`watcher.go`：文件变更热重载 → `OnReload` 回调路由（gateway 注册）。
- `EconomyConfig.Prices`：per-MTok 费率 map；`ThrottleConfig.Enforce` 默认 false（[12-economy.md](12-economy.md)）。

## feature — 特性开关

```go
// internal/feature
type Registry  // 布尔开关注册表
```

核心系统默认开（memory/skills/multi-agent）；`selfops` 默认关。`InitFeatures` 装配，新增特性须更新 `subsystem_feature.go`（CLAUDE.md 维护约定）。

## hook — 钩子链

```go
// internal/hook
type Hook        // pre/post tool use
HookInterceptor  // 内置 hook（injector_git/injector_workdir 注入上下文）
userHookInterceptor  // 用户 YAML hook（user_hooks.go）
```

- `injector_git.go` / `injector_workdir.go`：工具调用前注入 git 状态 / 工作目录上下文。
- `user_hooks.go`：用户配置的 YAML hook（pre/post），`safety.go` 安全校验。
- `audit.go` / `audit_db.go`：审计 sink（DB 持久化）。
- `precompact_preserver.go`：上下文压缩前保留关键信息。
- 挂在拦截链 hook/user_hooks 段（[15-tools.md](15-tools.md)）。

## mcp — Model Context Protocol

```go
// internal/mcp
// client：连外部 MCP server 拉工具 → adapter 注入 registry
// server：daimon 自身作为 MCP server 暴露能力（daimon mcp serve）
```

`InitMCP` 装配（`subsystem_mcp.go`）。外部 MCP 工具经 adapter 包成 `tool.Tool` 注入注册表。**注意**：交互认证的 MCP server 在 headless/cron 运行可能缺失。

## session — 会话

```go
// internal/session
type Manager  // sessions/messages 持久化
```

`InitDatabase` 内构造。父链（session_chain）+ 增量摘要（previous_summary）支撑长对话压缩（[16-channels-agent.md](16-channels-agent.md)）。

## memory — 绞杀中遗留检索

mem0 式事实抽取 + FTS5 混合检索（旧 IronClaw 残留）。**绞杀者改造中**：world 模型（[06-world.md](06-world.md)）的 `Retrieve` 是目标态，逐步取代直连 memory 路径。`prompt_frame.go` 仍有 legacy `Cortex.Search` 直连——CF3 legacy memory 退役需 P2-H 生产浸泡后才能拆（绞杀者纪律"旧路径跑通才拆"）。`memory` 工具仍注册供查/更新。

## appdir / userdir — 磁盘布局

```go
// internal/appdir/appdir.go
const DirName = ".daimon"   // LegacyDirName = ".ironclaw"
const DBName  = "daimon.db" // LegacyDBName  = "ironclaw.db"
func BaseDir() string         // ~/.daimon
func SkillsDir() string        // ~/.daimon/skills（active）
func SkillsStagingDir() string // ~/.daimon/skills-staging（inert 草稿）
```

`~/.daimon` 整目录 git 化（[13-selfops.md](13-selfops.md)）。完整布局见 [19-data-layer.md](19-data-layer.md)。`userdir` 为用户级路径解析。

## vcs — git 门面

```go
// internal/vcs/vcs.go (leaf, os/exec 调 git, 全 LC_ALL=C)
func EnsureRepo(dir) error                  // 幂等初始化
func Commit(dir string, paths ...string) error  // 路径限定提交
func Log(dir, path) (...)
func RevertFileToPrevious(dir, path) error  // 按文件自身历史 git log -n 2 -- path
func hasCommits(dir) (bool, error)
```

`~/.daimon` 自我修改可逆性基底——身份/技能/规则各有 revert CLI（[20-security-governance.md](20-security-governance.md)）。全 path `--` 守卫无 argument injection。world_edit 成功后 best-effort `EnsureRepo`+`Commit`（失败仅 warn 不阻塞）。

## store — 数据库底座

SQLite 打开 + 嵌入迁移（`internal/store/migrations/*.sql`）字母序自动应用。WAL 单写。详见 [19-data-layer.md](19-data-layer.md)。

## telemetry — 录制

```go
// internal/telemetry
// 订阅 EventBus → 写 JSONL replays/（jsonl.go）
// replay.go：录制语料源（被 internal/replay 消费，[11-replay.md]）
```

`InitTelemetry`（gateway 装配序第 3 位）。每会话/情节事件落 JSONL，是回放评测的语料源。

## taskruntime — 任务账本

```go
// internal/taskruntime/ledger.go
type Ledger  // 任务运行账本（checkpoint/状态转移）
```

`gateway.NewLedger`（装配序第 6 位）。`finishInbound` 用其做 task checkpoint + scheduler 状态转移（[14-gateway.md](14-gateway.md)）。

## errors / netdial / util — 微叶子

| 包 | 职责 |
|---|---|
| `errors` | 哨兵错误 + 包装约定 |
| `netdial` | 网络拨号控制（超时/受限） |
| `util` | 通用小工具（无业务逻辑） |

## 跨包接缝

- **config → 全子系统**：装配读配置；`OnReload` 热重载路由。
- **hook → tool**：拦截链 hook 段。
- **mcp → tool**：外部工具 adapter 注入。
- **vcs → world/skill/attention**：自我修改可逆性。
- **telemetry ← EventBus → replay**：录制语料。

## 设计取舍 / 不变量映射

| 设计 | 对应 | 为什么 |
|---|---|---|
| config 多文件拆分 | 体量红线 | 单文件不超 500 行 |
| 凭据走 ${VAR} 环境 | 宪法 6 本地主权 | 密钥不进代码/配置仓 |
| vcs leaf + LC_ALL=C | 确定性 | git 输出 locale 无关，path `--` 防注入 |
| memory 绞杀残留 | 绞杀者纪律 | 旧路径跑通才拆，world 渐进取代 |

下一篇：[19-data-layer.md](19-data-layer.md) — 数据层与迁移。
