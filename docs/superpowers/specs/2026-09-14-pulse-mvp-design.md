# Pulse MVP 设计文档

- 日期：2026-09-14
- 状态：待评审
- 前置调研：[docs/research/2026-09-14-feishu-ai-pm-research.md](../../research/2026-09-14-feishu-ai-pm-research.md)

## 1. 背景与目标

做一个项目管理软件，人和 AI agent（ZCode / Claude Code / Codex 等编码 agent）都是协作者。经调研与讨论，确定 MVP 定位：

> **Pulse 是本地优先的项目管理核心。** 本地 SQLite 是工作副本，团队通过共享多维表格做多人结构化同步；人用 CLI 操作，编码 agent 通过 MCP 操作；甘特图、人力负载、版本规划、周报四份"管理结果"可一键沉淀到飞书（文档 + 多维表格原生视图）。

关键决策（已与需求方确认）：

| 决策点 | 结论 | 理由 |
|---|---|---|
| 部署形态 | 本地单机工具（单个二进制） | "谁都能单独使用"，零服务端依赖 |
| 飞书角色 | 结构化数据=多维表格**同步中枢**（双向）；报表与协作内容=飞书文档（单向创建，多人协作填写，不解析回流） | 零部署、复用飞书生态，团队在飞书里直接看活数据 |
| 任务数据真源 | 本地 SQLite（`~/.pulse/pulse.db`）= 工作副本；共享 Bitable = 团队共享层 | 每台机器离线可用；修正此前"只读沉淀"的方案 |
| AI 协作者 | 外部编码 agent 经 MCP 驱动 | core **不内置任何 LLM 调用** |
| 甘特图呈现 | 同步任务到共享多维表格，用飞书原生甘特视图 | 免渲染 PNG，人的观看体验最好 |
| 数据位置 | `~/.pulse/`（全局单库多项目） | 用户指定；不放项目目录，避免 git 二进制合并问题 |
| 交互形态 | CLI（人）+ MCP stdio（agent） | ZCode/CC/Codex 均为 MCP client |
| 多人结构化同步 | 多维表格同步中枢：本地 push/pull、最终一致、记录级 last-writer-wins | 不引入服务端；pulse serve 模式留作演进选项 |
| 多人文档协作 | pulse 管结构/状态/链接，纪要/清单类内容按模板建飞书文档由多人协作填写，内容不回流 | 状态流转永远经 CLI/MCP 显式操作，保证审计可信 |

## 2. 架构

```
人:    pulse CLI ──────────┐
                           ├──► pulse core (Go) ──► SQLite  ~/.pulse/pulse.db（工作副本，每台机器一份）
agent: pulse mcp (stdio) ──┘        │
         ▲                          ▼
    ZCode / CC / Codex      feishu adapter
                             ├─ sync（push/pull）◄──► 共享多维表格 = 团队同步中枢（v1.0：任务/版本）
                             ├─ publish（单向）──► 飞书文档（周报/版本规划/人力/风险）
                             └─ record new（v1.1）──► 协作记录文档（提测单/发版记录/纪要，模板创建）
```

- **core**：数据模型、CRUD、审计、报表生成。无任何网络依赖（除飞书适配器）。
- **两个入口，一个核心**：CLI 和 MCP server 是同一 core 包的薄封装；MCP 工具与 CLI 命令一一对应。
- **feishu adapter**：可选模块，未配置时完全不影响其余功能。sync（多维表格）为**双向**；publish/record（文档）为单向，文档内容永不解析回流。

## 3. 数据模型（SQLite）

