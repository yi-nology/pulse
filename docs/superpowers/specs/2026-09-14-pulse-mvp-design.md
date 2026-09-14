# Pulse MVP 设计文档

- 日期：2026-09-14
- 状态：待评审
- 前置调研：[docs/research/2026-09-14-feishu-ai-pm-research.md](../../research/2026-09-14-feishu-ai-pm-research.md)

## 1. 背景与目标

做一个项目管理软件，人和 AI agent（ZCode / Claude Code / Codex 等编码 agent）都是协作者。经调研与讨论，确定 MVP 定位：

> **Pulse 是本地优先的项目管理核心。** 数据真源是本机 SQLite；人用 CLI 操作，编码 agent 通过 MCP 操作；甘特图、人力负载、版本规划、周报四份"管理结果"可一键沉淀到飞书（文档 + 多维表格镜像）。

关键决策（已与需求方确认）：

| 决策点 | 结论 | 理由 |
|---|---|---|
| 部署形态 | 本地单机工具（单个二进制） | "谁都能单独使用"，零服务端依赖 |
| 飞书角色 | **只读沉淀地**，不是协作面/任务库 | 团队只用飞书"看结果"，避免双向同步 |
| 任务数据真源 | 本地 SQLite（`~/.pulse/pulse.db`） | 推翻此前"多维表格当库"方案 |
| AI 协作者 | 外部编码 agent 经 MCP 驱动 | core **不内置任何 LLM 调用** |
| 甘特图呈现 | 同步任务到**多维表格镜像**，用飞书原生甘特视图 | 免渲染 PNG，人的观看体验最好 |
| 数据位置 | `~/.pulse/`（全局单库多项目） | 用户指定；不放项目目录，避免 git 二进制合并问题 |
| 交互形态 | CLI（人）+ MCP stdio（agent） | ZCode/CC/Codex 均为 MCP client |

## 2. 架构

```
人:    pulse CLI ──────────┐
                           ├──► pulse core (Go) ──► SQLite  ~/.pulse/pulse.db
agent: pulse mcp (stdio) ──┘        │
         ▲                          ▼ feishu publish（可选，手动命令触发）
    ZCode / CC / Codex      feishu adapter ──► 飞书文档（周报/版本规划/人力/风险）
                                           ──► 多维表格镜像（原生甘特/看板视图）
```

- **core**：数据模型、CRUD、审计、报表生成。无任何网络依赖（除飞书适配器）。
- **两个入口，一个核心**：CLI 和 MCP server 是同一 core 包的薄封装；MCP 工具与 CLI 命令一一对应。
- **feishu adapter**：可选模块，未配置时完全不影响其余功能。单向：本地 → 飞书。飞书侧的修改**不会**回流。

## 3. 数据模型（SQLite）

```sql
-- 成员池（全局，跨项目复用）；agent 与人并列
members(id PK, name UNIQUE, type TEXT CHECK(type IN ('human','agent')),
        capacity_days_per_week REAL DEFAULT 5, notes, created_at)

projects(id PK, key TEXT UNIQUE,        -- 短名，如 "pulse"
         name, description, status,
         feishu_doc_token,              -- 沉淀文档
         feishu_bitable_app_token, feishu_bitable_table_id,  -- 镜像
         created_at)

tasks(id PK, project_id FK, title, description,
      assignee_id FK members NULL,      -- 人或 agent
      status TEXT CHECK(status IN ('backlog','todo','in_progress','blocked','done')),
      priority INTEGER,                 -- 1=P0 高
      estimate_days REAL,               -- 预估人日（人力负载的计算基础）
      start_date, due_date DATE,        -- 甘特条位置（手动给定，v1 不做自动排程）
      version_id FK versions NULL,
      bitable_record_id TEXT,           -- 镜像回写映射（幂等 upsert 依据）
      created_at, updated_at)

dependencies(id PK, task_id FK, depends_on_task_id FK,
             type TEXT DEFAULT 'FS')    -- finish-to-start

versions(id PK, project_id FK, name, target_date DATE,
         status TEXT CHECK(status IN ('planned','in_dev','released','shipped')), notes)

activity(id PK, project_id FK,          -- 审计核心表：一切变更留痕
         actor_id FK members, actor_type TEXT,      -- human | agent
         on_behalf_of FK members NULL,  -- agent 受人委托时的委托人
         action TEXT,                   -- create/update_status/publish/...
         entity_type TEXT, entity_id INTEGER,
         detail TEXT,                   -- JSON 变更摘要
         created_at)
```

设计要点：

- **agent 是一等成员**：`members.type='agent'`，出现在人力视图里与人并列；agent 的每次写入按 `actor_type=agent + on_behalf_of` 双字段归因（借鉴 GitHub Copilot bot 模式）。
- **当前人类用户**：CLI 动作的 actor 来自配置 `default_actor`（人类成员名），首次使用时自动在 members 中创建——保证 `member add`、`project init` 这类动作本身也有归属。
- **状态流转**：不做硬性状态机限制（任意状态可改任意状态）；但 done → 非-done 在审计中记为 `reopen` 动作，与普通 `update_status` 区分，供周报统计"返工"。
- **v1 甘特不做自动排程**：条形位置 = 手动 start/due；依赖以箭头呈现；**依赖倒置**（A 依赖 B 但 B.due > A.due）等 violations 在报表风险区列出。自动排程留待后续。
- 所有写操作（CLI 与 MCP 同路径）自动落 `activity`。

## 4. 四份管理结果（core 生成）

