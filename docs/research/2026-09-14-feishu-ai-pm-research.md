# 「人 + AI Agent 混合协作」项目管理软件 · 全网调研报告

> 调研日期：2026-09-14
> 目标场景：深度结合飞书文档的项目管理软件，人和 AI agent 都是协作者（AI 可被分配任务、读写文档、汇报进度，人可审批验收）
> 方法：四路并行网络调研（飞书开放平台能力 / 国际竞品 / 国内竞品 / 人机协作协议与开源方案），信息来源见文末

---

## TL;DR 核心结论

1. **技术可行性高**。飞书开放平台已提供完整的技术地基：文档块级读写 API、应用可作为文档协作者（AI agent 可拥有独立身份）、官方 MCP Server（2025-09）与官方 CLI（2026-04，专为 AI Agent 设计）、卡片流式渲染。想让 AI agent 坐进项目群、持有文档、被 @、被派活，官方路径全部打通。
2. **"AI 协作者"的产品范式已在国际上收敛**。Linear / Jira(Rovo) / Asana / Notion / GitHub 在 2025-2026 年全部完成同一件事：**把 AI agent 做成一等公民成员**——有独立身份、可被 assign、可被 @、有活动记录、不占 human seat。"assign-to-agent" 已是行业默认交互。
3. **国内存在明确空白**。国内 PM 工具（飞书项目/TAPD/PingCode/ONES）的 AI 仍集中在"生成周报/摘要/测试用例/填单"辅助层；国内没有"AI agent 作为项目一等成员（有身份、有看板、可领任务、可交付、被验收）"的产品化工具。最接近的是民间 OpenClaw+飞书实践（飞书官方甚至提供了一键部署方案），但都是通用 agent 拼装，缺 PM 语义层。
4. **反例警示**：独立"全自主 AI PM"先驱 Height.app 于 2025-09 关停；飞书自家 Aily（任务模式）+ 飞书项目 AI 正快速逼近这个空间。**窗口存在但平台吞噬风险真实**，定位要选在"平台能力缝隙 + agent 编排 + PM 语义层"作为壁垒。
5. **推荐架构**：自研服务持有项目数据（真源）→ 飞书文档/多维表格做协作面（双向同步）→ 应用机器人 + 流式卡片做群内交互面 → AI agent 以应用身份（tenant token）成为文档/任务协作者 → 对外暴露 MCP Server 接入任意 agent 宿主。

---

## 一、市场格局

### 1.1 国际：三条路线分化

| 路线 | 代表 | 模式 | 现状 |
|---|---|---|---|
| **成熟工具 agent 化** | Linear、Jira(Rovo)、Asana、Notion 3.0、GitHub(Agent HQ) | 在既有 PM 工具内把 agent 做成一等成员 | 2025-2026 全面落地，是行业主流 |
| **独立 AI-native PM** | Height 2.0（自主 AI 队友）、Motion（agentic work suite，2025-09 融 $60M）、Coworked Harmony（headless AI PM） | 产品本身就是 AI 团队成员 | Height 已死（2025-09 关停）；Motion 等垂直生长中 |
| **寄生 agent** | Devin、Factory droids、OpenHands、Manus | 不做 PM 界面，寄生在 PM 工具的指派体系里 | 跑通"被 assign → 干活 → 交 PR/回帖 → 人 review"闭环 |

**标志性产品动作：**
- **Linear for Agents**（2025-05）：agent 作为 first-class user 进 workspace；issue 的 assignee 下拉框直接选 agent；人类队友始终保留 primary owner 身份，agent 是"受托人"；agent 有独立 profile 页与活动流；不占 billable seat。2026 年推出自家原生 AI agent + Agent API（任何人可把自己的 agent 变成 Linear 成员）。
- **Jira + Rovo**：四种触发方式（agent picker、评论 @、工作流状态触发、看板列触发）；agent 可被 assign 为 assignee；输出"触发透明、内容默认私有、publish 才共享"的三层可见性；credit 计费（$0.01/credit 超额）。
- **Asana AI Teammates**（2025 秋）：30 个预置 AI 队友（Status Reporter、Spec Reviewer、Sprint Coach…），像普通成员一样被指派任务；checkpoints 展示进展；全操作可审计可回滚；权限"never elevate access"；定价按 outcome 溢价。
- **Notion 3.0 Agents**（2025-09）：agent "能做人能在 Notion 做的一切事"，记忆建在页面/数据库上；但更像"私人助理"，成员化程度不及 Linear/Jira。
- **GitHub Copilot coding agent + Agent HQ**：独立 bot 身份、可被 assign issue、commit author=bot + 人列 co-author、审计日志有 agentic 事件——**agent 身份与归因的最成熟产品化样本**。