```sql
-- 成员池（全局，跨项目复用）；agent 与人并列
members(id PK, name UNIQUE, type TEXT CHECK(type IN ('human','agent')),
        capacity_days_per_week REAL DEFAULT 5, notes, created_at)

projects(id PK, key TEXT UNIQUE,        -- 短名，如 "pulse"
         name, description, status,
         feishu_doc_token,              -- 沉淀文档
         feishu_bitable_app_token, feishu_bitable_table_id,  -- 同步中枢
         created_at)

tasks(id PK, project_id FK, title, description,
      assignee_id FK members NULL,      -- 人或 agent
      status TEXT CHECK(status IN ('backlog','todo','in_progress','blocked','done')),
      priority INTEGER,                 -- 1=P0 高
      estimate_days REAL,               -- 预估人日（人力负载的计算基础）
      start_date, due_date DATE,        -- 甘特条位置（手动给定，v1 不做自动排程）
      version_id FK versions NULL,
      bitable_record_id TEXT,           -- 同步中枢映射（幂等 upsert 依据）
      bitable_synced_hash TEXT,         -- 最近一次成功 push/pull 的内容指纹（自回声判定）
      archived INTEGER DEFAULT 0,       -- 软删（pull 到 Bitable"已废弃"时置 1）
      created_at, updated_at)

dependencies(id PK, task_id FK, depends_on_task_id FK,
             type TEXT DEFAULT 'FS')    -- finish-to-start

versions(id PK, project_id FK, name, target_date DATE,
         status TEXT CHECK(status IN ('planned','in_dev','released','shipped')), notes,
         bitable_record_id TEXT, bitable_synced_hash TEXT)

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
- **同步元数据**（机制见 §6）：参与同步的实体（v1.0：tasks、versions）持有 `bitable_record_id` + `bitable_synced_hash`；`activity` 不参与同步（各机器本地留存）。删除走软删：同步实体删除时在 Bitable 侧置"已废弃"，pull 到"已废弃"记录时本地打 `archived` 标记（默认视图与报表排除）。

### 3.1 v1.1 扩展：研发交付闭环实体（设计现在定，实现排 MVP 后）

覆盖 需求 → 评审 → 开发 → 提测 → bug → 发版 流程。全部复用 §3 的模式（状态字段 + activity 审计 + 可选绑定飞书文档）：

```sql
requirements(id PK, project_id FK, title, description,
             status CHECK(status IN ('proposed','reviewing','accepted','in_dev','delivered','rejected')),
             priority INTEGER, owner_id FK members, source TEXT,
             feishu_doc_token, created_at, updated_at)
-- tasks 增加 requirement_id FK NULL（需求拆解为任务）

reviews(id PK, project_id FK, requirement_id FK NULL, kind TEXT,   -- requirement|release|...
        held_at DATETIME,
        conclusion CHECK(conclusion IN ('pending','passed','passed_with_notes','rejected')),
        feishu_doc_token, created_by FK members, created_at)

meetings(id PK, project_id FK, title, held_at DATETIME,
         feishu_doc_token, created_by FK members, created_at)      -- 参会人/纪要在飞书文档内协作维护

bugs(id PK, project_id FK, title, description,
     severity INTEGER,                                             -- 1=P0
     status CHECK(status IN ('open','fixing','fixed','verified','closed','wontfix')),
     reporter_id FK, assignee_id FK NULL, requirement_id FK NULL,
     found_version_id FK versions NULL, fix_task_id FK tasks NULL,
     created_at, updated_at)

test_submissions(id PK, project_id FK, version_id FK, requirement_id FK NULL,
                 submitted_by FK, test_owner_id FK,
                 status CHECK(status IN ('draft','submitted','testing','passed','failed')),
                 scope TEXT,                                       -- 结构化范围；自检清单在飞书
                 feishu_doc_token, submitted_at, concluded_at, created_at)

releases(id PK, project_id FK, version_id FK,
         status CHECK(status IN ('preparing','testing','released','rolled_back')),
         release_manager_id FK members, released_at DATETIME,
         feishu_doc_token, notes, created_at)
