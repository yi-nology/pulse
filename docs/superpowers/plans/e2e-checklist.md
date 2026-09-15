# pulse MVP E2E 手动验收清单

逐条人工执行并勾选。命令均与当前代码的 CLI 旗标/输出一致（v b4d48a9+docs）。

标记说明：
- 【纯本地】不需要飞书，任何环境可直接执行；
- 【仿真飞书】不需要真实租户：用仓库自带的仿真飞书服务器（`scripts/e2e/fakefeishu`，
  监听 `127.0.0.1:19090`）+ 环境变量 `PULSE_FEISHU_ENDPOINT=http://127.0.0.1:19090` 注入。
  `scripts/e2e/setup_v11.sh` 一键重建环境并自动断言本场景 7.2/7.3/7.4 的全部检查项；
- 【需要真实租户】需要一个真实飞书测试租户 + 自建应用（开通步骤见 README「飞书自建应用开通」，
  权限：bitable 读写 `bitable:app`、docx 读写 `docx:document`、drive 文件创建 `drive:file`，
  发布审批通过后使用）。禁止对着生产项目执行，全部用测试项目。

准备（一次性）：

```bash
make build                     # 产物 ./pulse，下文命令均用它
export PATH="$PWD:$PATH"
```

---

## 场景 1：本地上手 + 甘特图 【纯本地】

```bash
export PULSE_HOME=/tmp/p1
rm -rf "$PULSE_HOME" && mkdir -p "$PULSE_HOME"
printf 'default_actor: zhangyi\n' > "$PULSE_HOME/config.yaml"   # default_actor 必填

pulse init demo --name 演示项目                                 # → 项目已创建: demo (id=1)
pulse member add zhangyi --capacity 5                          # → 成员已添加: zhangyi (id=…)
pulse member add codex --type agent
pulse version add v0.1 --project demo --target 2026-10-01      # → 版本已创建: v0.1 (id=…)
pulse task add "搭建骨架" --project demo --assignee zhangyi \
  --version v0.1 --start 2026-09-14 --due 2026-09-20 --estimate 2   # → 任务已创建: 搭建骨架 (id=1)
pulse task add "写文档" --project demo --assignee zhangyi --due 2026-09-25  # (id=2)
pulse task dep 2 --on 1                                        # → 依赖已添加: 任务 2 依赖任务 1
pulse report gantt --project demo --out /tmp/p1/gantt.html     # → 报表已写入: /tmp/p1/gantt.html
open /tmp/p1/gantt.html
```

- [ ] 各命令输出与注释一致（`pulse task list --project demo` 能看到两行任务，带负责人/截止/版本列）
- [ ] gantt.html 浏览器可打开：版本泳道 + 任务条，任务 2 条下有「依赖 #1」文字标注（按被依赖任务 ID）
- [ ] （无 config.yaml 时）任意命令报 `默认执行者为空：必须提供 defaultActor 或 agentName` —— 引导文案生效

## 场景 2：feishu bind 创建 base / 文档 / 甘特视图 【需要真实租户】

沿用场景 1 的 `PULSE_HOME=/tmp/p1`，配置凭据：

```bash
cat >> /tmp/p1/config.yaml <<'EOF'
feishu:
  app_id: cli_xxxxxxxx        # 换成测试应用 App ID
EOF
export PULSE_FEISHU_APP_SECRET=xxxxxx   # 换成测试应用 App Secret，勿提交

pulse feishu bind --project demo
```

预期输出（token 为真实值）：

```
项目 demo 已绑定飞书:
  base(app_token):  …
  任务表(table_id): …
  版本表(table_id): …
  沉淀文档(doc):    …
其他机器共享提示: 执行 pulse feishu bind --project demo --app-token … --task-table … --version-table … --doc … 可绑定同一 base
日期列提示: 开始/截止 目前是文本列，甘特视图需日期类型；可在 Bitable 中将这两列改为日期类型（一次性手动操作）
```