### 1.2 国内：辅助层 AI + 执行体外循环

| 产品 | AI 现状 | 飞书生态关系 |
|---|---|---|
| **飞书项目** | AI 助手（生成报告/风险洞察/建工作项/节点流转）、AI 个人周报、智能填单、测试用例生成；**MCP Server（2025-09，专为 Agent 场景）**；OpenClaw 官方方案（IPD 排期 48h→3min） | 独立产品线，商业版约 ¥60/人/月起（第三方口径，未确认） |
| **飞书多维表格** | AI 字段捷径、工作流 AI Agent 节点、多维表格智能体；被大量中小团队当轻量 PM 用 | 缺依赖/关键路径/容量规划等 PM 深水区能力 |
| **TAPD** | AI 需求评审（多 agent）、AI 测试用例（覆盖度 +35%）、TAPD MCP；路线图 2026+ 建设多智能体协作 | 深度绑定企微/腾讯文档 |
| **PingCode** | v6.0 把自动化产品 Flow 升级为"智能引擎"（企业智能体平台）；拼扣(PinKo)对话应用；发布到飞书"未确认" | 飞书集成浅（目录同步+消息通知） |
| **ONES** | **国内最完整的执行型 AI**：Assistant 可在权限内查询/执行/回写、PRD 批量拆解父子工作项、AI 结果留痕可审计；MCP Server 63 工具免费开放 | 无原生飞书集成 |
| **Worktile/Teambition/云效** | 功能型 AI 为主；Teambition AI 助理邀测；云效限研发场景 | 各自绑定自家生态 |
| **飞书 Aily + 豆包工作伙伴** | 企业级 Agent 平台（六大场景含"项目管理专家"）；**任务模式（2025-12）：员工在飞书内给 AI 派任务、看进展、验收结果**——官方已在做"AI 员工接活"形态；¥9,900/年起 | 平台本身，吞噬风险来源 |
| **民间实践** | **OpenClaw + 飞书官方一键部署**（内置飞书文档/多维表格工具，群聊流式卡片）——国内最接近"AI 群成员管项目"的主流方案；Coze 一键发布到飞书 | 需求真实存在，但都是通用拼装 |

### 1.3 关键空白（机会）

1. **国内 PM 赛道没有"AI agent 一等项目成员"产品**：有账号、有任务看板、可领任务/交活/被验收、贡献留痕——全部空白。
2. **文档↔工作项的语义双向深链**：官方打通止步于字段同步/关联；"文档评论/会议/群聊自动生成任务、需求变更自动 diff 文档"仍是空白。
3. **群聊原生 PM bot 的产品化**：OpenClaw/Coze 证明需求真实，但缺"懂项目语义的群成员"（站会、周报、风险预警、依赖提醒、排期建议）。
4. **多维表格之上的专业 PM 语义层**：借其权限/视图/自动化底座，补依赖、关键路径、容量规划、跨项目聚合 + agent 编排。

---

## 二、"AI 协作者"产品设计范式（行业收敛的 7 条共性）