| 报表 | 内容 | 输出 |
|---|---|---|
| 甘特图 | 任务条（按 start/due）+ 里程碑 + 依赖箭头 + 版本泳道 | 本地自包含 HTML |
| 人力视图 | 每 member 名下任务、已排人日 vs 周容量、负载率；人/agent 并列 | HTML 表格 |
| 版本规划 | 每 version 的 scope、完成度、逾期项、目标日期 | HTML / 文本 |
| 周报 | 本周完成、进行中、下周计划、风险清单（逾期/阻塞/依赖倒置/超载）、agent 贡献摘要 | Markdown（发布时转飞书块） |

计算口径（v1，可解释优先）：

- **负载率** = 未来 14 天内到期且未 done 任务的 estimate_days 之和 ÷（capacity_days_per_week × 2）。无到期日的未完成任务不计入负载率，在报表单独列出"未排期"。
- **周报窗口** = 自然周（ISO 周一至周日）；"本周完成"依据 activity 表中该窗口内的 done 流转记录聚合，不依赖 updated_at。
- 风险规则（纯本地规则引擎，无 LLM）：逾期（due < today 且未 done）、blocked 超 N 天、无人认领且已进版本、依赖倒置（A 依赖 B 但 B.due > A.due）、member 负载率 > 100%。

## 5. 接口

### 5.1 CLI（cobra）

```
pulse init <key>                    # 注册项目
pulse project list
pulse member add <name> --type human|agent --capacity 5
pulse task add <title> --project K [--assignee] [--status] [--estimate] [--start] [--due] [--version]
pulse task update <id> --status/--assignee/--due/...
pulse task dep <id> --on <task-id>
pulse version add <name> --target 2026-10-01 ; pulse version list
pulse report gantt|workload|versions|weekly [--out FILE]
pulse feishu bind [--doc <token>|--new] [--bitable <token>|--new]
pulse feishu publish [--report weekly|versions|all]   # 全量：文档块 + bitable 镜像
pulse mcp                                           # 启动 stdio MCP server
```

### 5.2 MCP tools（与 CLI 同语义，供 agent 调用）

`list_projects` / `get_project_status` / `list_tasks`（过滤：assignee、status、version、overdue）/ `add_task` / `update_task` / `add_dependency` / `list_versions` / `add_version` / `list_members` / `get_workload` / `publish_feishu`。

- agent 身份：启动 MCP server 时以环境变量/参数指定（`.mcp.json` 里 `PULSE_ACTOR=codex`），server 按 actor 匹配 `members.type='agent'` 记录；未注册的 actor 首次调用自动创建 agent 成员。
- 工具描述里写明"更新任务状态前先 list_tasks 确认 id"，降低 agent 误操作。

## 6. 飞书适配器

- **身份**：自建应用 tenant_access_token（自用，免平台审核）。adapter 主动创建文档/多维表格 → owner=应用，天然持有权限，规避"添加文档应用"冷启动授权问题。
- **publish（单向、幂等、串行）**：
  1. 周报/版本规划/人力/风险 → docx 块追加（heading/text/bullet/TODO 块），落款归因："由 pulse 导出 · 本次由 {actor} 触发"；
  2. 任务 → Bitable 镜像：按 `bitable_record_id` 幂等 upsert（字段：任务名/状态/负责人/优先级/起止/版本/预估；"版本"为单选字段，选项随 versions 自动补齐）；首次初始化表结构 + 甘特视图（首选视图 API `view_type=gantt` 创建；若该类型不可经 API 创建，则降级为提示用户在多维表格里手动建一次，仅一次）；
  3. 全程串行写 + 429 指数退避（最多 5 次）；失败可重跑（幂等）。
- **配置**：`~/.pulse/config.yaml`——`default_actor`（人类成员名，CLI 动作的归属）、飞书 `app_id`/`app_secret`（secret 优先从环境变量 `PULSE_FEISHU_APP_SECRET` 读取）。

## 7. 技术栈

Go 1.25；SQLite 用 `modernc.org/sqlite`（纯 Go、无 cgo，单二进制好分发）；MCP 用官方 `modelcontextprotocol/go-sdk`（stdio）；CLI 用 cobra；飞书用官方 `larksuite/oapi-sdk-go`；报表 HTML 用 Go template + 内联 CSS（无外部依赖）。

## 8. 错误处理

- SQLite：单写者模型天然匹配单机场景；写操作事务化。
- MCP/CLI 输入校验：不存在的 id、非法状态流转（done→in_progress 需显式 reopen）返回结构化错误。
- 飞书：429/限流退避重试；token 自动刷新；publish 幂等可重跑；未配置 feishu 时 `publish` 命令给出引导性报错。
- 甘特缺日期：无 start/due 的任务不出现在甘特，报表风险区提示"未排期任务 N 个"。

## 9. 测试策略

- core/store：内存 SQLite 单元测试（CRUD、状态流转、activity 落痕）。
- 规则引擎：风险/负载计算表驱动测试。
- 报表：golden 文件测试（HTML/Markdown 渲染稳定性）。
- MCP：工具级集成测试（内存 store + stdio JSON-RPC 往返）。
- feishu adapter：`httptest` mock OpenAPI 合同测试（upsert 幂等、429 重试、块结构）。
- E2E：真实飞书测试租户手动清单（bind→publish→文档/镜像检查）。

## 10. MVP 明确不做

站会/日报收集、群 bot、自动排程（依赖驱动日程推算）、多机同步/协作、server 模式、自建 Web 界面、飞书→本地回流、pulse 内置 LLM 调用、多租户/ISV。

## 11. 里程碑建议（供 writing-plans 展开）

1. core + store + CLI（可用最小闭环：init/member/task/version）
2. 报表四件套（本地 HTML/Markdown）
3. MCP server（agent 可驱动 = 核心价值验证点）
4. feishu adapter（bind/publish + bitable 镜像）
5. E2E 打磨 + README