- [ ] 飞书出现 base：含「任务表」「版本表」两张表
- [ ] 飞书出现「演示项目 沉淀文档」
- [ ] 任务表出现「甘特」视图（若 bind 的 stderr 出现
      `警告: 创建甘特视图失败（可在 Bitable 中手动创建一次）`，在任务表手动新建一次甘特视图）
- [ ] **甘特视图手动步骤**：任务表的「开始/截止」是文本列，甘特视图需日期类型——
      在 Bitable 中把这两列改为日期类型（一次性手动操作，改后甘特条才按日期渲染）
- [ ] 幂等：重跑 `pulse feishu bind --project demo` →
      `项目 demo 已绑定飞书，无需重复绑定（幂等跳过）`，飞书侧无新建资源
- [ ] Bitable 任务表此刻为空（bind 只建表结构，数据由 sync/push 写入）

## 场景 3：sync 双向同步 【需要真实租户】

```bash
pulse sync --project demo
# → 同步完成: 已推送 N、拉取 0、跳过回声 0、废弃 0、冲突 0
#   （N≥3：首次同步把场景 1 的 2 个任务 + 1 个版本推上全新 base；base 为空故拉取为 0）
pulse task list --project demo
pulse task add "同步验证" --project demo --assignee zhangyi
# → 任务已创建后写命令会自动尽力 push；Bitable 未出现记录时执行 pulse sync --project demo 补推
```

- [ ] Bitable 任务表出现「同步验证」记录，「任务名/状态/负责人/优先级/开始/截止/预估人日/版本」列有值
- [ ] 在 Bitable 中把「同步验证」的「状态」从 todo 改为 in_progress（只能填合法状态词：
      backlog|todo|in_progress|blocked|done），再执行 `pulse sync --project demo` →
      `pulse task list --project demo` 本地状态已变为 in_progress
- [ ] sync 输出行格式为
      `同步完成: 已推送 N、拉取 N、跳过回声 N、废弃 N、冲突 N`；冲突逐行 `冲突: …`、告警逐行 `警告: …`
- [ ] 回声抑制：本机刚推送的记录再 sync 时计入「跳过回声」而非「拉取」

## 场景 4：双机共享同一 base，互推互拉收敛 【需要真实租户】

第二台"机器"用另一个 PULSE_HOME 模拟另一人（真实双机同理），项目 key 必须一致：

```bash
export PULSE_HOME=/tmp/p2
rm -rf "$PULSE_HOME" && mkdir -p "$PULSE_HOME"
cat > /tmp/p2/config.yaml <<'EOF'
default_actor: li
feishu:
  app_id: cli_xxxxxxxx      # 与机器 A 相同（或同租户另一应用，需能访问该 base）
EOF
export PULSE_FEISHU_APP_SECRET=xxxxxx

pulse init demo --name 演示项目
# 采用模式：指向机器 A bind 输出中的既有 token（零 API 调用，不创建任何飞书资源）
pulse feishu bind --project demo \
  --app-token <A的app_token> --task-table <A的任务表id> --version-table <A的版本表id> --doc <A的doc_id>
# → 项目 demo 已绑定既有飞书 base:（下列 4 行 token 与 A 一致）
```

- [ ] 采用模式必须带 `--task-table` 与 `--version-table`（缺任一报
      `采用既有 base 时必须同时提供 --task-table 与 --version-table…`）
- [ ] `--doc` 可省略：省略时输出 `提示: 未绑定文档，publish 时会自动创建并写回`，
      之后首次 publish 自动补建文档并写回（可单独验证）
- [ ] 互推：机器 A `pulse task add "A侧任务" --project demo`（写后自动 push）→
      机器 B `pulse sync --project demo` 拉到「A侧任务」
- [ ] 互拉：机器 B `pulse task update <id> --status blocked` →
      机器 A `pulse sync --project demo` 后状态变 blocked
- [ ] 反复几轮后两侧 `pulse task list --project demo` 完全一致（收敛）
- [ ] （可选）两侧几乎同时改同一任务再各自 sync：按更新时间 LWW 判定，后写胜出，
      输出含 `冲突: …` 行，最终两侧一致