1. **身份：从功能按钮到具名成员**。三档演进：功能按钮（ClickUp Brain）→ 自动化角色（Monday Agent Factory）→ **一等公民成员**（Linear/Jira/Asana/GitHub）。成员化标配：独立头像/档案页、活动流、"app user/agent" 徽章（防混淆是共同设计点）。
2. **指派：三种触发并存**。显式 assign（与人类同构，学习成本为零）> @ 提及（派活+提问双语义，最低门槛）> 规则自动触发（工作流状态/看板列）。行业收敛点：**显式为主、规则化为辅、全自主慎用**（Height 之死）。
3. **透明度：活动流 + 会话回放是信任基建**。agent 的工作过程必须像人类一样留痕：profile 页、issue 内活动、Delegate 维度过滤统计、session 回放、diff。
4. **权限：agent 权限 = 某个人类用户权限的投影，绝不越权**。Asana "never elevate access"；Linear admin 安装制 + 团队级授权；Notion 删除前强制确认。
5. **Human-in-the-loop 藏进既有流程，不做独立"AI 审批中心"**：人类 owner 始终负责（Linear）、Draft comment 手动 publish（Jira）、PR review（coding agents）、checkpoints（Asana）、删除确认（Notion）。
6. **商业模式：AI 不占 human seat，按用量/产出收费**。AI add-on 月费（ClickUp $9）、credit/ACU（Rovo、Devin）、per-action（Asana Studio）、outcome 溢价（Asana Teammates）。
7. **AI 工程师类 agent 寄生于 PM 指派体系**：PM 工具 assign/@ → agent 干活 → PR/评论/session 回放交付 → 人 review 关闭 loop。PM 工具正在把自己变成 agent 的工作台/操作系统（Linear Agent API、GitHub Agent HQ、Rovo partner agents）。

---

## 三、飞书开放平台能力边界（9 项关键结论）

| # | 能力 | 结论 | 要点 |
|---|---|---|---|
| 1 | **云文档 API** | ✅ 能 | 文档/块 7 个 API 完整读写（含 **Todo 块**可创建）；评论 API 全（读+回帖+局部评论）；文档 40,000 块上限；Task/思维笔记/议程/同步块不可 API 创建 |
| 2 | **应用作为文档协作者** | ✅ 能（对 AI agent 场景最关键） | 协作者类型官方明确含"应用"；agent 用 tenant_access_token 即可以独立身份持有/编辑文档，与人在协作者列表并列；用户侧需"添加文档应用"授权 |
| 3 | **多维表格 Bitable** | ✅ 能（最成熟的 API 域） | 记录批量 1000/批、字段/视图/仪表盘/角色全 API；记录变更事件可订阅；**同一数据表不支持并发写**（需单点写队列） |
| 4 | **机器人与事件** | ✅ 能 | 自建应用机器人收发+卡片；**事件支持 WebSocket 长连接（无需公网）**；卡片回调 3s 超时；**CardKit 流式更新卡片（打字机效果，2025）** 适合 AI 回答渲染 |
| 5 | **身份模型** | ✅ 清晰 | tenant token（应用自己，推荐默认）/ user token（OAuth 代表用户）；scopes 管理员审批；自建应用免平台审核，商店应用（ISV）需飞书审核 |
| 6 | **官方 MCP** | ✅ Beta | `larksuite/lark-openapi-mcp`：im/docs/bitable/task/wiki/calendar 约 46+ bitable 工具；**注意：builtin 不支持文档块级编辑（仅导入/读取）**，需 `-t` 补挂 docx 写 API |
| 7 | **官方 CLI（2026-04 新）** | ✅ | `larksuite/cli`：200+ 命令、以 Markdown 为载体、26 个 Agent Skills、`--as user\|bot` 双身份——对 LLM 心智最友好；飞书项目另有 `meegle-cli` |
| 8 | **限流与坑** | ⚠️ | docx 读写 3-5 QPS、免费版月调用总量 1 万次、Bitable 同表写互斥、卡片回调 3s、应用访问他人文档需先授权（冷启动问题）→ 必须自建服务端收敛写路径 + 队列 |
| 9 | **UI 嵌入** | ✅ 四条路 | **云文档小组件（Docs add-on，2024-25 新开放：第三方 UI 嵌进文档正文）**、多维表格插件、工作台小组件、网页应用 OAuth 免登 |

另有：**飞书 Aily**（企业 Agent 平台，可经 aily.v1 MCP 工具驱动，但偏平台内闭环，非本文档协作者形态）；**飞书项目** OpenAPI 120+ 接口 + webhook + 富文本 Markdown 读写（对标产品而非依赖）。

---

## 四、协议与参考架构（人机协作的技术底座）

