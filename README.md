# pulse

本地优先的项目管理 CLI：任务 / 版本 / 成员 / 活动全部存在本机 SQLite，可单人离线使用；
v1.1 起覆盖研发交付闭环六实体（需求 / 评审 / 会议 / bug / 提测单 / 发版记录），
需要多人协作时，通过飞书多维表格（Bitable）双向同步，报表与协作记录（评审纪要、
会议纪要、提测自检清单、发版清单等）可一键沉淀到飞书文档。
内置 MCP server（stdio），可被 ZCode / Claude Code / Codex 等编码代理直接驱动，
agent 的每次写入都带归因（`actor_type=agent` + `on_behalf_of`）。

## 安装

需要 Go 1.25+。

```bash
# 方式一：构建到当前目录
go build -o pulse .        # 或 make build

# 方式二：安装到 $(go env GOPATH)/bin（仓库根目录执行）
go install .
```

安装后确认：

```bash
pulse --help
```

## 数据存放与 PULSE_HOME

pulse 是本地优先：所有数据默认放在 `~/.pulse/` 下——

| 文件 | 说明 |
| --- | --- |
| `config.yaml` | 配置（default_actor、飞书凭据、同步参数） |
| `pulse.db` | SQLite 数据库（项目/任务/版本/成员/活动） |

设置环境变量 `PULSE_HOME` 可把整个数据目录重定向到任意路径（多机演练、测试隔离时很有用）：

```bash
PULSE_HOME=/tmp/p1 pulse init demo   # 配置与数据库都落在 /tmp/p1/ 下
```

`config.yaml` 示例（文件不存在时全部走默认值，也可先不创建）：

```yaml
default_actor: zhangyi        # 你的成员名（human）。必填：不配置任何命令都无法执行
feishu:
  app_id: cli_xxxxxxxx        # 飞书自建应用 App ID（未接飞书可先不配）
  # app_secret 建议不要写进文件，走环境变量 PULSE_FEISHU_APP_SECRET
sync:
  stale_minutes: 10           # 报表生成前超过该分钟数未拉取则先静默同步一次
```

环境变量 `PULSE_FEISHU_APP_SECRET` 优先于配置文件中的 `feishu.app_secret`。

## 五分钟上手

```bash
# 0. 先写最小配置（default_actor 必填，否则所有命令报"默认执行者为空"）
mkdir -p ~/.pulse
printf 'default_actor: %s\n' "$(whoami)" > ~/.pulse/config.yaml

# 1. 创建项目
pulse init demo --name 演示项目

# 2. 添加成员（--type human|agent，--capacity 每周可投入人日）
pulse member add zhangyi --capacity 5
pulse member add codex --type agent

# 3. 建任务（--assignee 不存在时会按 human 自动创建）
pulse task add "搭建骨架" --project demo --assignee zhangyi \
  --start 2026-09-14 --due 2026-09-20 --estimate 2
pulse task list --project demo

# 4. 建版本并把任务挂到版本
pulse version add v0.1 --project demo --target 2026-10-01
pulse task update 1 --version v0.1 --status in_progress

# 5. 生成报表（gantt/workload/versions/weekly；--out 缺省打印到 stdout）
pulse report gantt --project demo --out gantt.html   # 浏览器打开即是甘特图
pulse report weekly --project demo                    # 周报 Markdown
```

## 接入编码代理（MCP）

`pulse mcp` 以 stdio 传输运行 MCP server。agent 身份来自环境变量 `PULSE_ACTOR`
（为空拒绝启动），数据目录同样可用 `PULSE_HOME` 指定。在编码代理的项目里放
`.mcp.json`（Claude Code / Codex / ZCode 通用格式）：

```json
{
  "mcpServers": {
    "pulse": {
      "command": "/path/to/pulse",
      "args": ["mcp"],
      "env": { "PULSE_ACTOR": "codex", "PULSE_HOME": "/Users/me/.pulse" }
    }
  }
}
```