```

### 3.2 协作记录模式（v1.1 统一交互）

pulse 持有每条记录的结构化字段（状态/负责人/时间点/关联关系）；需要多人共同填写的内容（评审结论、会议纪要、提测自检清单、发版清单）由 pulse 按**内置模板**在飞书创建文档，token 回填到记录。约束：

- 文档内容**不回流**：pulse 不解析飞书文档内容，状态流转只由人经 CLI/MCP 显式操作（或 agent 代操作，记 activity、`on_behalf_of` 归因）。
- 评审/会议产出的**行动项**：需要跟踪时，由人或 agent 经 MCP 建 task 并在纪要中提及；pulse 不自动从文档提取。
- 模板共 5 套：提测单、发版记录、评审纪要、会议纪要、需求文档（可选——短需求直接写 pulse 的 description 字段，长 PRD 才建文档）；复用 §6 飞书 adapter 的块写入能力（`feishu record new test-submission --version v1.2` 一类命令）。
- 对应 CLI 子命令与 MCP 工具（`create_requirement`/`update_bug_status`/`create_test_submission`/`create_release` 等）在 v1.1 阶段随实体一起交付。

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
pulse feishu bind                                     # 首次：创建共享 Bitable（任务表+版本表+甘特视图）与沉淀文档
pulse sync                                            # 双向同步：push 本地变更 → pull 共享 Bitable 变更
pulse feishu publish [--report weekly|versions|all]   # 报表沉淀到飞书文档（单向）
pulse mcp                                             # 启动 stdio MCP server
```

### 5.2 MCP tools（与 CLI 同语义，供 agent 调用）

`list_projects` / `get_project_status` / `list_tasks`（过滤：assignee、status、version、overdue）/ `add_task` / `update_task` / `add_dependency` / `list_versions` / `add_version` / `list_members` / `get_workload` / `publish_feishu`。

- agent 身份：启动 MCP server 时以环境变量/参数指定（`.mcp.json` 里 `PULSE_ACTOR=codex`），server 按 actor 匹配 `members.type='agent'` 记录；未注册的 actor 首次调用自动创建 agent 成员。
- 工具描述里写明"更新任务状态前先 list_tasks 确认 id"，降低 agent 误操作。

## 6. 飞书适配器

- **身份**：自建应用 tenant_access_token（自用，免平台审核）。adapter 主动创建 base/文档 → owner=应用，天然持有权限，规避"添加文档应用"冷启动授权问题；团队成员对共享 base 的查看/编辑权限由人在飞书侧添加（一次性操作）。
- **bind（每个项目一次性）**：创建共享 Bitable base（任务表+版本表，字段映射见下）+ 甘特视图（首选视图 API `view_type=gantt` 创建；若该类型不可经 API 创建，降级为提示用户手动建一次）+ 沉淀文档；各 token 写入项目记录。
- **sync（双向，结构化数据的多人共享机制）**：
  - **push**：本地自上次同步后有变更的记录 → 按 `bitable_record_id` 幂等 upsert；新建记录成功后回填 record_id；
  - **pull**：分页拉全量（500/页），按 Bitable `last_modified_time` 水位挑出变更记录 → 与本地 `bitable_synced_hash` 比对内容指纹，**相同则判定为自回声、跳过**；不同则按 record_id 合入本地库；
  - **删除**：软删——本地删除 → Bitable 置"已废弃"复选框；pull 到"已废弃" → 本地置 `archived=1`；
  - **归因**：Bitable `last_modified_by` 恒为应用身份（区分不了操作者），操作者由记录内 `updated_by` 文本字段携带；
  - **冲突**：记录级 last-writer-wins（以 Bitable 侧最后写入为准），activity 本地留痕可追溯；不做字段级合并；
  - **触发**：写操作后自动 push；`pulse sync` 显式双向；报表命令执行前若超过 `sync.stale_minutes`（默认 10）未 pull 则先静默 pull（离线时跳过并提示"数据可能滞后"）；
  - **范围**：v1.0 只同步 tasks + versions；v1.1 按 §3.1 扩展六实体（同一模式复用）；**文档内容永不参与同步**（pulse 不解析飞书文档）。