1. **MCP（事实标准）**：2026-07-28 规范完成生产级改造（无状态核心、MRTR、Tasks 扩展=长任务轮询，天然契合"agent 被分配任务"）；OpenAI/Google/Microsoft 全采用。标准架构：`Agent → MCP Server（SaaS 适配器）→ REST API` + OAuth 2.1。
2. **A2A（agent 互操作）**：Linux 基金会 v1.0、150+ 组织；Agent Card（agent 的"简历"）+ 有生命周期的 Task 状态机。官方口径 **"MCP for tools, A2A for agents"**——可直接照搬为架构原则。
3. **Agent 身份**：GitHub 是最成熟样本（bot 账号 + assign + co-author 归因 + 审计事件）；IAM 业界共识 "AI Agents Are Not Users"，agent 需要一等 principal 身份 + 双字段归因（`actor_type=agent` + 委托人）。IETF/OIDF 标准 draft 中，暂以 GitHub 模式自建即可。
4. **HITL 标准三件套**：暂停（可持久化，LangGraph interrupt 模式）→ 提案（diff/卡片/表单）→ 批准/修改/拒绝后恢复；超时默认拒绝；工具白名单+预算做前置约束。IM 落地 = 飞书消息卡片 + 回调（与 Slack Block Kit/Teams Adaptive Card 完全同构）。
5. **文档技术**（若自建块编辑器）：结构化块模型(JSON) + Yjs CRDT；agent 作为 headless CRDT peer 写入（每次 update 天然携带 actor → 人/AI 归因）；Tiptap track-changes / BlockNote 内置 AI 做"agent 建议态→人采纳"。选型：BlockNote（快）/ BlockSuite（自主可控）。
6. **编排后置**：单 agent 派活用"agent 运行时 + MCP 工具 + 任务队列"即可；CrewAI hierarchical / LangGraph supervisor 作为外挂引擎，数据面始终回到自己的 PM 数据模型。
7. **开源 PM 底座候选**（若不完全自建）：**Plane**（AI-native 定位 + 官方 MCP 28 工具 + 可分配 agent）最贴合；OpenProject（官方 MCP，偏传统）；Huly/Taiga/Focalboard 需自建 AI 层。

---

## 五、三条产品路线（供决策）

### 路线 A：飞书原生底座（推荐起步）
多维表格/文档 = 数据+协作底座，自研服务做 **PM 语义层 + agent 编排**，机器人+卡片做交互面，agent 以应用身份协作文档。
- 优点：最快见效；权限/视图/自动化白嫖；免建编辑器；自建应用零审核。
- 缺点：受飞书能力边界约束（QPS、写互斥、块类型限制）；被 Aily/多维表格 AI 节点平台吞噬的风险。

### 路线 B：独立产品 + 飞书深度集成（推荐终态）
自建数据模型 + 看板 + 块编辑器（BlockNote/Yjs），**对外暴露 MCP Server**，与飞书做四层集成：文档双向同步、机器人交互面、Docs add-on 小组件、官方 MCP/CLI 接入外部 agent。
- 优点：完全掌控 agent 协作语义（身份/审计/审批）；不押注单一平台；可复用用户现有 workspace 体系（zeus IAM）。
- 缺点：工作量最大；编辑器是硬骨头。

### 路线 C：AI 项目管家 bot（轻量切入）
不做完整 PM 界面，做一个"懂项目语义的飞书群成员"（站会收集、周报、风险预警、依赖提醒、任务领取交付验收），底下接多维表格或飞书项目。
- 优点：2-4 周可出 MVP；直接对标 OpenClaw 实践的升级版；验证"人+AI 协作"核心交互。
- 缺点：天花板低，最终仍要回答"界面在哪"。

**组合建议：C 起步验证交互 → A 做数据底座 → 演进到 B。**（即 MVP 用 C 的形态 + A 的底座，核心资产是"项目数据模型 + agent 身份/审计/审批语义"，界面可后补。）

---

## 六、风险清单

| 风险 | 说明 | 对策 |
|---|---|---|
| 平台吞噬 | 飞书 Aily 任务模式 + 飞书项目 AI + 多维表格 AI 节点三个月一迭代，正逼近"AI 群成员管项目" | 壁垒放在跨平台 agent 编排 + PM 深水区语义（依赖/关键路径/容量）+ agent 审计治理，不在"套壳调 API" |
| API 限流 | docx 3-5 QPS、免费版 1 万次/月、Bitable 写互斥 | 服务端收敛写路径 + 队列重试 + 批量接口；商用提醒客户版本要求 |
| 商店审核 | 做 ISV SaaS 需飞书审核上架，成本高 | 先做企业自建应用（免平台审核），验证后再走 ISV |
| 冷启动授权 | 应用访问他人文档需先被"添加文档应用" | 引导流程设计；优先让应用自建文档（owner=应用） |
| 全自主陷阱 | Height 之死证明"全自动 PM"不被市场接受 | 显式指派为主、规则触发为辅、HITL 关卡内建 |
| 身份混淆 | agent 冒充用户写数据是治理红线 | agent 永远以应用身份落操作，双字段归因（actor + 委托人） |

