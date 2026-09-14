# Pulse MVP 合并后待办（源自最终全分支审查 triage）

> 2026-09-15，最终审查结论 With fixes → 修复波 eef07fe 后收口。以下为审查裁定 DEFER 的快速改进项与真实使用前置条件。

## 建议尽快做（小改动）

1. 从 `internal/rules` 导出 `BlockedDaysDefault`/`LoadHorizonDays` 常量，`internal/mcpserver/server.go` 与 `internal/reports/weekly.go`、`workload.go` 改为引用，消除三处重复。
2. `dependencies` 表加 `CREATE UNIQUE INDEX IF NOT EXISTS idx_dep ON dependencies(task_id, depends_on_task_id)`，并把冲突映射到 `ErrDuplicateDependency`（增量 schema 变更，对现有库安全）——关闭已 parked 的 TOCTOU。
3. `AutopushFunc` 钩子带上 agent 名（PULSE_ACTOR），让 MCP 触发的 sync_conflict/pull-archive activity 归因到 agent 而非人类。
4. README 增加"已知限制"一节：publish 追加不去重；Bitable 日期列为 text（甘特需手动改列类型）；Bitable 状态列不校验，非法值会钉住该记录直到修复。
5. `.gitignore` 的 `pulse` 改为 `/pulse`（避免误忽略任意深度的同名目录）。

## 真实使用前必须人工过一遍

- **执行 docs/superpowers/plans/e2e-checklist.md 全部 6 场景**（需要真实飞书测试租户）：bind/sync/publish 目前只经 httptest 验证，bind 部分失败的孤儿 base 行为、甘特视图手动建/改列步骤需要一次真人确认。
- 飞书权限 scope 名（bitable:app / docx:document / drive:file）在开放平台控制台开通时逐字核对。
- 双机场景（checklist 场景 4）用 `pulse feishu bind --app-token …` 采用模式实测收敛。

## 审查裁定记录（DEFER 摘要）

- 全部 14 个任务的 task-scoped minor 项均裁定 DEFER（详见 SDD ledger，工作区已删，裁定理由在 git 提交历史与各任务审查中）。
- parked 的 dependencies TOCTOU：单事务内 check-then-insert，本地单用户 CLI 风险极小，最坏情况重复一条依赖行（仅甘特文字标注重复）→ 上面第 2 条关闭它。
- warnWriter 仍是包级全局（测试注入用）：MCP 已改为 set-once，但未来若在工具回调里再 Set 会重新引入竞争——加注释警惕即可。
