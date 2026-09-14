# pulse MVP E2E 手动验收清单

逐条人工执行并勾选。命令均与当前代码的 CLI 旗标/输出一致（v b4d48a9+docs）。

标记说明：
- 【纯本地】不需要飞书，任何环境可直接执行；
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