---

## 七、下一步待决策问题（brainstorming 继续）

1. **定位**：先自用（自己团队跑起来）还是直接商业化（ISV 路线）？——决定应用形态、成本预算、审核路径。
2. **目标场景**：研发项目管理（对标飞书项目）还是通用项目/任务协作（对标多维表格用法）？
3. **AI agent 的核心角色**：执行型（写代码/写文档/干活交付）还是管理型（整理/提醒/汇报/排期）还是两者？第一个 MVP 场景选哪个？
4. **底座**：接受多维表格当数据库（路线 A）还是自建数据模型（路线 B）？
5. **技术栈**：Go（与 workspace 现有体系/ekit/agentkit 一致）+ 前端框架选择？

---

## 附录：主要信息来源

### 飞书官方
- docx 概述/块 API：https://open.feishu.cn/document/server-docs/docs/docs/docx-v1/docx-overview
- 云文档权限（协作者含应用）：https://open.feishu.cn/document/server-docs/docs/permission/overview
- 多维表格概述：https://open.feishu.cn/document/server-docs/docs/bitable-v1/bitable-overview
- 频控策略：https://open.feishu.cn/document/server-docs/api-call-guide/frequency-control
- 免费版调用上限：https://open.feishu.cn/document/platform-notices/platform-updates-/custom-app-api-call-limit
- 卡片概览/回调：https://open.feishu.cn/document/feishu-cards/feishu-card-overview
- 云文档小组件：https://open.feishu.cn/document/client-docs/docs-add-on/docs-add-on-introduction
- 官方 MCP：https://github.com/larksuite/lark-openapi-mcp
- 官方 CLI：https://github.com/larksuite/cli
- 飞书项目 OpenAPI：https://project.feishu.cn/b/helpcenter/1p8d7djs/wlcwhshe
- 飞书项目 MCP：https://project.feishu.cn/b/helpcenter/1p8d7djs/73n2upf3
- OpenClaw 飞书方案：https://www.feishu.cn/openclaw

### 国际竞品
- Linear Agents：https://linear.app/agents 、https://linear.app/docs/agents-in-linear
- Jira + Rovo agents：https://support.atlassian.com/jira-software-cloud/docs/collaborate-on-work-items-with-ai-agents/
- Asana AI Teammates：https://asana.com/product/ai/ai-teammates
- Notion 3.0：https://www.notion.com/blog/introducing-notion-3-0
- GitHub Agent HQ：https://github.blog/news-insights/company-news/welcome-home-agents/
- Devin：https://docs.devin.ai/integrations/linear
- Height 关停（反例）：https://www.reddit.com/r/SaaS/comments/1ji3q3n/

### 国内竞品
- TAPD MCP/AI：https://cloud.tencent.com/developer/article/2514008
- PingCode v6.0：http://blog.pingcode.com/pingcode-v6/
- ONES Assistant：https://ones.cn/products/ai
- 飞书 Aily 任务模式：https://www.feishu.cn/content/article/7585126677299137754

### 协议/标准
- MCP 规范 2026-07-28：https://blog.modelcontextprotocol.io/posts/2026-07-28/
- A2A：https://github.com/a2aproject/a2a
- Agent 身份（Auth0）：https://auth0.com/blog/agent-as-principal-purpose-built-identity-for-agents/
- GitHub Copilot agent 审计：https://docs.github.com/en/copilot/reference/enterprise-administrators/agentic-audit-log-events
- LangGraph HITL：https://docs.langchain.com/oss/python/langgraph/interrupts
- Plane MCP：https://www.npmjs.com/package/@makeplane/plane-mcp-server
- Gartner 2026 预测：https://www.gartner.com/en/newsroom/press-releases/2025-08-26-*
