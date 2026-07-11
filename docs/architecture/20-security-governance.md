# 20 · 安全与治理（横切）

> 横切关注点：可逆性分域、路径围栏、信任、审批审计、可逆性矩阵、本地主权

本篇不对应单一包，汇总贯穿全系统的安全与治理机制，串联各模块的安全设计。

## 治理脊柱：行动八段

每个自主行动经同一治理管线（[08-action.md](08-action.md)），这是宪法第 3/4 条落地的承重链：

```
values（价值门控）→ trust（信任等级）→ classify（可逆性）
  → HOLD（补偿性延迟）→ execute → undo（可逆性兜底）→ verify → audit
```

chat 同步路径与 autonomous 事件路径**共用同一拦截链**（agent 的 `invokeTool` 闭包），无治理绕过。

## 可逆性分域

蓝图核心安全模型：**按可逆性分级，而非按危险度一刀切**。

### 代码域 — 沙箱隔离

- **路径围栏**（双层）：
  - `ResolveWorkPath`（`tool.go`）：字符串级，拒 `..`/绝对路径逃出工作目录。
  - `fencedRealPath`（undo executor）+ `ensureWithinRoot`（skill promote）：`EvalSymlinks` 父目录重校验，闭中间 symlink 逃逸，fail-closed。
- **seatbelt/sandbox 路由**：`ChannelRoutingBackend` 非本地触发（remote/scheduled/internal/background）强制 sandbox，本地按配置（[15-tools.md](15-tools.md)）。sandbox 不可用时非本地来源 fail-closed，远程 bash 是真实 RCE 风险面，分域强制隔离。

### 生活域 — 可逆性 + 信任 + hold

代码工具无法沙箱的真实世界副作用（发邮件/改身份），靠：
- **可逆性分类**：Reversible(0) / Compensable(1) / Irreversible(2)。
- **AST/动词分类**：bash 命令经 AST 解析判可逆性；mutating HTTP（POST/PUT/PATCH/DELETE）→ Compensable（flag 门控）。
- **hold 窗口**：Compensable 行动延迟执行，留 recall 撤回窗口（[08-action.md](08-action.md) hold 状态机）。
- **undo 回执**：Reversible 行动 stamp undo receipt，`daimon undo` 可逆转。

## 信任升降级

```
AskEvery(0) ──1 verified──→ AskFirst(1) ──3──→ HoldThenAuto(2) ──10──→ FullAuto(3)
```

- 阈值：`promotionThreshold` = {1, 3, 10}（verified 累计）。
- **Irreversible 天花板** = HoldThenAuto——不可逆行动永不全自动。
- **单次纠正冻结升级**：`RecordCorrection` 后该 class 不再 promote（fail-closed）。
- **升级全程通知 + 审计**：`RecordAttempt` 跨阈 → `TrustNotifier` fire-and-forget → operator 通知 + journal `trust_promotion` 耐久 entry（宪法交账 + 本地主权）。

trust 是自主行动的**许可源之一**（另一是 values gate / reversible 豁免，[07-values.md](07-values.md)）。

## 审批与审计

- **审批**：`PermissionEngine` 按规则 + 能力 + channel profile 决策。需审批工具经 Telegram inline `[批准/拒绝]`（[16-channels-agent.md](16-channels-agent.md)）。
- **自治拒审批**：`handleApproval` 中 `ch==nil`（内部情节无人签）→ **拒绝需审批的工具**，仅自动批准/只读工具自治运行（[14-gateway.md](14-gateway.md)）。
- **审计 sink**：`permission_audit_log`（迁移 014）+ hook `audit_db.go`；行动经 `audit` 拦截段落账。

## 可逆性矩阵（自我修改回滚）

宪法第 4 条"任何自我修改可单独回滚"——三类自我修改各有 revert CLI（[13-selfops.md](13-selfops.md)）：

| 自我修改 | 落地 | 回滚 CLI |
|---|---|---|
| 身份 edit | `world_edit` 写 identity.md + `vcs.Commit` | `daimon world revert` |
| 技能转正 | `skill.PromoteDraft` 文件移动 | `daimon skill demote` |
| 路由规则 | `synthesize` 写 rules.yaml + `vcs.Commit` | `daimon attention revert` |
| 工具行动 | undo_journal receipt | `daimon undo [receipt\|--episode\|list]` |

`~/.daimon` 整目录 git 化 = 回滚即 `vcs.RevertFileToPrevious`（按文件自身历史，[18-supporting.md](18-supporting.md)）。

## 本地主权（宪法第 6 条）

- 单一本地 SQLite，无外部 DB / 云依赖（[19-data-layer.md](19-data-layer.md)）。
- 凭据走 `${VAR}` 环境注入，不进代码/配置仓。
- 无遥测：agent/tool 路径零 OpenTelemetry/metrics（CLAUDE.md 现状）。
- 主权代理：Telegram first allowed user 即唯一委托人，`primaryNotifier`。
- 不可逆/核心路径决策永人签（宪法第 4 条）——distill 自治转正受 Canary 阻塞（诚实墙，[11-replay.md](11-replay.md)）。

## 管理 HTTP 边界

- 管理端默认关闭并默认绑定 `127.0.0.1:8080`；启用时必须提供 `server.token`，推荐使用 `${DAIMON_ADMIN_TOKEN}` 环境注入。
- `GET /health` 公开且只暴露基础存活状态；所有 `/api/*` 路由经 Bearer token 中间件，当前仅有只读 `GET /api/sessions`。
- token 使用定长比较，认证失败统一返回 401；数据库错误仅返回通用 500，不向客户端泄露内部错误。
- 非 loopback 监听会扩大信任边界，必须由部署者配套网络访问控制，并在跨主机访问时提供 TLS 终止。

## §706 安全守卫（自我修改路径）

文件移动型自我修改（技能转正/undo）三道守卫：
1. `ValidSlug` / 路径分量校验——拒逃出单层目录。
2. `ensureNotSymlink`（`Lstat`）——绝不跟 symlink 链。
3. `ensureWithinRoot`（`EvalSymlinks` 后 root 内校验）——fail-closed。

确定性文件移动**绝不走 RunInternalEpisode**（LLM 非确定 + §706 风险）。

## 威胁模型边界

单用户本地部署：
- undo 镜像文件工具同一字符串围栏，无新能力面。
- 篡改 journal/DB = 本地已沦陷（不在威胁模型内）。
- 故未引入并发 CAS/fingerprint/mode 保留（best-effort + 确认提示对 v1 合理）。

## 设计取舍 / 不变量映射

| 设计 | 对应 | 为什么 |
|---|---|---|
| 按可逆性分域非危险度 | 宪法 4 | 可逆自由跑，不可逆永人签 |
| 远程触发强制 seatbelt | 安全 | 远程 bash 是 RCE 面 |
| trust 不可逆天花板 | 宪法 4 | 不可逆永不全自动 |
| 自我修改各有 revert | §563/宪法 4 | 任何自我修改可单独回滚 |
| 凭据环境注入 + 无遥测 | 宪法 6 | 本地主权，数据不外流 |

下一篇：[21-cli-reference.md](21-cli-reference.md) — CLI 参考。
