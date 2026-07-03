# Daimon 流量浸泡 Runbook

> 性质: P1-d 浸泡配置档 + 运营手册。开发冻结后启用，浸泡窗口 2-4 周。
> 目的: 用真实日常流量喂肥 replay 语料、回归集、economy 台账、trust ledger，
> 验证自治环全链路，为收割期（C4 流量金标 / CF3 legacy 退役 / §240 基准）攒数据。

---

## 1. 前置检查（浸泡开始前一次性）

### 凭证

| 项 | 位置 | 状态检查 |
|---|---|---|
| DeepSeek API | `${DEEPSEEK_API_KEY}` | `daimon` 能正常对话 |
| Telegram bot | `configs/daimon.yaml` telegram 块 | Telegram 端收到回复 |
| Gmail IMAP | `agent.heart.mail`（已配置） | 日志出现 `mail:` 无持续 Warn |
| Gmail SMTP | `tools.email`（已配置） | `send_email` 工具可见 |
| CalDAV | `agent.heart.calendar` — **待配置** | 见 §2 差量 |
| OpenRouter 嵌入 | `${OPENROUTER_API_KEY}` | memory 无嵌入报错 |

### 运行时 feature

```
/feature enable selfops     # 看门狗，默认关；状态持久化于 ~/.daimon，重启不丢
/feature list               # 确认 memory / skills / multi_agent / selfops 全 on
```

## 2. 配置差量（浸泡开始时应用到 configs/daimon.yaml）

只列需要改动的键，其余保持现状（mail / email / hold_enabled / replay 已开）：

```yaml
agent:
  heart:
    daily_brief_interval_minutes: 1440   # 每日摘要（journal + proposals + holds）
    health_interval_minutes: 60          # selfops 巡检每小时（需 feature selfops）
    sleep_interval_minutes: 1440         # 自治睡眠每日一轮
    sleep_idle_minutes: 30               # 30 分钟静默才允许自治睡眠
    chat_through_heart: true             # 入站聊天入事件流（审计+去重，回复路径不变）
    fs_watch_dirs:
      - "${HOME}/Documents"              # 按需选低噪声目录；严禁 ~/.daimon（自写反馈环）
    calendar:
      enabled: true
      caldav_url: "https://caldav.icloud.com"   # 或其他 CalDAV 服务
      username: "${CALDAV_USERNAME}"
      password: "${CALDAV_PASSWORD}"            # app-password，勿明文
      calendar_paths: []                        # 空 = 自动发现 VEVENT 日历
```

**保持关闭（有意）**：
- `economy throttle enforce` — 观察态，浸泡先攒 per-class ROI 数据，不执法
- `model_router` — 小模型 triage 需单独模型配置，非浸泡必需
- `agent.subagent_episode_enabled` — 子代理 episode 路由为 flag-gated 实验路径，浸泡验证主环
- `heartbeat_interval_minutes` — 无消费场景，空转事件白费 token

## 3. 日常运营（每天 ~5 分钟）

1. 看每日 brief（Telegram 自动送达），或 TUI `/brief`
2. 巡检: `/selfops`（健康信号）· `/holds`（待执行 Compensable）· `/proposals`（提案队列）· `/trust`（信任晋升）· `/episodes`
3. **纠正即投资**: 发现坏 session 立刻 `daimon correct <session-id>` —— 每次纠正自动进回归集，直接喂 C4 金标
4. Compensable 动作（send_email 等）进 hold 队列后有 120s 召回窗，误发用 `daimon holds recall`
5. 撤销: `daimon undo list` / `daimon undo --episode <id>`

## 4. 每周检查

- `daimon replay --against <model> --canary` — 周度回归金丝雀
- economy 月报 CLI — 看 per-class 花费与 ROI，throttle advisor 建议（观察态）
- `daimon skill drafts` — distill 产出的技能草稿，值得的走 `daimon skill promote`（人签）
- §240 标注: 从事件流抽样标注 路由该唤醒/不该唤醒，目标浸泡期攒满 100 条

## 5. 冻结纪律

- 浸泡窗口内 **main 零 merge**，唯一例外: 崩溃级 bugfix（修后重启浸泡时钟不重置，但记录扰动日期）
- 新想法一律进 backlog，浸泡结束再排期
- 配置改动同罪 —— §2 差量应用后配置冻结

## 6. 收割 checklist（浸泡满 2-4 周）

- [ ] C4: 从 replay 语料蒸馏流量金标任务，canary 置信 tier `synthetic` → `traffic-derived`
- [ ] CF3: legacy memory 退役 —— 删 `prompt_frame.go` Cortex.Search 直连路径（绞杀者: 新路径已浸泡跑通）
- [ ] §240: 100 标注事件跑 attention recall/precision 基准
- [ ] mind §4.7: LIVE 影子采样数据回看（quality_per_1k_tok）
- [ ] trust ledger 回看: 哪些 action-kind 晋升了、晋升是否合理
- [ ] economy: 首份完整月报，throttle 是否值得转执法（enforce: true）