## 场景 5：MCP 接入编码代理 + 归因 【纯本地（publish_feishu 工具部分需要真实租户）】

按 README 放好 `.mcp.json`（`command` 指向真实二进制，env 带 `PULSE_ACTOR=codex` 与
`PULSE_HOME=/tmp/p1`），重启编码代理后对话驱动：

> 帮我在 demo 项目里加一个任务"代理建的任务"，负责人是我（zhangyi），状态 todo；
> 然后把它的状态改成 in_progress。（agent 应调用 add_task / update_task，
> update 前先 list_tasks 确认 id；add_task 时请传 delegated_by="zhangyi"）

- [ ] 工具返回 JSON（两空格缩进），任务创建/更新成功
- [ ] 归因检查（activity 表）：

  ```bash
  sqlite3 /tmp/p1/pulse.db \
    "select id, actor_id, actor_type, on_behalf_of, action, entity_type, entity_id from activity order by id desc limit 5"
  pulse member list    # 对照成员 id
  ```

  新增行满足：`actor_type=agent`、`actor_id` 指向 members 中 name=codex 且 type=agent 的行、
  `on_behalf_of` = zhangyi 的成员 id、action 为 create / update_status
- [ ] CLI 等价物形状一致：`pulse --agent codex --delegated-by zhangyi task add "CLI代理" --project demo`
      产生同样的 actor/on_behalf_of 归因
- [ ] （需要真实租户）让 agent 调 `publish_feishu(project="demo", report="weekly")` →
      返回含真实 `doc` token，文档末尾出现新周报块

## 场景 6：publish 周报 + 归因落款 【需要真实租户】

```bash
export PULSE_HOME=/tmp/p1          # 项目已 bind（场景 2），有任务数据（场景 1/3/5）
pulse feishu publish --project demo --report weekly
# → 报表已沉淀到飞书文档: <doc_token>（report: weekly，触发人: zhangyi）
```

打开项目绑定的沉淀文档，检查本次追加的块：

- [ ] 顶部防混淆标题：`周报 <年>-W<周>（生成于 <YYYY-MM-DD HH:MM>，由 zhangyi 触发）`
- [ ] 正文块齐全：本周完成 / 进行中 / 下周计划 / 风险清单 / 未排期 / Agent 贡献；
      场景 5 中代理创建并推进的任务出现在对应分区（Agent 贡献能看到 codex 的动作）
- [ ] 底部落款：`由 pulse 导出 · 触发人 zhangyi`
- [ ] 重跑一次 publish → 文档末尾追加新段落（时间线性质，不去重，属预期行为）
- [ ] （可选）`--report versions`：标题形如 `版本规划（生成于 …，由 zhangyi 触发）`，落款同上；
      `--report all`：周报块 + 版本规划块先后追加

---

## 场景 7：研发交付闭环（v1.1）

六实体（需求/评审/会议/bug/提测单/发版记录）全链 + 协作记录文档 + 双机收敛 + agent 归因。
一键脚本：`scripts/e2e/setup_v11.sh`（重建仿真飞书与双机 HOME，跑通 7.2/7.3/7.4 全部断言）。

### 7.1 全链：需求 → 评审 → 拆任务 → bug → 提测 → 发版 【纯本地】