- **publish（单向，报表沉淀）**：周报/版本规划/人力/风险 → docx 块追加（heading/text/bullet/TODO 块），落款"由 pulse 导出 · 本次由 {actor} 触发"。
- **限流应对**：全程串行写（每 base 一条写队列）+ 429 指数退避（最多 5 次）；sync/publish 均幂等可重跑；免费版配额触顶时报明确错误并保留本地 dirty 标记，恢复后自动补推。
- **任务表字段映射**（SQLite ↔ Bitable）：任务名/状态（单选）/负责人（文本，v1.0 不用飞书成员字段——本地成员含 agent，映射不上）/优先级（单选）/开始、截止（日期）/预估人日（数字）/版本（单选，选项随 versions 自动补齐）/已废弃（复选）/updated_by（文本）。
- **配置**：`~/.pulse/config.yaml`——`default_actor`（人类成员名，CLI 动作的归属）、飞书 `app_id`/`app_secret`（secret 优先从环境变量 `PULSE_FEISHU_APP_SECRET` 读取）、`sync.stale_minutes`（默认 10）。

## 7. 技术栈

Go 1.25；SQLite 用 `modernc.org/sqlite`（纯 Go、无 cgo，单二进制好分发）；MCP 用官方 `modelcontextprotocol/go-sdk`（stdio）；CLI 用 cobra；飞书用官方 `larksuite/oapi-sdk-go`；报表 HTML 用 Go template + 内联 CSS（无外部依赖）。

## 8. 错误处理

- SQLite：单写者模型天然匹配单机场景；写操作事务化。
- MCP/CLI 输入校验：不存在的 id、非法状态流转（done→in_progress 需显式 reopen）返回结构化错误。
- 飞书：429/限流退避重试；token 自动刷新；publish/sync 幂等可重跑；未配置飞书时 `sync`/`publish` 给出引导性报错，本地功能完全不受影响。
- 同步：dirty 标记持久化——离线期间的本地变更在下次 `pulse sync` 自动补推；pull 中断可重跑；配额触顶报明确错误并保留 dirty 标记，恢复后补推；pull 到的记录缺字段（有人在飞书手改表结构）时跳过该记录并在 sync 输出中告警。
- 甘特缺日期：无 start/due 的任务不出现在甘特，报表风险区提示"未排期任务 N 个"。

## 9. 测试策略

- core/store：内存 SQLite 单元测试（CRUD、状态流转、activity 落痕）。
- 规则引擎：风险/负载计算表驱动测试。
- 报表：golden 文件测试（HTML/Markdown 渲染稳定性）。
- MCP：工具级集成测试（内存 store + stdio JSON-RPC 往返）。
- feishu adapter：`httptest` mock OpenAPI 合同测试（upsert 幂等、429 重试、块结构）。
- E2E：真实飞书测试租户手动清单（bind→sync 双机互推→publish→文档/甘特视图检查）。

## 10. MVP 明确不做（v1.1 或更晚）

研发交付闭环六实体（需求/评审/会议/bug/提测/发版，§3.1-3.2，v1.1 首批）、自动排程（依赖驱动日程推算）、站会/日报收集、群 bot、事件订阅近实时同步（MVP 用命令触发+水位增量）、pulse serve 共享实例模式（同步架构的演进选项）、自建 Web 界面、飞书文档内容回流解析、pulse 内置 LLM 调用、多租户/ISV。

## 11. 里程碑建议（供 writing-plans 展开）

1. core + store + CLI（可用最小闭环：init/member/task/version）
2. 报表四件套（本地 HTML/Markdown）
3. MCP server（agent 可驱动 = 核心价值验证点）
4. feishu adapter（bind/publish + 多维表格同步中枢：任务/版本双向 sync）
5. E2E 打磨 + README
6. v1.1：研发交付闭环六实体 + 协作记录模板 + 对应 CLI/MCP（MVP 验收后启动）
