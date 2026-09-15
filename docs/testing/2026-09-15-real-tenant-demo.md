# Pulse 真实飞书租户 Demo 测试报告

- 日期：2026-09-15
- 环境：真实自建应用 `cli_xxxxxxxxxx`（用户租户）、真实二进制 `feat/mvp@45bc84c`、本地真源 `~/.pulse/pulse.db`（demo 项目）+ `/tmp/demo-p2`（模拟同事 wangfang 的机器）
- 说明：这是首次脱离仿真服务器、直连真实租户的全链验证。demo 数据留在真实库与真实租户中供体验，可整包删除（base 与文档手动删，本地 `pulse project` 数据可清库）。

## Demo 数据一览（真实库）

- 项目 `demo`「Pulse 演示项目」；成员：zhangyi / wangfang / liwei（人）+ claude / codex（agent）
- 版本 v0.9（in_dev 目标 09-25）、v1.0（planned 目标 10-30）
- 任务 9 条（2 done / 1 blocked / 1 逾期 / 1 无人认领 / 1 未排期 / 1 依赖倒置——风险引擎五类齐全）、依赖 3 条
- 需求 2、评审 1（passed_with_notes）、会议 1、bug 2（P1×1）、提测单 1（testing）、发版记录 1

## 真实租户验证矩阵（全部通过）

| 场景 | 结果 | 证据 |
|---|---|---|
| bind 真实建 8 张表+沉淀文档 | ✅ | base `REDACTED_OLD_BASE_TOKEN`，共享提示带全八表 id |
| sync 推送 9 任务+2 版本 | ✅ | 逐条 record_id 回填 |
| 真实建 7 份协作文档 | ✅ | 2 需求+1 评审+1 会议+1 提测+1 发版，token 全回填 |
| sync 推六实体（autopush 累计） | ✅ | 19 条干净回声，远端已全量落数 |
| agent（claude）经 MCP 干活 | ✅ | MCP add_task/update_bug → activity `claude(agent)/zhangyi`，autopush 即时上表 |
| 真实 publish 周报+版本规划 | ✅ | 沉淀文档 revision 3，含报表块与归因落款 |
| **真实双机收敛** | ✅ | p2 adopt 八表→首次拉取 20 条→p2 改 bug verified（autopush）→p1 拉取 1 收敛 |
| 只读 API 核验落数据 | ✅ | 8 表合计 20 条；抽样字段值与本地一致（含 agent 任务、双机收敛后的 bug 状态） |

## 真实租户发现（仿真不可能暴露）

| # | 级别 | 描述 | 处置 |
|---|---|---|---|
| REAL-1 | 高（已修复） | **Bitable text 字段读回是富文本数组** `[{type:"text",text:"v0.9"}]`，非裸字符串；pull 解析全挂（优雅降级兜住：告警+本地保留+水位封顶，零丢失） | `flattenRichText` 归一化进 toText/toFloat（mapping.go），红→绿测试 + 真实租户重跑 11 条干净回声验证。**写方向裸字符串真实 Bitable 接受，无需改** |

附带验证：真实 records/search 分页信封、tenant token 流程、envelope 解析、30s 超时重试路径在真实网络下工作正常（一次瞬时超时后重试成功）。

## 链接（登录你的租户可直接打开）

- 多维表格（8 表 20 条记录）：https://www.feishu.cn/base/REDACTED_OLD_BASE_TOKEN
- 沉淀文档（周报+版本规划）：https://www.feishu.cn/docx/REDACTED_DOC_TOKEN_2
- 需求文档×2 / 评审纪要 / 会议纪要 / 提测单 / 发版记录：token 见 `sqlite3 ~/.pulse/pulse.db "SELECT feishu_doc_token FROM requirements"` 等各表

## 建议的人工体验路径

1. 打开多维表格——切甘特视图前先把"开始/截止"两列改为日期类型（bind 提示的一次性操作）
2. 在 Bitable 里随手改一条任务状态 → 回终端 `pulse sync --project demo` → 看本地被拉平
3. 用 ZCode/CC 挂 MCP（README `.mcp.json` 示例，`PULSE_ACTOR=claude`）让 agent 建任务/修 bug → `pulse sync` → 表格里出现
4. `/tmp/demo-p2` 是模拟的同事机器，可继续双机实验（删掉即清理）

## 复核补充（用户反馈"飞书那边没弄好"后，2026-09-15）

**用户在真实 UI 看到的三个问题与根因：**

| 现象 | 根因 | 处置 |
|---|---|---|
| 甘特视图是空的 | 首版 bind 用**文本列**存日期（计划中的妥协决策），甘特视图需要日期类型列才能渲染条形 | **方案推翻**：bind 改建原生列——开始/截止/目标日期/评审/会议/发布时间=日期类型（写毫秒时间戳、读回转 YYYY-MM-DD），状态/优先级/版本/结论/严重级等=单选（选项自动创建），预估人日=数字。甘特开箱可用，单选可下拉筛选（`9506a58`） |
| base 里多一张空"数据表" | Bitable API 建 base 时自动生成默认表，无 API 可删 | 无害；可在 UI 手动删 |
| 其中一张发版表删不掉（403） | 清理旧表时该表返回 403（资源级，重试无效） | 需在 UI 手动删旧 base（整个旧 base `REDACTED_OLD_BASE_TOKEN` 建议手动删除） |

**迁移注意（已写入 README）**：旧 base 的表列类型是文本，无法原地升级——删除旧表/旧 base 后重新 `pulse feishu bind` + `pulse sync` 即可；本地需同步清空各实体的 `bitable_record_id/bitable_synced_hash`（本次 demo 迁移中踩过：残留旧 record_id 会导致 PUT 1254043 RecordIdNotFound，告警保留本地、可清后自愈）。

**重建后核验（真实 API）**：新 base `REDACTED_BASE_TOKEN` 任务表列类型=任务名文本/状态单选/负责人文本/优先级单选/开始日期/截止日期/预估人日数字/版本单选/已废弃勾选 ✅；截止值 1788998400000 = 2026-09-10 UTC ✅；11 条任务全量在表；表格+甘特视图就绪 ✅。

**新链接（替换旧的）**：
- 多维表格：https://www.feishu.cn/base/REDACTED_BASE_TOKEN
- 沉淀文档：重新 publish 后生成（bind 时新建）