```bash
export PULSE_HOME=/tmp/p7 && rm -rf "$PULSE_HOME" && mkdir -p "$PULSE_HOME"
printf 'default_actor: zhangsan\n' > "$PULSE_HOME/config.yaml"

pulse init demo --name 演示项目
pulse member add zhangsan && pulse member add lisi && pulse member add codex --type agent
pulse version add v1.0 --project demo --target 2026-09-30

pulse requirement add "支持扫码登录" --project demo --owner zhangsan --priority 2
# → 需求已创建: 支持扫码登录 (id=1)；已配飞书时追加一行 协作文档已创建: <token>，
#   未配飞书时 stderr 提示: 提示: 已保存记录（无文档）: 未配置飞书：…
pulse requirement list --project demo                       # ID/标题/状态/负责人/优先级
pulse requirement update 1 --status accepted                # proposed→…→delivered 六态流转
pulse review record --project demo --kind requirement --requirement 1   # → 评审已记录 (id=1)
pulse review conclude 1 --conclusion passed                 # passed|passed_with_notes|rejected
pulse meeting record "需求评审会" --project demo             # → 会议已记录 (id=1)
pulse task add "扫码登录联调" --project demo --assignee lisi --due 2026-09-25
# 拆任务：tasks 表已预留 requirement_id 列，但 CLI/MCP 暂无 --requirement 旗标（已知限制）
pulse bug add "扫码后页面白屏" --project demo --severity 2 --assignee zhangsan \
  --requirement 1 --found-version v1.0                      # severity 1-4 = P0-P3，缺省 3=P2
pulse bug list --project demo --severity 2
pulse bug update 1 --status fixing                          # open|fixing|fixed|verified|closed|wontfix
pulse submit create --project demo --version v1.0 --requirement 1 --test-owner lisi
pulse submit update 1 --status submitted                    # submitted|testing|passed|failed
pulse release new --project demo --version v1.0 --manager zhangsan
pulse release update 1 --status released                    # preparing|testing|released|rolled_back
```

- [ ] 每条命令输出与注释一致；`pulse submit list` / `release list` 的状态与时间列正确
      （进入 released 后发布时间自动补记）
- [ ] 状态枚举校验：`pulse requirement update 1 --status done` →
      `Error: status 必须为 proposed|reviewing|accepted|in_dev|delivered|rejected，收到 "done"`
      （bug/submit/release 同理，枚举见各命令 --status 帮助）
- [ ] 悬空引用校验：`pulse bug add x --project demo --requirement 99` →
      `Error: 需求不存在: id=99`；`pulse review conclude 99 --conclusion passed` →
      `Error: 评审不存在: id=99`；`pulse submit create --version 不存在版本` → `Error: 版本不存在: …`
- [ ] 无文档降级（未配飞书时）：requirement/review/meeting/submit/release 的创建命令
      stderr 出现 `提示: 已保存记录（无文档）: …`，命令本身退出码 0，实体正常可列可流转
- [ ] `--no-doc` 跳过建文档：`pulse meeting record "纯记录" --project demo --no-doc`
      不产生任何文档提示

### 7.2 记录文档模板检查（仿真飞书 dump 断言 5 类标题）【仿真飞书】

```bash
scripts/e2e/setup_v11.sh          # 一键跑通；下面为手动分步时的断言方法
curl -s http://127.0.0.1:19090/_debug/dump | python3 -c \
  "import json,sys; [print(t) for t in sorted(d['title'] for d in json.load(sys.stdin)['docs'].values())]"
```

- [ ] dump 中出现 5 类模板文档，标题逐字为：
      `需求 · 支持扫码登录`、`评审记录 · requirement · 需求#1`、`会议纪要 · 需求评审会`、
      `提测单 · v1.0 · #1`、`发版记录 · v1.0`
- [ ] 每类文档含模板节块：需求（背景/描述、状态与负责人、行动项提示）、评审/会议（行动项
      todo 块）、提测单（自检清单 todo 三项）、发版（检查清单 todo 三项）
- [ ] token 回填：sqlite3 查 `requirements.feishu_doc_token` 非空且等于 dump 中的 doc id
- [ ] 幂等：重跑 `pulse requirement doc 1` → `协作文档: <token>`（直接返回，dump 中文档数不变）
- [ ] `pulse requirement doc <未建文档的id>` → `协作文档已创建: <新token>`（get-or-create 补建）
- [ ] MCP 写不自动建记录文档：经 MCP `create_requirement` 建的需求在 dump 中无对应文档
      （补建用 CLI `pulse requirement doc <id>`，见 README 已知限制）

### 7.3 双机 six-table 收敛 【仿真飞书】（真实租户同理）

沿用 setup_v11.sh 的双机编排（p1 创建共享，p2 采用）：