> 把 `/path/to/pulse` 换成实际二进制路径（`which pulse`）；`PULSE_HOME` 建议用绝对路径。

重启代理后即可对话式驱动，共 27 个工具——12 个核心工具：`list_projects`、`get_project_status`、
`list_tasks`、`add_task`、`update_task`、`add_dependency`、`list_versions`、
`add_version`、`update_version`、`list_members`、`get_workload`、`publish_feishu`；
15 个研发交付闭环工具：`create_requirement` / `update_requirement` / `list_requirements`、
`create_bug` / `update_bug` / `list_bugs`、`create_test_submission` / `update_test_submission` /
`list_test_submissions`、`create_release` / `update_release` / `list_releases`、
`create_review`、`list_reviews`、`list_meetings`。

每次 agent 写入都会在 activity 里归因：`actor_type=agent`、执行者是 `PULSE_ACTOR`
对应的 agent 成员；代理"代表某人"操作时传 `delegated_by`，activity 的
`on_behalf_of` 记录被代表的人类成员。CLI 侧等价物是全局旗标
`pulse --agent codex --delegated-by zhangyi task add ...`。

## 飞书自建应用开通（可选）

用于多人经 Bitable 同步、报表沉淀到飞书文档。纯本地使用可完全跳过本节。

1. 打开[飞书开放平台开发者后台](https://open.feishu.cn/app)，创建一个**企业自建应用**，
   记下 App ID 与 App Secret。
2. 在「权限管理」中开通以下权限（按名称搜索勾选）：
   - **bitable 读写**：`bitable:app`（查看、评论、编辑和管理多维表格）
   - **docx 读写**：`docx:document`（查看、评论、编辑和管理新版文档）
   - **drive 文件创建**：`drive:file`（查看、编辑和管理云空间文件，用于创建沉淀文档）
3. 在「版本管理与发布」创建版本并发布（企业自建应用需管理员审批通过后生效）。
   授权身份使用应用身份（tenant token），pulse 会自动获取与刷新，无需手动处理。
4. 配置凭据（Secret 不要提交进任何仓库）：

   ```bash
   # app_id 写配置文件
   cat >> ~/.pulse/config.yaml <<'EOF'
   feishu:
     app_id: cli_xxxxxxxx
   EOF
   # app_secret 走环境变量（写入你的 shell rc 亦可）
   export PULSE_FEISHU_APP_SECRET=xxxxxx
   ```

5. 为项目创建同步 base 与沉淀文档：

   ```bash
   pulse feishu bind --project demo
   ```

   bind 成功后输出 base / 任务表 / 版本表 / 文档四个 token，并打印一条
   **其他机器共享提示**：另一台机器执行同样的
   `pulse feishu bind --project demo --app-token <app_token> --task-table <table_id> --version-table <table_id> --doc <doc_id>`
   即可采用既有 base（不创建任何飞书资源，实现多机共享同一张表）。

6. 同步与沉淀：

   ```bash
   pulse sync --project demo                              # 显式双向同步（先推后拉）
   pulse feishu publish --project demo --report weekly    # 周报沉淀到飞书文档
   ```

   说明：
   - 未配置飞书或未绑定时，`sync` 会给出引导性报错，本地功能完全不受影响；
   - 写命令（task/version 变更）成功后会自动尽力推送，失败只打 stderr 警告，不影响退出码；
   - `report` 生成前若超过 `sync.stale_minutes` 未拉取会先静默同步一次，失败仅提示数据可能滞后。

## 命令速查

| 命令 | 说明 |
| --- | --- |
| `pulse init <key> [--name --desc]` | 创建项目 |
| `pulse project list` | 列出项目 |
| `pulse member add <name> [--type human\|agent] [--capacity N]` / `pulse member list` | 成员管理 |
| `pulse task add <title> --project K [--assignee --status --priority --estimate --start --due --version --desc]` | 创建任务 |
| `pulse task list --project K [--status --mine --overdue]` | 列出任务 |
| `pulse task update <id> [--title --status --assignee --due ...]` | 更新任务（done 改回其他状态自动记 reopen） |
| `pulse task rm <id>` | 软删任务（归档保留） |
| `pulse task dep <id> --on <taskID>` | 添加完成-开始（FS）依赖 |
| `pulse version add <name> --project K [--target YYYY-MM-DD]` / `list` / `update <id> [--status --target]` | 版本管理（planned\|in_dev\|released\|shipped） |
| `pulse requirement add <title> --project K [--owner --priority --status --no-doc]` / `list` / `update <id> [--status --owner --priority]` / `doc <id>` | 需求管理（proposed\|reviewing\|accepted\|in_dev\|delivered\|rejected；默认按模板建协作文档） |
| `pulse review record --project K [--requirement ID] --kind requirement\|release\|test [--conclusion pending --no-doc]` / `pulse review conclude <id> --conclusion passed\|passed_with_notes\|rejected` | 评审记录与结论（纪要内容在飞书文档协作） |
| `pulse meeting record <title> --project K [--no-doc]` / `pulse meeting list --project K` | 会议记录登记（纪要内容在飞书文档协作） |
| `pulse bug add <title> --project K [--severity 1-4 --assignee --requirement --found-version]` / `list [--status --severity]` / `update <id> [--status --assignee --severity]` | 缺陷管理（severity 1-4=P0-P3，缺省 3=P2；open\|fixing\|fixed\|verified\|closed\|wontfix） |
| `pulse submit create --project K --version V [--requirement --test-owner --no-doc]` / `list [--version]` / `pulse submit update <id> --status submitted\|testing\|passed\|failed` | 提测单管理（draft 提交到测试结论，时间自动补记） |
| `pulse release new --project K --version V [--manager --no-doc]` / `list` / `pulse release update <id> --status preparing\|testing\|released\|rolled_back` | 发版记录（进入 released 自动补记发布时间） |
| `pulse report gantt\|workload\|versions\|weekly --project K [--out FILE]` | 报表（gantt/workload/versions 为 HTML，weekly 为 Markdown） |
| `pulse sync --project K` | 双向同步（先推本地变更，再拉共享变更） |
| `pulse feishu bind --project K [--app-token X --task-table Y --version-table Z --doc W]` | 创建（或采用既有）飞书 base 与沉淀文档 |
| `pulse feishu publish --project K --report weekly\|versions\|all` | 报表沉淀到飞书文档（每次追加新块，带触发人落款） |
| `pulse mcp` | 启动 MCP server（stdio），需 `PULSE_ACTOR` 环境变量 |

## 每人日报

```bash
pulse report daily --project demo                 # 全员日报（每人一节）输出到终端
pulse report daily --project demo --person zhangyi --out zhangyi.md
pulse feishu publish --project demo --report daily # 全员日报沉淀到飞书文档（按人分节）
```

日报由任务数据自动生成（今日完成/进行中/明日计划/风险/名下 bug），感想与补充由成员直接写在飞书文档对应小节下；agent（claude/codex）的任务完成同样计入其个人日报。

## 已知限制

- **publish 追加不去重**：`pulse feishu publish` 每次向沉淀文档追加新块，不检测重复。同一份报表重复发布会产生重复段落；需要最新结论时以最新一次发布为准，旧段落需在飞书文档中手动清理。
- **记录文档内容不回流**：需求/评审/会议/提测单/发版的协作文档由 pulse 按模板**只建一次**，之后的正文编辑（结论、纪要、清单勾选）都在飞书文档中多人协作完成，pulse 不更新也不解析这些内容。实体的结构化状态流转只经 CLI/MCP 显式操作（如 `pulse review conclude`、`pulse bug update`）。
- **MCP 写不自动建记录文档**：经 MCP 工具（`create_requirement` 等）创建的记录不会自动建协作文档；CLI 创建命令默认自动建（`--no-doc` 跳过）。需要为 MCP 建的需求补建文档时执行 `pulse requirement doc <id>`（get-or-create，已建过则直接显示 token）。
- **旧 base 的列类型升级**：v1.1.1 起 bind 创建日期列为原生日期类型（甘特开箱可用）、状态/优先级/版本/人员等为单选（选项随指派自动创建，Bitable 侧手动新增选项经同步会自动注册为本地成员）。**注意：对已有数据的列做 text→单选的就地类型转换会丢值**（API 行为）——旧 base 请删表重新 bind，或清空本地 `bitable_synced_hash` 后 sync 全量重写。
- **Bitable 状态列不校验**：pulse 不校验飞书侧填入的状态词。在 Bitable 中把状态改成非法值后，该记录无法映射回本地状态，`pulse sync` 会告警并跳过这条记录（水位被压住、每轮重试），直到在飞书侧修正为止。
- **双机同时改同一条记录为整条 last-writer-wins**：没有字段级合并，后写入的一方覆盖整条记录。autopush 默认写完即推，被覆盖的一方通常无感知；`pulse sync` 的输出会对"本地近期修改被飞书侧覆盖"补一条警告；双方均有本地未同步改动时才另落 `sync_conflict` 活动备查。
- **双机共享 base 时六实体表 id 随 bind 旗标传递**：`feishu bind` 创建模式的"其他机器共享提示"会打印带全六实体表 id 的完整命令（`--requirements-table/--reviews-table/--meetings-table/--bugs-table/--submissions-table/--releases-table`），机器 B 整行复制到采用模式执行即可让六实体参与同步；未传的表保留本机既有值，六实体未配置时自动跳过、只同步任务与版本（不报错）。
- **协作文档链接列跨机共享（v1.2）**：bind 创建的六实体表（需求/评审/会议/bug/提测/发版）新增 协作文档 超链接列，`feishu_doc_token` 随记录同步：push 侧 token 非空即写 `打开文档` 链接；pull 侧本地还没建文档而远端有链接时采纳同一篇（单人协作文档跨机共享，不再分叉）。本地已有文档时忽略远端（不覆盖）；会议为只读记录，既有行不采纳（仅拉入新行时带上）。**旧 base 六实体表没有 协作文档 列**，请删表重新 bind 或在 Bitable 中手动补列（超链接类型），否则推送会因缺列失败（告警提示、不影响其他表）。
- **需求引用按全局 UID 解析（v1.2）**：需求创建时自动生成 32 位十六进制的全局 UID，随 Bitable 需求表的 需求UID 列同步；评审/bug/提测表 的 需求ID 列存的就是这个 UID，pull 侧按它还原本地关联——双机各自建需求再共享同一 base 不再因本地 id 撞号错链。兼容说明：旧 base 的 需求ID 列里是旧本地 id 数字串，读取时容忍按本地 id 解析一次并告警，push 一轮后即迁移为 UID；本地行的 UID 在首个同步轮次自动回填。**UID 采纳前的旧 UID 引用不可解析**（迁移期一次性：双机为同一共享记录回填了不同 UID、一方采纳对方 UID 后，其他机器仍带被取代旧 UID 的引用在本机查不到，会置空关联并告警；待其同步一轮把引用改写为最新 UID 后即恢复）。**旧 base 需求表没有 需求UID 列（bug/提测表没有 需求ID 列）**，请删表重新 bind 或在 Bitable 中手动补列（文本类型），否则这些表的推送会因缺列失败（告警提示、不影响其他表）。

## 开发

```bash
make build   # go build -o pulse .
make test    # go test ./...
make vet     # go vet ./...
```

环境变量 `PULSE_FEISHU_ENDPOINT` 可把飞书 API 域名重定向到自建地址（测试注入用）。