```bash
# p2 采用既有 base（经典 4 token；六实体表 id 不经 CLI 传递，见下方首条勾选项）
pulse feishu bind --project demo --app-token app1 --task-table tbl1 --version-table tbl2 --doc doc6
pulse sync --project demo                      # 先拉到版本与任务
# 六实体表 id 需共享方告知：从 p1 库拷贝 feishu_tables_json（setup_v11.sh 用 sqlite3 完成）
pulse sync --project demo                      # 第二次：拉入需求/评审/会议/bug/提测/发版
```

- [ ] p2 首次 sync：`已推送 0、拉取 N`（N=版本+任务数）；六表配置后第二次 sync 拉入全部
      六实体行，`pulse requirement/bug/submit/release/meeting list` 与 p1 内容一致
- [ ] p2 → p1：p2 修改需求状态（写后自动 push）→ p1 `pulse sync` 收敛，两侧同标题行状态一致
      （注意：跨机本地 ID 不保证一致——pull 建行顺序不定，定位一律按标题/内容）
- [ ] 覆盖警告（六实体泛化）：p1 已同步的需求被 p2 修改后，p1 sync 输出
      `警告: 需求 #N <标题> 被飞书侧更新覆盖（覆盖的是你已同步到飞书的修改）`
- [ ] base 中 8 张表（任务/版本/需求/评审/会议/bug/提测/发版）各有预期记录数；
      会议表只有 标题/时间 两列，p2 侧会议「创建人」显示为 p2 操作者（创建人不随表同步，属预期）
- [ ] 回声抑制：sync 输出的「跳过回声」计入本机刚推送的行，不重复合入

### 7.4 MCP agent 建 bug/需求 + activity 归因 【纯本地】

```bash
PULSE_ACTOR=codex PULSE_HOME=/tmp/p7 pulse mcp     # agent 经 stdio 接入后对话驱动，或：
python3 scripts/e2e/mcp_driver_v11.py /tmp/p7      # 脚本驱动：27 个工具（12 核心 + 15 交付闭环）
```

- [ ] tools/list 含 15 个新工具：`create_requirement` / `update_requirement` /
      `list_requirements` / `create_bug` / `update_bug` / `list_bugs` /
      `create_test_submission` / `update_test_submission` / `list_test_submissions` /
      `create_release` / `update_release` / `list_releases` / `create_review` /
      `list_reviews` / `list_meetings`
- [ ] agent 建「需求 → bug 关联该需求 → bug 状态 fixed」全链成功，返回两空格缩进 JSON
- [ ] 归因检查：

  ```bash
  sqlite3 /tmp/p7/pulse.db \
    "SELECT a.actor_type, m.name, m2.name AS behalf, a.action, a.entity_type, a.entity_id
     FROM activity a JOIN members m ON m.id=a.actor_id
     LEFT JOIN members m2 ON m2.id=a.on_behalf_of ORDER BY a.id DESC LIMIT 6"
  ```

  agent 写入满足：`actor_type=agent`、执行者为 codex（type=agent）、传 `delegated_by`
  时 `on_behalf_of` 指向被代理人（zhangsan）；未传 delegated_by 的调用
  （如 `update_bug`）on_behalf_of 为空
- [ ] MCP 建的 bug 在 `pulse bug list` 可见、处理人/发现版本按名解析正确

### 7.5 真实租户增补检查 【需要真实租户】

在真实飞书租户重复 7.2/7.3（bind → sync → 六实体写命令 → 双机）：

- [ ] bind 一次建出 8 张表 + 沉淀文档；六实体表列与仿真一致（全 text + 已废弃 + updated_by）
- [ ] 5 类记录文档在真实飞书文档中打开正常：标题/节标题/todo 块可勾选，多人可协作编辑
- [ ] 文档内容编辑后 `pulse sync` 不回流：pulse 侧实体字段不变（记录文档永不解析回流）
- [ ] 需求/评审/会议/提测/发版在 Bitable 侧修改状态词（合法枚举）后 sync 能合回本地；
      改成非法枚举时 sync 告警跳过该记录（与任务表行为一致）
